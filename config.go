package pinpoint

import (
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"math"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cast"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config option keys
const (
	CfgAppName                    = "ApplicationName"
	CfgAppType                    = "ApplicationType"
	CfgAgentID                    = "AgentID"
	CfgAgentName                  = "AgentName"
	CfgCollectorHost              = "Collector.Host"
	CfgCollectorAgentPort         = "Collector.AgentPort"
	CfgCollectorSpanPort          = "Collector.SpanPort"
	CfgCollectorStatPort          = "Collector.StatPort"
	CfgLogLevelOld                = "LogLevel"
	CfgLogLevel                   = "Log.Level"
	CfgLogOutput                  = "Log.Output"
	CfgLogMaxSize                 = "Log.MaxSize"
	CfgSamplingType               = "Sampling.Type"
	CfgSamplingCounterRate        = "Sampling.CounterRate"
	CfgSamplingPercentRate        = "Sampling.PercentRate"
	CfgSamplingNewThroughput      = "Sampling.NewThroughput"
	CfgSamplingContinueThroughput = "Sampling.ContinueThroughput"
	CfgSpanQueueSize              = "Span.QueueSize"
	CfgSpanMaxCallStackDepth      = "Span.MaxCallStackDepth"
	CfgSpanMaxCallStackSequence   = "Span.MaxCallStackSequence"
	CfgStatCollectInterval        = "Stat.CollectInterval"
	CfgStatBatchCount             = "Stat.BatchCount"
	CfgIsContainerEnv             = "IsContainerEnv"
	CfgConfigFile                 = "ConfigFile"
	CfgActiveProfile              = "ActiveProfile"
	CfgSQLTraceBindValue          = "SQL.TraceBindValue"
	CfgSQLMaxBindValueSize        = "SQL.MaxBindValueSize"
	CfgSQLTraceCommit             = "SQL.TraceCommit"
	CfgSQLTraceRollback           = "SQL.TraceRollback"
	CfgEnable                     = "Enable"
	CfgHttpUrlStatEnable          = "Http.UrlStat.Enable"
	CfgHttpUrlStatLimitSize       = "Http.UrlStat.LimitSize"
)

const (
	cfgIdPattern             = "[a-zA-Z0-9\\._\\-]+"
	maxApplicationNameLength = 24
	maxAgentIdLength         = 24
	maxAgentNameLength       = 255
	samplingTypeCounter      = "COUNTER"
	samplingTypePercent      = "PERCENT"
)

// Config value type
const (
	CfgInt int = iota
	CfgFloat
	CfgBool
	CfgString
	CfgStringSlice
)

type cfgMapItem struct {
	value        interface{}
	defaultValue interface{}
	valueType    int
	cmdKey       string
	envKey       string
	dynamic      bool
	oldValue     interface{}
}

var (
	cfgBaseMap map[string]*cfgMapItem
)

func initConfig() {
	cfgBaseMap = make(map[string]*cfgMapItem, 0)

	AddConfig(CfgAppName, CfgString, "", false)
	AddConfig(CfgAppType, CfgInt, ServiceTypeGoApp, false)
	AddConfig(CfgAgentID, CfgString, "", false)
	AddConfig(CfgAgentName, CfgString, "", false)
	AddConfig(CfgCollectorHost, CfgString, "localhost", false)
	AddConfig(CfgCollectorAgentPort, CfgInt, 9991, false)
	AddConfig(CfgCollectorSpanPort, CfgInt, 9993, false)
	AddConfig(CfgCollectorStatPort, CfgInt, 9992, false)
	AddConfig(CfgLogLevelOld, CfgString, "info", true)
	AddConfig(CfgLogLevel, CfgString, "info", true)
	AddConfig(CfgLogOutput, CfgString, "stderr", true)
	AddConfig(CfgLogMaxSize, CfgInt, 10, true)
	AddConfig(CfgSamplingType, CfgString, samplingTypeCounter, true)
	AddConfig(CfgSamplingCounterRate, CfgInt, 1, true)
	AddConfig(CfgSamplingPercentRate, CfgFloat, 100, true)
	AddConfig(CfgSamplingNewThroughput, CfgInt, 0, true)
	AddConfig(CfgSamplingContinueThroughput, CfgInt, 0, true)
	AddConfig(CfgSpanQueueSize, CfgInt, defaultQueueSize, false)
	AddConfig(CfgSpanMaxCallStackDepth, CfgInt, defaultEventDepth, false)
	AddConfig(CfgSpanMaxCallStackSequence, CfgInt, defaultEventSequence, false)
	AddConfig(CfgStatCollectInterval, CfgInt, 5000, false)
	AddConfig(CfgStatBatchCount, CfgInt, 6, false)
	AddConfig(CfgIsContainerEnv, CfgBool, false, false)
	AddConfig(CfgConfigFile, CfgString, "", false)
	AddConfig(CfgActiveProfile, CfgString, "", false)
	AddConfig(CfgSQLTraceBindValue, CfgBool, true, false)
	AddConfig(CfgSQLMaxBindValueSize, CfgInt, 1024, false)
	AddConfig(CfgSQLTraceCommit, CfgBool, true, false)
	AddConfig(CfgSQLTraceRollback, CfgBool, true, false)
	AddConfig(CfgEnable, CfgBool, true, false)
	AddConfig(CfgHttpUrlStatEnable, CfgBool, false, true)
	AddConfig(CfgHttpUrlStatLimitSize, CfgInt, 1024, false)
}

// AddConfig adds a configuration item.
func AddConfig(cfgName string, valueType int, defaultValue interface{}, dynamic bool) {
	cfgBaseMap[cfgName] = &cfgMapItem{
		defaultValue: defaultValue,
		valueType:    valueType,
		cmdKey:       cmdName(cfgName),
		envKey:       envName(cfgName),
		dynamic:      dynamic,
	}
}

func cmdName(cfgName string) string {
	return "pinpoint-" + strings.ReplaceAll(strings.ToLower(cfgName), ".", "-")
}

func envName(cfgName string) string {
	return strings.ReplaceAll(strings.ToLower(cfgName), ".", "_")
}

// Config holds agent configuration, for passing to NewAgent.
type Config struct {
	cfgMap         map[string]*cfgMapItem
	containerCheck bool
	useNewLogOpt   bool
	offGrpc        bool //for test

	//dynamic config
	reloadCallback      []ConfigReloadCallback
	collectUrlStat      bool // CfgHttpUrlStatEnable
	sqlTraceBindValue   bool // CfgSQLTraceBindValue
	sqlMaxBindValueSize int  // CfgSQLMaxBindValueSize
	sqlTraceCommit      bool // CfgSQLTraceCommit
	sqlTraceRollback    bool // CfgSQLTraceRollback
}

// ConfigOption represents an option that can be passed to NewConfig.
type ConfigOption func(*Config)

// ConfigReloadCallback represents an option that can be passed to NewConfig.
type ConfigReloadCallback func(*Config)

// GetConfig returns a global Config created by NewConfig.
func GetConfig() *Config {
	return GetAgent().Config()
}

// Set stores the specified configuration item value.
func (config *Config) Set(cfgName string, value interface{}) {
	if v, ok := config.cfgMap[cfgName]; ok {
		v.value = value
	}
}

// Int returns an integer value for the specified configuration item.
func (config *Config) Int(cfgName string) int {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToInt(v.value)
	}
	return 0
}

