package pinpoint

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cast"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	cfgAppName                    = "ApplicationName"
	cfgAppType                    = "ApplicationType"
	cfgAgentID                    = "AgentID"
	cfgAgentName                  = "AgentName"
	cfgCollectorHost              = "Collector.Host"
	cfgCollectorAgentPort         = "Collector.AgentPort"
	cfgCollectorSpanPort          = "Collector.SpanPort"
	cfgCollectorStatPort          = "Collector.StatPort"
	cfgLogLevel                   = "LogLevel"
	cfgSamplingType               = "Sampling.Type"
	cfgSamplingCounterRate        = "Sampling.CounterRate"
	cfgSamplingPercentRate        = "Sampling.PercentRate"
	cfgSamplingNewThroughput      = "Sampling.NewThroughput"
	cfgSamplingContinueThroughput = "Sampling.ContinueThroughput"
	cfgStatCollectInterval        = "Stat.CollectInterval"
	cfgStatBatchCount             = "Stat.BatchCount"
	cfgIsContainerEnv             = "IsContainerEnv"
	cfgConfigFile                 = "ConfigFile"
	cfgUseProfile                 = "UseProfile"
	cfgSQLTraceBindValue          = "SQL.TraceBindValue"
	cfgSQLMaxBindValueSize        = "SQL.MaxBindValueSize"
	cfgSQLTraceCommit             = "SQL.TraceCommit"
	cfgSQLTraceRollback           = "SQL.TraceRollback"
	cfgIdPattern                  = "[a-zA-Z0-9\\._\\-]+"

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
}

var (
	cfgBaseMap   map[string]*cfgMapItem
	globalConfig *Config
)

func initConfig() {
	cfgBaseMap = make(map[string]*cfgMapItem, 0)

	AddConfig(cfgAppName, CfgString, "")
	AddConfig(cfgAppType, CfgInt, ServiceTypeGoApp)
	AddConfig(cfgAgentID, CfgString, "")
	AddConfig(cfgAgentName, CfgString, "")
	AddConfig(cfgCollectorHost, CfgString, "localhost")
	AddConfig(cfgCollectorAgentPort, CfgInt, 9991)
	AddConfig(cfgCollectorSpanPort, CfgInt, 9993)
	AddConfig(cfgCollectorStatPort, CfgInt, 9992)
	AddConfig(cfgLogLevel, CfgString, "info")
	AddConfig(cfgSamplingType, CfgString, samplingTypeCounter)
	AddConfig(cfgSamplingCounterRate, CfgInt, 1)
	AddConfig(cfgSamplingPercentRate, CfgFloat, 100)
	AddConfig(cfgSamplingNewThroughput, CfgInt, 0)
	AddConfig(cfgSamplingContinueThroughput, CfgInt, 0)
	AddConfig(cfgStatCollectInterval, CfgInt, 5000)
	AddConfig(cfgStatBatchCount, CfgInt, 6)
	AddConfig(cfgIsContainerEnv, CfgBool, false)
	AddConfig(cfgConfigFile, CfgString, "")
	AddConfig(cfgUseProfile, CfgString, "")
	AddConfig(cfgSQLTraceBindValue, CfgBool, true)
	AddConfig(cfgSQLMaxBindValueSize, CfgInt, 1024)
	AddConfig(cfgSQLTraceCommit, CfgBool, true)
	AddConfig(cfgSQLTraceRollback, CfgBool, true)
}

// AddConfig adds a configuration item.
func AddConfig(cfgName string, valueType int, defaultValue interface{}) {
	cfgBaseMap[cfgName] = &cfgMapItem{
		defaultValue: defaultValue,
		valueType:    valueType,
		cmdKey:       cmdName(cfgName),
		envKey:       envName(cfgName),
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
	offGrpc        bool //for test
}

// ConfigOption represents an option that can be passed to NewConfig.
type ConfigOption func(*Config)

// GetConfig returns a global Config created by NewConfig.
func GetConfig() *Config {
	return globalConfig
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

	logLevel := parseLogLevel(config.String(cfgLogLevel))
	logger.SetLevel(logLevel)
	if logLevel > logrus.InfoLevel {
		logger.SetReportCaller(true)
	}

	r, _ := regexp.Compile(cfgIdPattern)
	appName := config.String(cfgAppName)
	if appName == "" {
		return nil, errors.New("application name is required")
	} else if len(appName) > maxApplicationNameLength {
		return nil, errors.New("application name is too long (max length: " + fmt.Sprint(maxApplicationNameLength) + ")")
	} else if !r.MatchString(appName) {
		return nil, errors.New("application name has invalid pattern (" + cfgIdPattern + ")")
	}

	agentId := config.String(cfgAgentID)
	if agentId == "" || len(agentId) > maxAgentIdLength || !r.MatchString(agentId) {
		config.cfgMap[cfgAgentID].value = randomString(maxAgentIdLength - 1)
		Log("config").Infof("auto-generated AgentID: %v", config.cfgMap[cfgAgentID].value)
	}

	agentName := config.String(cfgAgentName)
	if agentName != "" {
		if len(agentName) > maxAgentNameLength {
			return nil, errors.New("agent name is too long (max length: " + fmt.Sprint(maxAgentNameLength) + ")")
		} else if !r.MatchString(agentName) {
			return nil, errors.New("agent name has invalid pattern (" + cfgIdPattern + ")")
		}
	}

	sampleType := strings.ToUpper(strings.TrimSpace(config.String(cfgSamplingType)))
	if sampleType != samplingTypeCounter && sampleType != samplingTypePercent {
		config.cfgMap[cfgSamplingType].value = samplingTypeCounter
		config.cfgMap[cfgSamplingCounterRate].value = 0
	}

	if config.containerCheck {
		config.cfgMap[cfgIsContainerEnv].value = isContainerEnv()
	}

	maxBindSize := config.Int(cfgSQLMaxBindValueSize)
	if maxBindSize > 1024 {
		config.cfgMap[cfgSQLMaxBindValueSize].value = 1024
	} else if maxBindSize < 0 {
		config.cfgMap[cfgSQLTraceBindValue].value = false
		config.cfgMap[cfgSQLMaxBindValueSize].value = 0
	}

	globalConfig = config
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

	item := config.cfgMap[cfgConfigFile]
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
	}

	return cfgFileViper
}

