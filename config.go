package pinpoint

import (
	"errors"
	"math/rand"
	"os"
	"regexp"
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
	cfgRunOnContainer             = "RunOnContainer"
	cfgConfigFile                 = "ConfigFile"
	cfgUseProfile                 = "UseProfile"
	cfgIdPattern                  = "[a-zA-Z0-9\\._\\-]+"
)

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
	AddConfig(cfgCollectorHost, CfgString, "localhost")
	AddConfig(cfgCollectorAgentPort, CfgInt, 9991)
	AddConfig(cfgCollectorSpanPort, CfgInt, 9993)
	AddConfig(cfgCollectorStatPort, CfgInt, 9992)
	AddConfig(cfgLogLevel, CfgString, "info")
	AddConfig(cfgSamplingType, CfgString, SamplingTypeCounter)
	AddConfig(cfgSamplingCounterRate, CfgInt, 1)
	AddConfig(cfgSamplingPercentRate, CfgFloat, 100)
	AddConfig(cfgSamplingNewThroughput, CfgInt, 0)
	AddConfig(cfgSamplingContinueThroughput, CfgInt, 0)
	AddConfig(cfgStatCollectInterval, CfgInt, 5000)
	AddConfig(cfgStatBatchCount, CfgInt, 6)
	AddConfig(cfgRunOnContainer, CfgBool, false)
	AddConfig(cfgConfigFile, CfgString, "")
	AddConfig(cfgUseProfile, CfgString, "")
}

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

type Config struct {
	cfgMap         map[string]*cfgMapItem
	containerCheck bool
}

type ConfigOption func(*Config)

func GetConfig() *Config {
	return globalConfig
}

func (config *Config) Set(cfgName string, value interface{}) {
	if v, ok := config.cfgMap[cfgName]; ok {
		v.value = value
	}
}

func (config *Config) Int(cfgName string) int {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToInt(v.value)
	}
	return 0
}

func (config *Config) Float(cfgName string) float64 {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToFloat64(v.value)
	}
	return 0
}

func (config *Config) String(cfgName string) string {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToString(v.value)
	}
	return ""
}

func (config *Config) StringSlice(cfgName string) []string {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToStringSlice(v.value)
	}
	return []string{}
}

func (config *Config) Bool(cfgName string) bool {
	if v, ok := config.cfgMap[cfgName]; ok {
		return cast.ToBool(v.value)
	}
	return false
}

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
	} else if len(appName) > MaxApplicationNameLength {
		return nil, errors.New("application name is too long (max length: 24)")
	} else if !r.MatchString(appName) {
		return nil, errors.New("application name has invalid pattern (" + cfgIdPattern + ")")
	}

	agentId := config.String(cfgAgentID)
	if agentId == "" || len(agentId) > MaxAgentIdLength || !r.MatchString(agentId) {
		config.cfgMap[cfgAgentID].value = randomString(MaxAgentIdLength - 1)
		Log("config").Infof("agentId is automatically generated: %v", config.cfgMap[cfgAgentID].value)
	}

	sampleType := config.String(cfgSamplingType)
	sampleType = strings.ToUpper(strings.TrimSpace(sampleType))
	if sampleType == SamplingTypeCounter {
		rate := config.Int(cfgSamplingCounterRate)
		if rate < 0 {
			config.cfgMap[cfgSamplingCounterRate].value = 0
		}
	} else if sampleType == SamplingTypePercent {
		rate := config.Float(cfgSamplingPercentRate)
		if rate < 0 {
			rate = 0
		} else if rate < 0.01 {
			rate = 0.01
		} else if rate > 100 {
			rate = 100
		}
		config.cfgMap[cfgSamplingPercentRate].value = rate
	} else {
		config.cfgMap[cfgSamplingType].value = SamplingTypeCounter
		config.cfgMap[cfgSamplingCounterRate].value = 1
	}

	if config.containerCheck {
		config.cfgMap[cfgRunOnContainer].value = isContainerEnv()
	}

	config.printConfigString()
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
	item.value = value
	if cfgName == cfgRunOnContainer {
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

func WithAppName(name string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgAppName].value = name
	}
}

func WithAppType(typ int32) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgAppType].value = typ
	}
}

func WithAgentId(id string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgAgentID].value = id
	}
}

func WithConfigFile(filePath string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgConfigFile].value = filePath
	}
}

func WithCollectorHost(host string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgCollectorHost].value = host
	}
}

func WithCollectorAgentPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgCollectorAgentPort].value = port
	}
}

func WithCollectorSpanPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgCollectorSpanPort].value = port
	}
}

func WithCollectorStatPort(port int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgCollectorStatPort].value = port
	}
}

func WithLogLevel(level string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgLogLevel].value = level
	}
}

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

func WithSamplingCounterRate(rate int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingCounterRate].value = rate
	}
}

func WithSamplingPercentRate(rate float32) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingPercentRate].value = rate
	}
}

func WithSamplingNewThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingNewThroughput].value = tps
	}
}

func WithSamplingContinueThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgSamplingContinueThroughput].value = tps
	}
}

func WithStatCollectInterval(interval int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgStatCollectInterval].value = interval
	}
}

func WithStatBatchCount(count int) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgStatBatchCount].value = count
	}
}

func WithIsContainer(isContainer bool) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgRunOnContainer].value = isContainer
		c.containerCheck = false
	}
}

func WithUseProfile(profile string) ConfigOption {
	return func(c *Config) {
		c.cfgMap[cfgUseProfile].value = profile
	}
}

func (config *Config) printConfigString() {
	for k, v := range config.cfgMap {
		Log("config").Infof("%s = %v", k, v.value)
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
