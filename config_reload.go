package pinpoint

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigReloadStatus is the outcome of ReloadConfig.
type ConfigReloadStatus int

const (
	// ConfigReloadApplied: the dynamic options were applied; RestartRequired
	// lists the non-dynamic ones the YAML would have changed.
	ConfigReloadApplied ConfigReloadStatus = iota
	// ConfigReloadNoAgent: NewAgent has not run, failed, or the agent was shut
	// down. Nothing was changed.
	ConfigReloadNoAgent
	// ConfigReloadConfigFileActive: the running agent reads a config file
	// (ConfigFile), which outranks YAML as it does at startup, so the file is
	// where the configuration changes. Nothing was changed.
	ConfigReloadConfigFileActive
	// ConfigReloadInvalidYAML: a YAML syntax error, or a document that is not
	// a mapping - a scalar, a sequence or an empty document. Nothing was
	// changed.
	ConfigReloadInvalidYAML
	// ConfigReloadInvalidConfig: a value of the wrong type for its option, or
	// a configuration NewAgent would refuse - no ApplicationName, or an
	// invalid identity. Nothing was changed.
	ConfigReloadInvalidConfig
	// ConfigReloadError: an unexpected internal error. Nothing was changed
	// unless the Message says otherwise.
	ConfigReloadError
)

// String spells the status as the other Pinpoint agents do, for a log line.
func (s ConfigReloadStatus) String() string {
	switch s {
	case ConfigReloadApplied:
		return "applied"
	case ConfigReloadNoAgent:
		return "no_agent"
	case ConfigReloadConfigFileActive:
		return "config_file_active"
	case ConfigReloadInvalidYAML:
		return "invalid_yaml"
	case ConfigReloadInvalidConfig:
		return "invalid_config"
	case ConfigReloadError:
		return "error"
	}
	return fmt.Sprintf("ConfigReloadStatus(%d)", int(s))
}

// ConfigReloadResult is what ReloadConfig reports.
type ConfigReloadResult struct {
	Status ConfigReloadStatus
	// RestartRequired names, by config key ("Collector.Host"), the
	// non-dynamic options whose value the YAML would have changed. They keep
	// their running values; a restart with the same configuration applies
	// them. Empty unless Status is ConfigReloadApplied.
	RestartRequired []string
	// Message says why the reload was refused. Empty on an applied reload.
	Message string
}

// OK reports whether the reload was applied.
func (r ConfigReloadResult) OK() bool {
	return r.Status == ConfigReloadApplied
}

// ReloadConfig applies configYAML, a complete configuration in the config
// file's YAML form, to the running agent. It is for a process that manages the
// agent's configuration itself - a control plane pushing new settings, an
// admin endpoint, a SIGHUP handler - and so has no config file for the agent
// to watch. It never panics; the result says what happened.
//
// The YAML is the whole configuration, as a config file is: the options
// marked dynamic in the configuration reference take the value it gives
// them, and a dynamic option it omits goes back to the value it had before
// any YAML or config file set it, the config function's or the default. The
// result therefore depends only on the last YAML applied, not on the history
// of calls. Precedence is the startup precedence: an option given by command
// line flag or environment variable, or set through Config.Set, keeps that
// value. The components derived from the changed options are rebuilt behind
// the config snapshot, as on a config file reload, and the reload callbacks
// run for the options that changed.
//
// The non-dynamic options - the identity, Collector.*, the queue and cache
// sizes, Enable - keep their running values. Those the YAML would have given
// another value are returned in RestartRequired and logged as a warning, so
// the caller can tell that a restart is needed to apply them. An option set
// by a flag or an environment variable would not change on a restart either,
// so it is not listed.
//
// The reload is refused, with nothing changed, when no agent is running
// (ConfigReloadNoAgent), when the agent reads a config file, which outranks
// YAML as it does at startup (ConfigReloadConfigFileActive), when the YAML
// does not parse or is not a mapping (ConfigReloadInvalidYAML), and when a
// value has the wrong type for its option or the configuration is one
// NewAgent would refuse - an empty ApplicationName, an invalid ServiceName
// for Uid.Version v4 (ConfigReloadInvalidConfig). Unlike a config file, a
// YAML with a value of the wrong type is refused as a whole rather than
// applied without that value: a caller that composes the document gets told
// instead of running on a partly applied one. An out-of-range value and an
// unknown key are handled as at startup: the option falls back to its
// default, the key is ignored, each with a warning.
//
// ReloadConfig is safe to call from any goroutine while requests create
// spans; concurrent calls and Config.Set are serialized.
func ReloadConfig(configYAML string) (result ConfigReloadResult) {
	defer func() {
		if e := recover(); e != nil {
			Log("config").Errorf("config reload panic: %v\n%s", e, debug.Stack())
			result = ConfigReloadResult{Status: ConfigReloadError, Message: fmt.Sprintf("config reload panic: %v", e)}
		}
	}()

	agent := GetAgent()
	if agent == NoopAgent() {
		return refuseReload(ConfigReloadNoAgent, "no agent is running: NewAgent has not run, failed, or the agent was shut down")
	}
	return agent.Config().reloadYAML(configYAML)
}