// Float returns a float value for the specified configuration item.
func (config *Config) Float(cfgName string) float64 {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToFloat64(v.value)
	}
	return 0
}

// String returns a string value for the specified configuration item.
func (config *Config) String(cfgName string) string {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToString(v.value)
	}
	return ""
}

// StringSlice returns a string slice value for the specified configuration item.
func (config *Config) StringSlice(cfgName string) []string {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToStringSlice(v.value)
	}
	return []string{}
}

// Bool returns a boolean value for the specified configuration item.
func (config *Config) Bool(cfgName string) bool {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToBool(v.value)
	}
	return false
}

// NewConfig creates a Config populated with default settings, command line arguments,
// environment variables and the given config options.
// Config uses the following precedence order. Each item takes precedence over the item below it:
//  1. command line flag
//  2. environment variable
//  3. configuration file
//  4. ConfigOption
//  5. default
//
// configuration keys used in config files are case-insensitive.
// The generated Config is maintained globally.
//
// example:
//
//	opts := []pinpoint.ConfigOption{
//	  pinpoint.WithAppName("GoTestApp"),
//	  pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
//	}
//	cfg, err := pinpoint.NewConfig(opts...)
func NewConfig(opts ...ConfigOption) (*Config, error) {
	config := defaultConfig()

	if opts != nil {
		for _, fn := range opts {
			fn(config)
		}
	}

	cmdEnvViper := viper.New()
	flagSet := config.newFlagSet()
	if err := flagSet.Parse(filterCmdArgs()); err != nil {
		Log("config").Errorf("commad line config loading error: %v", err)
	}
	cmdEnvViper.BindPFlags(flagSet)
	cmdEnvViper.SetEnvPrefix("pinpoint_go")
	cmdEnvViper.AutomaticEnv()

	cfgFileViper := config.loadConfigFile(cmdEnvViper)
	profileViper := config.loadProfile(cmdEnvViper, cfgFileViper)
	config.loadConfig(cmdEnvViper, cfgFileViper, profileViper)

	logger.setup(config)

	r, _ := regexp.Compile(cfgIdPattern)
	appName := config.String(CfgAppName)
	if appName == "" {
		return nil, errors.New("application name is required")
	} else if len(appName) > maxApplicationNameLength {
		return nil, errors.New("application name is too long (max length: " + fmt.Sprint(maxApplicationNameLength) + ")")
	} else if !r.MatchString(appName) {
		return nil, errors.New("application name has invalid pattern (" + cfgIdPattern + ")")
	}

	agentId := config.String(CfgAgentID)
	if agentId == "" || len(agentId) > maxAgentIdLength || !r.MatchString(agentId) {
		config.cfgMap[CfgAgentID].value = randomString(maxAgentIdLength - 1)
		Log("config").Infof("auto-generated AgentID: %v", config.cfgMap[CfgAgentID].value)
	}

	agentName := config.String(CfgAgentName)
	if agentName != "" {
		if len(agentName) > maxAgentNameLength {
			return nil, errors.New("agent name is too long (max length: " + fmt.Sprint(maxAgentNameLength) + ")")
		} else if !r.MatchString(agentName) {
			return nil, errors.New("agent name has invalid pattern (" + cfgIdPattern + ")")
		}
	}

	if config.containerCheck {
		config.cfgMap[CfgIsContainerEnv].value = isContainerEnv()
	}
	if config.Int(CfgSpanQueueSize) < 1 {
		config.cfgMap[CfgSpanQueueSize].value = defaultQueueSize
	}

	maxEventDepth = int32(config.Int(CfgSpanMaxCallStackDepth))
	if maxEventDepth == -1 {
		maxEventDepth = math.MaxInt32
	} else if maxEventDepth < minEventDepth {
		maxEventDepth = minEventDepth
	}

	maxEventSequence = int32(config.Int(CfgSpanMaxCallStackSequence))
	if maxEventSequence == -1 {
		maxEventSequence = math.MaxInt32
	} else if maxEventSequence < minEventSequence {
		maxEventSequence = minEventSequence
	}

	config.reloadCallback = make([]ConfigReloadCallback, 0)
	config.setDynamicConfig()

	return config, nil
}

