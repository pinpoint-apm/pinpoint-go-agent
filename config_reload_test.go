package pinpoint

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireNoAgent makes sure no agent from an earlier test is still the global
// one, so a test of ReloadConfig starts from a known state.
func requireNoAgent(t *testing.T) {
	t.Helper()
	if a := GetAgent(); a != NoopAgent() {
		a.Shutdown()
	}
	require.Equal(t, NoopAgent(), GetAgent())
}

// newReloadAgent starts an agent that talks to no collector and stops it when
// the test ends. It names a service, which the default Uid.Version v4
// requires, ahead of opts.
func newReloadAgent(t *testing.T, opts ...ConfigOption) (Agent, *Config) {
	t.Helper()
	requireNoAgent(t)
	config, err := NewConfig(append([]ConfigOption{WithServiceName("test-service")}, opts...)...)
	require.NoError(t, err)
	config.offGrpc = true
	agent, err := NewAgent(config)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)
	return agent, config
}

func TestReloadConfig_NoAgent(t *testing.T) {
	requireNoAgent(t)

	result := ReloadConfig("applicationName: nobody\nsampling:\n  counterRate: 3\n")
	assert.Equal(t, ConfigReloadNoAgent, result.Status)
	assert.False(t, result.OK())
	assert.Contains(t, result.Message, "no agent is running")
	assert.Empty(t, result.RestartRequired)
}

func TestReloadConfig_AppliesDynamicAndReportsNonDynamic(t *testing.T) {
	_, config := newReloadAgent(t, WithAppName("reload-yaml"), WithSamplingCounterRate(5))

	var rateReloads, commitReloads int
	config.AddReloadCallback([]string{CfgSamplingCounterRate}, func() { rateReloads++ })
	config.AddReloadCallback([]string{CfgSQLTraceCommit}, func() { commitReloads++ })

	result := ReloadConfig(`
applicationName: reload-yaml
collector:
  host: other.collector
  agentPort: 19991
sampling:
  counterRate: 2
span:
  maxCallStackDepth: 12
`)
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.True(t, result.OK())
	assert.Empty(t, result.Message)
	assert.Equal(t, []string{CfgCollectorAgentPort, CfgCollectorHost}, result.RestartRequired)

	assert.Equal(t, 2, config.Int(CfgSamplingCounterRate), "dynamic option applied")
	assert.Equal(t, 12, config.Int(CfgSpanMaxCallStackDepth))
	assert.Equal(t, int32(12), config.load().spanMaxEventDepth, "snapshot rebuilt")
	assert.Equal(t, "localhost", config.String(CfgCollectorHost), "non-dynamic option kept")
	assert.Equal(t, 9991, config.Int(CfgCollectorAgentPort))
	assert.Equal(t, 1, rateReloads)
	assert.Equal(t, 0, commitReloads, "callback fired for an option the yaml did not change")

	// The same YAML again changes nothing, and still reports what a restart
	// would change.
	result = ReloadConfig("applicationName: reload-yaml\ncollector:\n  host: other.collector\nsampling:\n  counterRate: 2\nspan:\n  maxCallStackDepth: 12\n")
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Equal(t, []string{CfgCollectorHost}, result.RestartRequired)
	assert.Equal(t, 1, rateReloads, "callback fired for an unchanged option")

	// The YAML is the complete configuration: an omitted dynamic option goes
	// back to the config function's value or the default, not the last YAML's.
	result = ReloadConfig("applicationName: reload-yaml\n")
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Empty(t, result.RestartRequired)
	assert.Equal(t, 5, config.Int(CfgSamplingCounterRate), "back to the config function's value")
	assert.Equal(t, defaultEventDepth, config.Int(CfgSpanMaxCallStackDepth), "back to the default")
	assert.Equal(t, 2, rateReloads)
}