func refuseReload(status ConfigReloadStatus, message string) ConfigReloadResult {
	Log("config").Warnf("config reload refused (%s): %s", status, message)
	return ConfigReloadResult{Status: status, Message: message}
}

// reloadYAML is ReloadConfig on this Config, see there.
func (config *Config) reloadYAML(text string) ConfigReloadResult {
	result, changed, callback := config.stageYAML(text)
	if !result.OK() {
		return result
	}

	Log("config").Infof("config reloaded from yaml, %d option(s) changed: %s",
		len(changed), strings.Join(slices.Sorted(maps.Keys(changed)), ", "))
	if len(result.RestartRequired) > 0 {
		Log("config").Warnf("non-dynamic config options changed by the yaml keep their running values, restart required: %s",
			strings.Join(result.RestartRequired, ", "))
	}
	// Callbacks read the config they were just given a new generation of, and
	// may register further callbacks, so they run off a copy with the lock
	// released, as on a config file reload.
	for _, cb := range callback {
		cb.do(changed)
	}
	return result
}

// stageYAML resolves and publishes the YAML under config.mu, and returns the
// result with the dynamic options that changed and the callbacks to run. The
// YAML is resolved on a private copy of the staging area first - every option,
// the non-dynamic ones included, normalized by publish and with the identity
// resolved as NewAgent resolves it - so that the comparison that produces
// RestartRequired compares like with like, and so that a refused YAML leaves
// the Config untouched. Only the dynamic options are then copied over, and the
// components the candidate built from them - and the warnings it logged
// building them - are taken over rather than built a second time. The lock is
// released by defer so that a panic, which ReloadConfig recovers, cannot
// leave it held.
func (config *Config) stageYAML(text string) (ConfigReloadResult, map[string]bool, []reloadCallback) {
	config.mu.Lock()
	defer config.mu.Unlock()

	if config.configFileCfg != nil {
		return refuseReload(ConfigReloadConfigFileActive,
			"config file "+config.configFileCfg.path+" is the configuration source and outranks yaml; change the file instead"), nil, nil
	}

	f, err := parseConfigYAML(text)
	if err != nil {
		return refuseReload(ConfigReloadInvalidYAML, err.Error()), nil, nil
	}
	config.warnUnknownKeys(f)

	sources := &cfgSources{file: f}
	sources.profile = config.loadProfile(sources)
	if bad := config.checkValueTypes(sources); len(bad) > 0 {
		return refuseReload(ConfigReloadInvalidConfig, strings.Join(bad, "; ")), nil, nil
	}

	candidate := config.reloadCandidate()
	candidate.restage(sources, true)
	candidate.publish()
	objName, err := resolveObjectNameKeeping(candidate, config.objName)
	if err != nil {
		return refuseReload(ConfigReloadInvalidConfig, err.Error()), nil, nil
	}
	// checkNameAndID writes the resolved identity back, so the running values
	// are the resolved ones; compare the candidate's resolved values likewise.
	candidate.setResolvedIdentity(objName)

	var restart []string
	changed := make(map[string]bool)
	for _, k := range slices.Sorted(maps.Keys(config.cfgMap)) {
		v, c := config.cfgMap[k], candidate.cfgMap[k]
		same := reflect.DeepEqual(v.value, c.value)
		// ActiveProfile selected the profile within this YAML, as it does
		// within the config file on a file reload, so it is in effect rather
		// than pending a restart.
		if v.dynamic || k == CfgActiveProfile {
			if !same {
				changed[k] = true
			}
			v.value, v.source = c.value, c.source
		} else if !same {
			restart = append(restart, k)
		}
	}
	config.publishFrom(candidate.load())
	callback := slices.Clone(config.callback)
	return ConfigReloadResult{Status: ConfigReloadApplied, RestartRequired: restart}, changed, callback
}

