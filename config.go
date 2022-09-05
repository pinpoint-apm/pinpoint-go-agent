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
	cfgHttpStatusCodeErrors       = "Http.StatusCodeErrors"
	cfgHttpExcludeUrl             = "Http.ExcludeUrl"
	cfgHttpExcludeMethod          = "Http.ExcludeMethod"
	cfgHttpRecordRequestHeader    = "Http.RecordRequestHeader"
	cfgHttpRecordRespondHeader    = "Http.RecordRespondHeader"
	cfgHttpRecordRequestCookie    = "Http.RecordRequestCookie"
	cfgRunOnContainer             = "RunOnContainer"
	cfgProfile                    = "Profile"
)

type configMapItem struct {
	value        interface{}
	cmdKey       string
	envKey       string
	defaultValue interface{}
}

type configMap map[string]*configMapItem

var cfgMap configMap

func initConfigMapItem(key string, v interface{}) {
	cfgMap[key] = &configMapItem{cmdKey: cmdName(key), envKey: envName(key), defaultValue: v}
}

func init() {
	cfgMap = make(map[string]*configMapItem, 25)

	initConfigMapItem(cfgAppName, "")
	initConfigMapItem(cfgAppType, ServiceTypeGoApp)
	initConfigMapItem(cfgAgentID, "")
	initConfigMapItem(cfgCollectorHost, "localhost")
	initConfigMapItem(cfgCollectorAgentPort, 9991)
	initConfigMapItem(cfgCollectorSpanPort, 9993)
	initConfigMapItem(cfgCollectorStatPort, 9992)
	initConfigMapItem(cfgLogLevel, "info")
	initConfigMapItem(cfgSamplingType, SamplingTypeCounter)
	initConfigMapItem(cfgSamplingCounterRate, 1)
	initConfigMapItem(cfgSamplingPercentRate, 100)
	initConfigMapItem(cfgSamplingNewThroughput, 0)
	initConfigMapItem(cfgSamplingContinueThroughput, 0)
	initConfigMapItem(cfgStatCollectInterval, 5000)
	initConfigMapItem(cfgStatBatchCount, 5)
	initConfigMapItem(cfgHttpStatusCodeErrors, []string{"5xx"})
	initConfigMapItem(cfgHttpExcludeUrl, []string{})
	initConfigMapItem(cfgHttpExcludeMethod, []string{})
	initConfigMapItem(cfgHttpRecordRequestHeader, []string{})
	initConfigMapItem(cfgHttpRecordRespondHeader, []string{})
	initConfigMapItem(cfgHttpRecordRequestCookie, []string{})
	initConfigMapItem(cfgRunOnContainer, false)
	initConfigMapItem(cfgProfile, "")

	pflag.String(cmdName(cfgAppName), "", "")
	pflag.Int(cmdName(cfgAppType), -1, "")
	pflag.String(cmdName(cfgAgentID), "", "")
	pflag.String(cmdName(cfgCollectorHost), "", "")
	pflag.Int(cmdName(cfgCollectorAgentPort), -1, "")
	pflag.Int(cmdName(cfgCollectorSpanPort), -1, "")
	pflag.Int(cmdName(cfgCollectorStatPort), -1, "")
	pflag.Int(cmdName(cfgSamplingType), -1, "")
	pflag.Int(cmdName(cfgSamplingCounterRate), -1, "")
	pflag.Float32(cmdName(cfgSamplingPercentRate), -1, "")
	pflag.Int(cmdName(cfgSamplingNewThroughput), -1, "")
	pflag.Int(cmdName(cfgSamplingContinueThroughput), -1, "")
	pflag.Int(cmdName(cfgStatCollectInterval), -1, "")
	pflag.Int(cmdName(cfgStatBatchCount), -1, "")
	pflag.StringSlice(cmdName(cfgHttpStatusCodeErrors), nil, "")
	pflag.StringSlice(cmdName(cfgHttpExcludeUrl), nil, "")
	pflag.StringSlice(cmdName(cfgHttpExcludeMethod), nil, "")
	pflag.StringSlice(cmdName(cfgHttpRecordRequestHeader), nil, "")
	pflag.StringSlice(cmdName(cfgHttpRecordRespondHeader), nil, "")
	pflag.StringSlice(cmdName(cfgHttpRecordRequestCookie), nil, "")
	pflag.String(cmdName(cfgLogLevel), "", "")
	pflag.Bool(cmdName(cfgRunOnContainer), false, "")
	pflag.String(cmdName(cfgProfile), "", "")
	pflag.Parse()

}

type Config struct {
	cfgFilePath string
	logLevel    logrus.Level

	needContainerCheck bool
	offGrpc            bool //for test
}

type ConfigOption func(*Config)

const idPattern = "[a-zA-Z0-9\\._\\-]+"