// An identity the YAML omits resolves to the running one: the generated agent
// name, the auto-detected container flag.
func TestReloadConfig_OmittedIdentityIsNotRestartRequired(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	_, config := newReloadAgent(t, WithAppName("reload-identity"))
	require.True(t, config.Bool(CfgIsContainerEnv))
	require.Equal(t, defaultAgentName(), config.String(CfgAgentName), "agent name is generated")

	result := ReloadConfig("applicationName: reload-identity\n")
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Empty(t, result.RestartRequired)

	// A changed identity is reported, with the YAML's value resolved as
	// NewAgent would resolve it.
	result = ReloadConfig("applicationName: reload-identity\nagentName: another-agent\n")
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Equal(t, []string{CfgAgentName}, result.RestartRequired)
	assert.NotEqual(t, "another-agent", config.String(CfgAgentName), "identity is not reloadable")
}

// The YAML's identity is trimmed as at startup, so whitespace alone is not a
// change, and a running v3 agent ignores the v4-only ApiKey on both sides.
func TestReloadConfig_TrimmedIdentityIsNotRestartRequired(t *testing.T) {
	t.Setenv("PINPOINT_GO_APPLICATIONNAME", "reload-trim\n")
	_, config := newReloadAgent(t, WithUidVersion("v3"), WithAgentName("trim-agent"), WithApiKey("unused-key"))
	require.Equal(t, "reload-trim", config.String(CfgAppName))
	require.Empty(t, config.String(CfgApiKey), "v3 does not use an api key")

	result := ReloadConfig("agentName: ' trim-agent '\napiKey: another-key\n")
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Empty(t, result.RestartRequired)
}

func TestReloadConfig_EnvAndSetValuesWin(t *testing.T) {
	t.Setenv("PINPOINT_GO_SAMPLING_COUNTERRATE", "7")
	t.Setenv("PINPOINT_GO_COLLECTOR_HOST", "env.collector")
	_, config := newReloadAgent(t, WithAppName("reload-env"))
	config.Set(CfgSpanMaxCallStackDepth, 9)

	result := ReloadConfig(`
applicationName: reload-env
collector:
  host: yaml.collector
sampling:
  counterRate: 2
span:
  maxCallStackDepth: 20
  maxCallStackSequence: 500
`)
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Equal(t, 7, config.Int(CfgSamplingCounterRate), "environment variable keeps its value")
	assert.Equal(t, 9, config.Int(CfgSpanMaxCallStackDepth), "Set() keeps its value")
	assert.Equal(t, 500, config.Int(CfgSpanMaxCallStackSequence), "an unpinned option is applied")
	assert.Empty(t, result.RestartRequired, "an option pinned by the environment would not change on a restart either")
	assert.Equal(t, "env.collector", config.String(CfgCollectorHost))
}

func TestReloadConfig_ProfileInsideTheYAML(t *testing.T) {
	_, config := newReloadAgent(t, WithAppName("reload-profile"))

	result := ReloadConfig(`
applicationName: reload-profile
activeProfile: dev
sampling:
  counterRate: 2
profile:
  dev:
    sampling:
      counterRate: 3
`)
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Equal(t, 3, config.Int(CfgSamplingCounterRate), "the active profile overrides the top level")
	assert.Equal(t, "dev", config.String(CfgActiveProfile))
	assert.Empty(t, result.RestartRequired, "the profile the yaml selected is in effect, not pending a restart")
}