func defaultConfig() *Config {
	config := new(Config)

	config.cfgMap = make(map[string]*cfgMapItem, 0)
	for k, v := range cfgBaseMap {
		config.cfgMap[k] = &cfgMapItem{
			defaultValue: v.defaultValue,
			valueType:    v.valueType,
			cmdKey:       v.cmdKey,
			envKey:       v.envKey,
		}
	}

	for _, v := range config.cfgMap {
		v.value = v.defaultValue
	}

	config.containerCheck = true
	return config
}

func (config *Config) newFlagSet() *pflag.FlagSet {
	flagSet := pflag.NewFlagSet("pinpoint_go_agent", pflag.ContinueOnError)

	for _, v := range config.cfgMap {
		switch v.valueType {
		case CfgInt:
			flagSet.Int(v.cmdKey, 0, "")
		case CfgFloat:
			flagSet.Float64(v.cmdKey, 0, "")
		case CfgBool:
			flagSet.Bool(v.cmdKey, false, "")
		case CfgString:
			flagSet.String(v.cmdKey, "", "")
		case CfgStringSlice:
			flagSet.StringSlice(v.cmdKey, nil, "")
		}
	}

	return flagSet
}

func filterCmdArgs() []string {
	cmdArgs := make([]string, 0)

	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--pinpoint-") {
			cmdArgs = append(cmdArgs, arg)
		}
	}
	return cmdArgs
}