func NewConfig(opts ...ConfigOption) (*Config, error) {
	config := defaultConfig()

	for _, fn := range opts {
		fn(config)
	}

	v1 := viper.New()
	initCmdLineConfig(v1)
	initEnvConfig(v1)

	v2 := viper.New()
	if config.cfgFilePath != "" {
		v2.SetConfigFile(config.cfgFilePath)
		v2.ReadInConfig()
		//v3 := initProfile(v1)
	}

	loadConfig(v1, v2)

	r, _ := regexp.Compile(idPattern)
	appName := ConfigString(cfgAppName)
	if appName == "" {
		return nil, errors.New("application name is required")
	} else if len(appName) > MaxApplicationNameLength {
		return nil, errors.New("application name is too long (max length: 24)")
	} else if !r.MatchString(appName) {
		return nil, errors.New("application name has invalid pattern (" + idPattern + ")")
	}

	agentId := ConfigString(cfgAgentID)
	if agentId == "" || len(agentId) > MaxAgentIdLength || !r.MatchString(agentId) {
		cfgMap[cfgAgentID].value = randomString(MaxAgentIdLength - 1)
		log("config").Infof("agentId is automatically generated: %v", cfgMap[cfgAgentID].value)
	}

	sampleType := ConfigString(cfgSamplingType)
	sampleType = strings.ToUpper(strings.TrimSpace(sampleType))
	if sampleType == SamplingTypeCounter {
		rate := ConfigInt(cfgSamplingCounterRate)
		if rate < 0 {
			cfgMap[cfgSamplingCounterRate].value = 0
		}
	} else if sampleType == SamplingTypePercent {
		rate := ConfigFloat32(cfgSamplingPercentRate)
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

	trimStringSlice(ConfigStringSlice(cfgHttpStatusCodeErrors))
	trimStringSlice(ConfigStringSlice(cfgHttpExcludeUrl))
	trimStringSlice(ConfigStringSlice(cfgHttpExcludeMethod))
	trimStringSlice(ConfigStringSlice(cfgHttpRecordRequestHeader))
	trimStringSlice(ConfigStringSlice(cfgHttpRecordRespondHeader))
	trimStringSlice(ConfigStringSlice(cfgHttpRecordRequestCookie))

	if config.needContainerCheck {
		cfgMap[cfgRunOnContainer].value = isContainerEnv()
	}

	config.logLevel = parseLogLevel(ConfigString(cfgLogLevel))
	config.printConfigString()

	return config, nil
}

func parseLogLevel(level string) logrus.Level {
	lvl, e := logrus.ParseLevel(level)
	if e != nil {
		log("config").Errorf("invalid log level: %v", e)
		lvl = logrus.InfoLevel
	}
	return lvl
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

func cmdName(cfgName string) string {
	return "pinpoint-" + strings.ReplaceAll(strings.ToLower(cfgName), ".", "-")
}

func envName(cfgName string) string {
	return strings.ReplaceAll(strings.ToLower(cfgName), ".", "_")
}

func initCmdLineConfig(v *viper.Viper) {
	v.BindPFlags(pflag.CommandLine)
}

func initEnvConfig(v *viper.Viper) {
	v.SetEnvPrefix("pinpoint_go")
	v.AutomaticEnv()
}

func initProfile(v *viper.Viper) *viper.Viper {
	profile := v.GetString(cmdName(cfgProfile))
	return v.Sub("profile." + profile)
}

func trimStringSlice(slice []string) {
	for i := range slice {
		slice[i] = strings.TrimSpace(slice[i])
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

func defaultConfig() *Config {
	config := new(Config)

	config.logLevel = logrus.InfoLevel
	config.needContainerCheck = true
	config.offGrpc = false

	for _, v := range cfgMap {
		v.value = v.defaultValue
	}

	return config
}

func ConfigString(cfgName string) string {
	return cast.ToString(cfgMap[cfgName].value)
}

func ConfigInt(cfgName string) int {
	return cast.ToInt(cfgMap[cfgName].value)
}

func ConfigFloat32(cfgName string) float32 {
	return cast.ToFloat32(cfgMap[cfgName].value)
}

func ConfigStringSlice(cfgName string) []string {
	return cast.ToStringSlice(cfgMap[cfgName].value)
}

func ConfigBool(cfgName string) bool {
	return cast.ToBool(cfgMap[cfgName].value)
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
		c.needContainerCheck = false
	}
}

func WithHttpStatusCodeError(errors []string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgHttpStatusCodeErrors].value = errors
	}
}

func WithHttpExcludeUrl(urlPath []string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgHttpExcludeUrl].value = urlPath
	}
}

func WithHttpExcludeMethod(method []string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgHttpExcludeMethod].value = method
	}
}

func WithHttpRecordRequestHeader(header []string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgHttpRecordRequestHeader].value = header
	}
}

func WithHttpRecordRespondHeader(header []string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgHttpRecordRespondHeader].value = header
	}
}

func WithHttpRecordRequestCookie(cookie []string) ConfigOption {
	return func(c *Config) {
		cfgMap[cfgHttpRecordRequestCookie].value = cookie
	}
}

func (config *Config) printConfigString() {
	if config.cfgFilePath != "" {
		dat, err := ioutil.ReadFile(config.cfgFilePath)
		if err == nil {
			log("agent").Info("config_yaml_file= ", config.cfgFilePath, "\n", string(dat))
		}
	}

	for k, v := range cfgMap {
		log("agent").Infof("config: %s = %v", k, v.value)
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