func TestConfigReload_ProfilePrecedenceAndRemoval(t *testing.T) {
	const profiles = `sampling:
  counterRate: 50
profile:
  dev:
    sampling:
      counterRate: 1
  prod:
    sampling:
      counterRate: 100
`
	for _, mode := range []string{"file", "yaml"} {
		for _, tc := range []struct {
			name, base, pinned string
			source             int
		}{
			{name: "default"},
			{name: "option", base: "prod"},
			{name: "env", pinned: "prod", source: cfgSrcEnv},
			{name: "cmd", pinned: "prod", source: cfgSrcCmd},
			{name: "set", pinned: "prod", source: cfgSrcAPI},
			{name: "set-empty", source: cfgSrcAPI},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				t.Setenv("PINPOINT_GO_ACTIVEPROFILE", "")
				swapForTest(t, &os.Args, []string{"profile-test"})
				if tc.source == cfgSrcEnv {
					t.Setenv("PINPOINT_GO_ACTIVEPROFILE", tc.pinned)
				} else if tc.source == cfgSrcCmd {
					os.Args = append(os.Args, "--pinpoint-activeprofile="+tc.pinned)
				}
				path := t.TempDir() + "/config.yaml"
				opts := []ConfigOption{WithAppName("reload-profile"), WithAgentName("reload-profile"), WithUidVersion("v3"), WithActiveProfile(tc.base)}
				if mode == "file" {
					require.NoError(t, os.WriteFile(path, []byte("activeProfile: dev\n"+profiles), 0o600))
					opts = append(opts, WithConfigFile(path))
				}
				config, err := NewConfig(opts...)
				require.NoError(t, err)
				config.Close() // Drive file reloads directly so the assertions do not race the watcher.
				if tc.source == cfgSrcAPI {
					config.Set(CfgActiveProfile, tc.pinned)
				}

				for _, selected := range []string{"dev", "prod", "omitted", ""} {
					text, want := profiles, selected
					if selected == "omitted" {
						want = tc.base
					} else {
						text = "activeProfile: '" + selected + "'\n" + text
					}
					if tc.source > cfgSrcProfile {
						want = tc.pinned
					}
					if mode == "file" {
						require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
						config.reloadConfig(newConfigFile(path))
					} else {
						result := config.reloadYAML(text)
						require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
						assert.Empty(t, result.RestartRequired)
					}
					assert.Equal(t, want, config.String(CfgActiveProfile), "selection: %q", selected)
					assert.Equal(t, map[string]int{"": 50, "dev": 1, "prod": 100}[want], config.Int(CfgSamplingCounterRate), "selection: %q", selected)
				}
			})
		}
	}
}

func TestReloadConfig_ConfigFileActive(t *testing.T) {
	path := t.TempDir() + "/pinpoint-config.yaml"
	writeConfigRate(t, path, 4)
	_, config := newReloadAgent(t, WithAppName("reload-file"), WithConfigFile(path))
	require.Equal(t, 4, config.Int(CfgSamplingCounterRate))

	result := ReloadConfig("applicationName: reload-file\nsampling:\n  counterRate: 2\n")
	assert.Equal(t, ConfigReloadConfigFileActive, result.Status)
	assert.Contains(t, result.Message, path)
	assert.Equal(t, 4, config.Int(CfgSamplingCounterRate), "the file stays the source")
}

func TestReloadConfig_InvalidYAML(t *testing.T) {
	_, config := newReloadAgent(t, WithAppName("reload-bad-yaml"), WithSamplingCounterRate(5))

	for name, text := range map[string]string{
		"syntax error": "sampling: [\n",
		"scalar":       "hello",
		"sequence":     "- applicationName: x\n",
		"empty":        "",
		"null":         "~\n",
	} {
		result := ReloadConfig(text)
		assert.Equal(t, ConfigReloadInvalidYAML, result.Status, name)
		assert.Contains(t, result.Message, "config yaml", name)
		assert.Equal(t, 5, config.Int(CfgSamplingCounterRate), "%s changed the config", name)
	}

	// An empty mapping is the explicit way to ask for every default.
	result := ReloadConfig("{}")
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Equal(t, 5, config.Int(CfgSamplingCounterRate), "the config function's value is the base, not the default")
}