func (config *Config) loadConfigFile(cmdEnvViper *viper.Viper) *viper.Viper {
	var cfgFile string

	item := config.cfgMap[CfgConfigFile]
	if cmdEnvViper.IsSet(item.cmdKey) {
		cfgFile = cmdEnvViper.GetString(item.cmdKey)
	} else if cmdEnvViper.IsSet(item.envKey) {
		cfgFile = cmdEnvViper.GetString(item.envKey)
	} else {
		cfgFile = item.value.(string)
	}

	cfgFileViper := viper.New()
	if cfgFile != "" {
		cfgFileViper.SetConfigFile(cfgFile)
		if err := cfgFileViper.ReadInConfig(); err != nil {
			Log("config").Errorf("config file loading error: %v", err)
		}

		cfgFileViper.OnConfigChange(func(e fsnotify.Event) {
			config.reloadConfig(cfgFileViper)
		})
		cfgFileViper.WatchConfig()
	}

	return cfgFileViper
}

func (config *Config) loadProfile(cmdEnvViper *viper.Viper, cfgFileViper *viper.Viper) *viper.Viper {
	var profile string

	item := config.cfgMap[CfgActiveProfile]
	if cmdEnvViper.IsSet(item.cmdKey) {
		profile = cmdEnvViper.GetString(item.cmdKey)
	} else if cmdEnvViper.IsSet(item.envKey) {
		profile = cmdEnvViper.GetString(item.envKey)
	} else if cfgFileViper.IsSet(CfgActiveProfile) {
		profile = cfgFileViper.GetString(CfgActiveProfile)
	} else {
		profile = item.value.(string)
	}

	if profile != "" {
		profileViper := cfgFileViper.Sub("profile." + profile)
		if profileViper != nil {
			return profileViper
		} else {
			Log("config").Warnf("config file doesn't have the profile: %s", profile)
		}
	}

	return viper.New()
}

func (config *Config) loadConfig(cmdEnvViper *viper.Viper, cfgFileViper *viper.Viper, profileViper *viper.Viper) {
	sortKeys := make([]string, 0)
	for k := range config.cfgMap {
		sortKeys = append(sortKeys, k)
	}
	sort.Strings(sortKeys)
	for _, k := range sortKeys {
		v := config.cfgMap[k]
		if cmdEnvViper.IsSet(v.cmdKey) {
			config.setFinalValue(k, v, cmdEnvViper.Get(v.cmdKey))
		} else if cmdEnvViper.IsSet(v.envKey) {
			config.setFinalValue(k, v, cmdEnvViper.Get(v.envKey))
		} else if profileViper.IsSet(k) {
			config.setFinalValue(k, v, profileViper.Get(k))
		} else if cfgFileViper.IsSet(k) {
			config.setFinalValue(k, v, cfgFileViper.Get(k))
		}
	}
}

func (config *Config) setFinalValue(cfgName string, item *cfgMapItem, value interface{}) {
	if item.valueType == CfgStringSlice {
		if s, ok := value.(string); ok {
			value = strings.Split(s, ",")
		}
	}

	item.value = value
	if cfgName == CfgIsContainerEnv {
		config.containerCheck = false
	} else if cfgName == CfgLogLevel {
		config.useNewLogOpt = true
	} else if cfgName == CfgLogLevelOld && !config.useNewLogOpt {
		config.cfgMap[CfgLogLevel].value = value
	}
}

