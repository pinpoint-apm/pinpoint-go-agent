package pinpoint

import (
	"errors"
	"github.com/spf13/cast"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"io/ioutil"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
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
	cfgProfile                    = "Profile"
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
	valueType    int
	defaultValue interface{}
	cmdKey       string
	envKey       string
}

var cfgMap map[string]*cfgMapItem
var flagSet *pflag.FlagSet
var globalConfig *Config

func init() {
	cfgMap = make(map[string]*cfgMapItem, 25)
	flagSet = pflag.NewFlagSet(os.Args[0], pflag.ContinueOnError)

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
	AddConfig(cfgStatBatchCount, CfgInt, 5)
	AddConfig(cfgRunOnContainer, CfgBool, false)
	AddConfig(cfgProfile, CfgString, "")

	flagSet.Parse(os.Args[1:])
}

func AddConfig(cfgName string, valueType int, defaultValue interface{}) {
	cfgMap[cfgName] = &cfgMapItem{
		valueType:    valueType,
		defaultValue: defaultValue,
		cmdKey:       cmdName(cfgName),
		envKey:       envName(cfgName),
	}

	switch valueType {
	case CfgInt:
		flagSet.Int(cmdName(cfgName), 0, "")
	case CfgFloat:
		flagSet.Float64(cmdName(cfgName), 0, "")
	case CfgBool:
		flagSet.Bool(cmdName(cfgName), false, "")
	case CfgString:
		flagSet.String(cmdName(cfgName), "", "")
	case CfgStringSlice:
		flagSet.StringSlice(cmdName(cfgName), nil, "")
	}
}

func cmdName(cfgName string) string {
	return "pinpoint-" + strings.ReplaceAll(strings.ToLower(cfgName), ".", "-")
}

func envName(cfgName string) string {
	return strings.ReplaceAll(strings.ToLower(cfgName), ".", "_")
}

type Config struct {
	cfgFilePath    string
	logLevel       logrus.Level
	containerCheck bool
}

type ConfigOption func(*Config)

func GetConfig() *Config {
	return globalConfig
}

func (config *Config) Set(cfgName string, value interface{}) {
	cfgMap[cfgName].value = value
}
func (config *Config) String(cfgName string) string {
	return cast.ToString(cfgMap[cfgName].value)
}

func (config *Config) Int(cfgName string) int {
	return cast.ToInt(cfgMap[cfgName].value)
}

func (config *Config) Float(cfgName string) float64 {
	return cast.ToFloat64(cfgMap[cfgName].value)
}

func (config *Config) StringSlice(cfgName string) []string {
	return cast.ToStringSlice(cfgMap[cfgName].value)
}

func (config *Config) Bool(cfgName string) bool {
	return cast.ToBool(cfgMap[cfgName].value)
}

func NewConfig(opts ...ConfigOption) (*Config, error) {
	config := defaultConfig()

	for _, fn := range opts {
		fn(config)
	}

	v1 := viper.New()
	v1.BindPFlags(flagSet)
	v1.SetEnvPrefix("pinpoint_go")
	v1.AutomaticEnv()

	v2 := viper.New()
	if config.cfgFilePath != "" {
		v2.SetConfigFile(config.cfgFilePath)
		v2.ReadInConfig()
		//v3 := initProfile(v1)
	}

	loadConfig(v1, v2)

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
		cfgMap[cfgAgentID].value = randomString(MaxAgentIdLength - 1)
		Log("config").Infof("agentId is automatically generated: %v", cfgMap[cfgAgentID].value)
	}

	sampleType := config.String(cfgSamplingType)
	sampleType = strings.ToUpper(strings.TrimSpace(sampleType))
	if sampleType == SamplingTypeCounter {
		rate := config.Int(cfgSamplingCounterRate)
		if rate < 0 {
			cfgMap[cfgSamplingCounterRate].value = 0
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
		cfgMap[cfgSamplingPercentRate].value = rate
	} else {
		cfgMap[cfgSamplingType].value = SamplingTypeCounter
		cfgMap[cfgSamplingCounterRate].value = 1
	}

	if config.containerCheck {
		cfgMap[cfgRunOnContainer].value = isContainerEnv()
	}

	config.logLevel = parseLogLevel(config.String(cfgLogLevel))
	config.printConfigString()

	globalConfig = config

	return config, nil
}

func defaultConfig() *Config {
	config := new(Config)

	config.logLevel = logrus.InfoLevel
	config.containerCheck = true

	for _, v := range cfgMap {
		v.value = v.defaultValue
	}

	return config
}

func loadConfig(cmdEnvViper *viper.Viper, cfgFileViper *viper.Viper) {
	for k, v := range cfgMap {
		if cmdEnvViper.IsSet(v.cmdKey) {
			v.value = cmdEnvViper.Get(v.cmdKey)
		} else if cmdEnvViper.IsSet(v.envKey) {
			v.value = cmdEnvViper.Get(v.envKey)
		} else if cfgFileViper.IsSet(k) {
			v.value = cfgFileViper.Get(k)
		}
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

func initProfile(v *viper.Viper) *viper.Viper {
	profile := v.GetString(cmdName(cfgProfile))
	return v.Sub("profile." + profile)
}

func WithAppName(name string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgAppName].value = name
	}
}

func WithAppType(typ int32) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgAppType].value = typ
	}
}

func WithAgentId(id string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgAgentID].value = id
	}
}

func WithConfigFile(filePath string) ConfigOption {
	return func(c *Config) {
		c.cfgFilePath = filePath
	}
}

func WithCollectorHost(host string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgCollectorHost].value = host
	}
}

func WithCollectorAgentPort(port int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgCollectorAgentPort].value = port
	}
}

func WithCollectorSpanPort(port int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgCollectorSpanPort].value = port
	}
}

func WithCollectorStatPort(port int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgCollectorStatPort].value = port
	}
}

func WithLogLevel(level string) ConfigOption {
	return func(c *Config) {
		c.logLevel = parseLogLevel(level)
	}
}

func WithSamplingType(samplingType string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgSamplingType].value = samplingType
	}
}

// WithSamplingRate DEPRECATED: Use WithSamplingCounterRate()
func WithSamplingRate(rate int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgSamplingCounterRate].value = rate
	}
}

func WithSamplingCounterRate(rate int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgSamplingCounterRate].value = rate
	}
}

func WithSamplingPercentRate(rate float32) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgSamplingPercentRate].value = rate
	}
}

func WithSamplingNewThroughput(tps int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgSamplingNewThroughput].value = tps
	}
}

func WithSamplingContinueThroughput(tps int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgSamplingContinueThroughput].value = tps
	}
}

func WithStatCollectInterval(interval int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgStatCollectInterval].value = interval
	}
}

func WithStatBatchCount(count int) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgStatBatchCount].value = count
	}
}

func WithIsContainer(isContainer bool) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgRunOnContainer].value = isContainer
		c.containerCheck = false
	}
}

func (config *Config) printConfigString() {
	if config.cfgFilePath != "" {
		dat, err := ioutil.ReadFile(config.cfgFilePath)
		if err == nil {
			Log("agent").Info("config_yaml_file= ", config.cfgFilePath, "\n", string(dat))
		}
	}

	for k, v := range cfgMap {
		Log("agent").Infof("config: %s = %v", k, v.value)
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