func TestReloadConfig_InvalidConfig(t *testing.T) {
	_, config := newReloadAgent(t, WithAppName("reload-bad-config"), WithSamplingCounterRate(5))

	// A value of the wrong type refuses the whole document, nothing applied.
	result := ReloadConfig("applicationName: reload-bad-config\nsampling:\n  counterRate: abc\nspan:\n  maxCallStackDepth: 12\n")
	assert.Equal(t, ConfigReloadInvalidConfig, result.Status)
	assert.Contains(t, result.Message, CfgSamplingCounterRate+" = abc is not a valid int")
	assert.Equal(t, 5, config.Int(CfgSamplingCounterRate))
	assert.Equal(t, defaultEventDepth, config.Int(CfgSpanMaxCallStackDepth), "a valid option of a refused yaml was applied")

	// A configuration NewAgent would refuse.
	result = ReloadConfig("applicationName: \"\"\nsampling:\n  counterRate: 2\n")
	assert.Equal(t, ConfigReloadInvalidConfig, result.Status)
	assert.Contains(t, result.Message, "application name is required")
	assert.Equal(t, 5, config.Int(CfgSamplingCounterRate))

	result = ReloadConfig("applicationName: reload-bad-config\nuid:\n  version: v4\nserviceName: ab\nsampling:\n  counterRate: 2\n")
	assert.Equal(t, ConfigReloadInvalidConfig, result.Status)
	assert.Contains(t, result.Message, "service name is required")
	assert.Equal(t, 5, config.Int(CfgSamplingCounterRate))

	// A mapping where a scalar belongs is a wrong type, not a missing key.
	result = ReloadConfig("applicationName: reload-bad-config\nsampling:\n  counterRate:\n    nested: 1\n")
	assert.Equal(t, ConfigReloadInvalidConfig, result.Status)
	assert.Equal(t, 5, config.Int(CfgSamplingCounterRate))
}

func TestReloadConfig_InvalidListElementKeepsCurrentConfig(t *testing.T) {
	_, config := newReloadAgent(t, WithAppName("reload-bad-list"), WithSamplingCounterRate(5), WithSpanErrorMark("exception"))
	before := config.load()
	for _, invalid := range []string{"{exception: true}", "[exception]", "null"} {
		t.Run(invalid, func(t *testing.T) {
			result := ReloadConfig("sampling:\n  counterRate: 2\nspan:\n  errorMark: [sql, " + invalid + "]\n")
			assert.Equal(t, ConfigReloadInvalidConfig, result.Status)
			assert.Contains(t, result.Message, CfgSpanErrorMark)
			assert.Contains(t, result.Message, "is not a valid string slice")
			assert.Same(t, before, config.load(), "a refused YAML must not publish any changes")
			assert.Equal(t, 5, config.Int(CfgSamplingCounterRate))
			assert.True(t, config.load().marksError(ErrorCategoryException))
		})
	}
}

// An out-of-range value and an unknown key are handled as at startup, with the
// reload applied.
func TestReloadConfig_OutOfRangeAndUnknownKeysAreTolerated(t *testing.T) {
	_, config := newReloadAgent(t, WithAppName("reload-range"))

	result := ReloadConfig("applicationName: reload-range\nnoSuchOption: 1\nspan:\n  eventChunkSize: 0\n  queueSize: 0\n")
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Equal(t, defaultEventChunkSize, config.Int(CfgSpanEventChunkSize), "out-of-range dynamic value falls back to the default")
	assert.Empty(t, result.RestartRequired, "an out-of-range non-dynamic value normalizes to the running default")
}