// AddReloadCallback adds a callback function will be called after reloading config file.
func (config *Config) AddReloadCallback(callback ConfigReloadCallback) {
	config.reloadCallback = append(config.reloadCallback, callback)
}

func (config *Config) reloadConfig(cfgFileViper *viper.Viper) {
	if err := cfgFileViper.ReadInConfig(); err != nil {
		Log("config").Errorf("config file reloading error: %v", err)
		return
	}

	profileViper := config.loadProfile(viper.New(), cfgFileViper)
	config.loadDynamicConfig(cfgFileViper, profileViper)
	config.setDynamicConfig()

	for _, callback := range config.reloadCallback {
		callback(config)
	}
}

func (config *Config) setDynamicConfig() {
	sampleType := strings.ToUpper(strings.TrimSpace(config.String(CfgSamplingType)))
	if sampleType != samplingTypeCounter && sampleType != samplingTypePercent {
		config.cfgMap[CfgSamplingType].value = samplingTypeCounter
		config.cfgMap[CfgSamplingCounterRate].value = 0
	}

	maxBindSize := config.Int(CfgSQLMaxBindValueSize)
	if maxBindSize > 1024 {
		config.cfgMap[CfgSQLMaxBindValueSize].value = 1024
	} else if maxBindSize < 0 {
		config.cfgMap[CfgSQLTraceBindValue].value = false
		config.cfgMap[CfgSQLMaxBindValueSize].value = 0
	}
	config.sqlTraceBindValue = config.Bool(CfgSQLTraceBindValue)
	config.sqlMaxBindValueSize = config.Int(CfgSQLMaxBindValueSize)
	config.sqlTraceCommit = config.Bool(CfgSQLTraceCommit)
	config.sqlTraceRollback = config.Bool(CfgSQLTraceRollback)

	config.collectUrlStat = config.Bool(CfgHttpUrlStatEnable)
}

func (config *Config) loadDynamicConfig(cfgFileViper *viper.Viper, profileViper *viper.Viper) {
	sortKeys := make([]string, 0)
	for k := range config.cfgMap {
		sortKeys = append(sortKeys, k)
	}
	sort.Strings(sortKeys)
	for _, k := range sortKeys {
		if v := config.cfgMap[k]; v.dynamic {
			if profileViper.IsSet(k) {
				config.reloadFinalValue(k, v, profileViper)
			} else if cfgFileViper.IsSet(k) {
				config.reloadFinalValue(k, v, cfgFileViper)
			}
		}
	}
}

func (config *Config) reloadFinalValue(cfgName string, item *cfgMapItem, viper *viper.Viper) {
	item.oldValue = item.value
	config.setFinalValue(cfgName, item, viper.Get(cfgName))
}

func (config *Config) isChanged(cfgName string) bool {
	item := config.cfgMap[cfgName]
	return item.oldValue != item.value
}

func isContainerEnv() bool {
	_, err := os.Stat("/.dockerenv")
	if err == nil || !os.IsNotExist(err) {
		return true
	}

	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}

	return false
}

// WithAppName sets the application name.
func WithAppName(name string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgAppName].value = name
	}
}

// WithAppType sets the application type.
func WithAppType(typ int32) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgAppType].value = typ
	}
}

// WithAgentId sets the agent ID.
func WithAgentId(id string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgAgentID].value = id
	}
}

// WithAgentName sets the agent name.
func WithAgentName(name string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgAgentName].value = name
	}
}

// WithConfigFile sets the configuration file.
func WithConfigFile(filePath string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgConfigFile].value = filePath
	}
}

// WithCollectorHost sets the host address of pinpoint collector.
func WithCollectorHost(host string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorHost].value = host
	}
}

// WithCollectorAgentPort sets the agent port of pinpoint collector.
func WithCollectorAgentPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorAgentPort].value = port
	}
}

// WithCollectorSpanPort sets the span port of pinpoint collector.
func WithCollectorSpanPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorSpanPort].value = port
	}
}

// WithCollectorStatPort sets the agent stat of pinpoint collector.
func WithCollectorStatPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgCollectorStatPort].value = port
	}
}

// WithLogLevel sets the logging level for agent logger.
func WithLogLevel(level string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgLogLevel].value = level
	}
}