// parseConfigYAML reads a YAML document into the config file form every
// option is looked up in. A document that is not a mapping is an error, the
// empty document included: an empty string is far more often a failed read
// upstream than a request for every default, and as the complete
// configuration it would reset every dynamic option. `{}` asks for that
// explicitly.
func parseConfigYAML(text string) (*configFile, error) {
	var tree map[string]any
	if err := yaml.Unmarshal([]byte(text), &tree); err != nil {
		return nil, fmt.Errorf("config yaml: %w", err)
	}
	if tree == nil {
		return nil, errors.New("config yaml: the document is empty or not a mapping")
	}
	f := newConfigFile("")
	flattenConfig("", tree, f.values)
	return f, nil
}

// warnUnknownKeys logs the leaf keys of f that name no option. A config file
// is read the other way round - each option looks itself up, so a key it
// does not know is never visited - which is fine for a file an operator
// edits by hand next to the documentation, but a YAML a program composes
// deserves to hear that a key it spelled wrong did nothing.
func (config *Config) warnUnknownKeys(f *configFile) {
	known := make(map[string]bool, len(config.cfgMap))
	for k := range config.cfgMap {
		known[strings.ToLower(k)] = true
	}
	for _, k := range slices.Sorted(maps.Keys(f.values)) {
		if _, isMapping := f.values[k].(map[string]any); isMapping {
			continue
		}
		if known[k] || k == "profile" || strings.HasPrefix(k, "profile.") {
			continue
		}
		Log("config").Warnf("config yaml: unknown key %s is ignored", k)
	}
}

// checkValueTypes reports every option the sources set to a value that does
// not convert to the option's type, as "<key> = <value> is not a valid <type>".
// The caller must hold config.mu.
func (config *Config) checkValueTypes(sources *cfgSources) []string {
	var bad []string
	for _, k := range slices.Sorted(maps.Keys(config.cfgMap)) {
		v := config.cfgMap[k]
		value, _, ok := sources.lookup(k, v)
		if !ok {
			continue
		}
		if _, err := convertCfgValue(v.valueType, value); err != nil {
			bad = append(bad, fmt.Sprintf("%s = %v is not a valid %s", k, value, cfgTypeName(v.valueType)))
		}
	}
	return bad
}

// reloadCandidate returns a private Config holding a copy of the staging
// area and the published snapshot, for resolving a YAML without touching
// this Config. It has no callbacks, watcher or identity of its own, and is
// dropped once the reload is decided. The caller must hold config.mu.
func (config *Config) reloadCandidate() *Config {
	candidate := &Config{cfgMap: make(map[string]*cfgMapItem, len(config.cfgMap))}
	for k, v := range config.cfgMap {
		item := *v
		candidate.cfgMap[k] = &item
	}
	// publish reads the previous snapshot - to keep an unknown Log.Level at
	// the current one, and to carry the sampler over - so the candidate
	// starts from the running one.
	candidate.snapshot.Store(config.load())
	return candidate
}