// A panic inside the reload is reported as an error, and leaves the Config
// usable - the lock released - for the next call.
// The candidate resolves the YAML first, so that RestartRequired compares
// like with like, and the final publish takes its components over: a value
// the YAML got wrong is warned about once, not once per publish.
func TestReloadConfig_WarnsOnceAboutAValue(t *testing.T) {
	cfg, err := NewConfig(WithAppName("TestApp"), WithServiceName("TestService"))
	require.NoError(t, err)
	agent, err := NewTestAgent(cfg)
	require.NoError(t, err)
	defer agent.Shutdown()

	var buf bytes.Buffer
	defer captureWarnLog(&buf)()
	require.True(t, ReloadConfig("Span:\n  ErrorMark: bogus\nSampling:\n  CounterRate: -1\n").OK())

	// logrus escapes the quotes around bogus, so the match stops before them.
	assert.Equal(t, 1, strings.Count(buf.String(), "is not a valid error category"))
	assert.Equal(t, 1, strings.Count(buf.String(), "sampling counter rate -1 is negative"))

	// An unrelated reload does not repeat them either: the mask and the
	// sampler are carried over while their options stand.
	buf.Reset()
	require.True(t, ReloadConfig("Span:\n  ErrorMark: bogus\n  EventChunkSize: 10\nSampling:\n  CounterRate: -1\n").OK())
	assert.Empty(t, buf.String())
}

func TestReloadConfig_RecoversPanic(t *testing.T) {
	_, config := newReloadAgent(t, WithAppName("reload-panic"))
	const broken = "Test.Broken"
	config.mu.Lock()
	config.cfgMap[broken] = nil
	config.mu.Unlock()

	result := ReloadConfig("applicationName: reload-panic\n")
	assert.Equal(t, ConfigReloadError, result.Status)
	assert.Contains(t, result.Message, "panic")

	config.mu.Lock()
	delete(config.cfgMap, broken)
	config.mu.Unlock()
	result = ReloadConfig("applicationName: reload-panic\nsampling:\n  counterRate: 2\n")
	require.Equal(t, ConfigReloadApplied, result.Status, result.Message)
	assert.Equal(t, 2, config.Int(CfgSamplingCounterRate))
}

func TestReloadConfig_AfterShutdown(t *testing.T) {
	agent, _ := newReloadAgent(t, WithAppName("reload-shutdown"))
	agent.Shutdown()

	result := ReloadConfig("applicationName: reload-shutdown\n")
	assert.Equal(t, ConfigReloadNoAgent, result.Status)
}

// Reloads from several goroutines while requests create spans: run with -race.
func TestReloadConfig_ConcurrentWithSpans(t *testing.T) {
	agent, config := newReloadAgent(t, WithAppName("reload-concurrent"))
	yamls := []string{
		"applicationName: reload-concurrent\nsampling:\n  counterRate: 1\nspan:\n  maxCallStackDepth: 10\n",
		"applicationName: reload-concurrent\nsampling:\n  counterRate: 2\nspan:\n  maxCallStackDepth: 20\n",
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				result := ReloadConfig(yamls[(i+n)%2])
				assert.Equal(t, ConfigReloadApplied, result.Status, result.Message)
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				tracer := agent.NewSpanTracer("reload", "/concurrent")
				tracer.NewSpanEvent("work").EndSpanEvent()
				tracer.EndSpan()
				_ = config.Int(CfgSamplingCounterRate)
			}
		}()
	}
	wg.Wait()

	rate := config.Int(CfgSamplingCounterRate)
	assert.Contains(t, []int{1, 2}, rate)
	depth := config.load().spanMaxEventDepth
	assert.Equal(t, int32(rate*10), depth, "the last reload published its whole generation at once")
}

func TestConfigReloadStatus_String(t *testing.T) {
	for status, want := range map[ConfigReloadStatus]string{
		ConfigReloadApplied:          "applied",
		ConfigReloadNoAgent:          "no_agent",
		ConfigReloadConfigFileActive: "config_file_active",
		ConfigReloadInvalidYAML:      "invalid_yaml",
		ConfigReloadInvalidConfig:    "invalid_config",
		ConfigReloadError:            "error",
	} {
		assert.Equal(t, want, status.String())
	}
	assert.True(t, strings.HasPrefix(ConfigReloadStatus(42).String(), "ConfigReloadStatus("))
}