// WithLogOutput sets the output for agent logger.
func WithLogOutput(output string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgLogOutput].value = output
	}
}

// WithLogMaxSize sets the max size of output file for agent logger.
func WithLogMaxSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgLogMaxSize].value = size
	}
}

// WithSamplingType sets the type of agent sampler.
// Either "COUNTER" or "PERCENT" must be specified.
func WithSamplingType(samplingType string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingType].value = samplingType
	}
}

// WithSamplingRate DEPRECATED: Use WithSamplingCounterRate()
func WithSamplingRate(rate int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingCounterRate].value = rate
	}
}

// WithSamplingCounterRate sets the sampling rate for a 'counter sampler'.
func WithSamplingCounterRate(rate int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingCounterRate].value = rate
	}
}

// WithSamplingPercentRate sets the sampling rate for a 'percent sampler'.
func WithSamplingPercentRate(rate float32) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingPercentRate].value = rate
	}
}

// WithSamplingNewThroughput sets the new tps for a 'throughput sampler'.
func WithSamplingNewThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingNewThroughput].value = tps
	}
}

// WithSamplingContinueThroughput sets the cont tps for a 'throughput sampler'.
func WithSamplingContinueThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSamplingContinueThroughput].value = tps
	}
}

// WithStatCollectInterval sets the statistics collection cycle for the agent.
func WithStatCollectInterval(interval int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgStatCollectInterval].value = interval
	}
}

// WithStatBatchCount sets batch delivery units for collected statistics.
func WithStatBatchCount(count int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgStatBatchCount].value = count
	}
}

// WithIsContainerEnv sets whether the application is running in a container environment or not.
// If this is not set, the agent automatically checks it.
func WithIsContainerEnv(isContainer bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgIsContainerEnv].value = isContainer
		c.containerCheck = false
	}
}

// WithActiveProfile sets the configuration profile.
func WithActiveProfile(profile string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgActiveProfile].value = profile
	}
}

// WithSQLTraceBindValue enables bind value tracing for SQL Driver.
func WithSQLTraceBindValue(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLTraceBindValue].value = trace
	}
}

// WithSQLMaxBindValueSize sets the max length of traced bind value for SQL Driver.
func WithSQLMaxBindValueSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLMaxBindValueSize].value = size
	}
}

// WithSQLTraceCommit enables commit tracing for SQL Driver.
func WithSQLTraceCommit(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLTraceCommit].value = trace
	}
}

// WithSQLTraceRollback enables rollback tracing for SQL Driver.
func WithSQLTraceRollback(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSQLTraceRollback].value = trace
	}
}

// WithEnable enables the agent is operational state.
func WithEnable(enable bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgEnable].value = enable
	}
}

// WithSpanQueueSize sets the size of the span queue for gRPC.
func WithSpanQueueSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanQueueSize].value = size
	}
}

// WithSpanMaxCallStackDepth sets the max callstack depth of a span.
func WithSpanMaxCallStackDepth(depth int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanMaxCallStackDepth].value = depth
	}
}

// WithSpanMaxCallStackSequence sets the max callstack sequence of a span.
func WithSpanMaxCallStackSequence(seq int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgSpanMaxCallStackSequence].value = seq
	}
}

// WithHttpUrlStatEnable enables the agent collects the HTTP URL statistics.
func WithHttpUrlStatEnable(enable bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgHttpUrlStatEnable].value = enable
	}
}

// WithHttpUrlStatLimitSize sets the maximum number of URLs that can be stored in one snapshot.
func WithHttpUrlStatLimitSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[CfgHttpUrlStatLimitSize].value = size
	}
}

func (config *Config) printConfigString() {
	sortKeys := make([]string, 0)
	for k := range config.cfgMap {
		sortKeys = append(sortKeys, k)
	}
	sort.Strings(sortKeys)

	for _, k := range sortKeys {
		Log("config").Infof("%s = %v", k, config.cfgMap[k].value)
	}
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomString(n int) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	l := len(charset)

	b := make([]byte, n)
	for i := range b {
		b[i] = charset[r.Intn(l)]
	}
	return string(b)
}