func (config *Config) loadProfile(cmdEnvViper *viper.Viper, cfgFileViper *viper.Viper) *viper.Viper {
	var profile string

	item := config.cfgMap[cfgUseProfile]
	if cmdEnvViper.IsSet(item.cmdKey) {
		profile = cmdEnvViper.GetString(item.cmdKey)
	} else if cmdEnvViper.IsSet(item.envKey) {
		profile = cmdEnvViper.GetString(item.envKey)
	} else if cfgFileViper.IsSet(cfgUseProfile) {
		profile = cfgFileViper.GetString(cfgUseProfile)
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
	for k, v := range config.cfgMap {
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
	if cfgName == cfgIsContainerEnv {
		config.containerCheck = false
	}
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

func parseLogLevel(level string) logrus.Level {
	lvl, e := logrus.ParseLevel(level)
	if e != nil {
		Log("config").Errorf("invalid Log level: %v", e)
		lvl = logrus.InfoLevel
	}
	return lvl
}

// WithAppName sets the application name.
func WithAppName(name string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgAppName].value = name
	}
}

// WithAppType sets the application type.
func WithAppType(typ int32) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgAppType].value = typ
	}
}

// WithAgentId sets the agent ID.
func WithAgentId(id string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgAgentID].value = id
	}
}

// WithAgentName sets the agent name.
func WithAgentName(name string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgAgentName].value = name
	}
}

// WithConfigFile sets the configuration file.
func WithConfigFile(filePath string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgConfigFile].value = filePath
	}
}

// WithCollectorHost sets the host address of pinpoint collector.
func WithCollectorHost(host string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgCollectorHost].value = host
	}
}

// WithCollectorAgentPort sets the agent port of pinpoint collector.
func WithCollectorAgentPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgCollectorAgentPort].value = port
	}
}

// WithCollectorSpanPort sets the span port of pinpoint collector.
func WithCollectorSpanPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgCollectorSpanPort].value = port
	}
}

// WithCollectorStatPort sets the agent stat of pinpoint collector.
func WithCollectorStatPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgCollectorStatPort].value = port
	}
}

// WithLogLevel sets the logging level for agent logger.
func WithLogLevel(level string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgLogLevel].value = level
	}
}

// WithSamplingType sets the type of agent sampler.
// Either "COUNTER" or "PERCENT" must be specified.
func WithSamplingType(samplingType string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingType].value = samplingType
	}
}

// WithSamplingRate DEPRECATED: Use WithSamplingCounterRate()
func WithSamplingRate(rate int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingCounterRate].value = rate
	}
}

// WithSamplingCounterRate sets the sampling rate for a 'counter sampler'.
func WithSamplingCounterRate(rate int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingCounterRate].value = rate
	}
}

// WithSamplingPercentRate sets the sampling rate for a 'percent sampler'.
func WithSamplingPercentRate(rate float32) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingPercentRate].value = rate
	}
}

// WithSamplingNewThroughput sets the new tps for a 'throughput sampler'.
func WithSamplingNewThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingNewThroughput].value = tps
	}
}

// WithSamplingContinueThroughput sets the cont tps for a 'throughput sampler'.
func WithSamplingContinueThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingContinueThroughput].value = tps
	}
}

// WithStatCollectInterval sets the statistics collection cycle for the agent.
func WithStatCollectInterval(interval int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgStatCollectInterval].value = interval
	}
}

// WithStatBatchCount sets batch delivery units for collected statistics.
func WithStatBatchCount(count int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgStatBatchCount].value = count
	}
}

// WithIsContainerEnv sets whether the application is running in a container environment or not.
// If this is not set, the agent automatically checks it.
func WithIsContainerEnv(isContainer bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgIsContainerEnv].value = isContainer
		c.containerCheck = false
	}
}

// WithUseProfile sets the configuration profile.
func WithUseProfile(profile string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgUseProfile].value = profile
	}
}

// WithSQLTraceBindValue enables bind value tracing for SQL Driver.
func WithSQLTraceBindValue(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSQLTraceBindValue].value = trace
	}
}

// WithSQLMaxBindValueSize sets the max length of traced bind value for SQL Driver.
func WithSQLMaxBindValueSize(size int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSQLMaxBindValueSize].value = size
	}
}

// WithSQLTraceCommit enables commit tracing for SQL Driver.
func WithSQLTraceCommit(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSQLTraceCommit].value = trace
	}
}

// WithSQLTraceRollback enables rollback tracing for SQL Driver.
func WithSQLTraceRollback(trace bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSQLTraceRollback].value = trace
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
