package pinpoint

import (
	"encoding/json"
	"errors"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	"math/rand"
	"os"
	"time"
)

type Config struct {
	ApplicationName string
	ApplicationType int32
	AgentId         string
	ConfigFilePath  string

	Collector struct {
		Host      string
		AgentPort int
		SpanPort  int
		StatPort  int
	}

	LogLevel logrus.Level

	Sampling struct {
		Rate               int
		NewThroughput      int
		ContinueThroughput int
	}

	Stat struct {
		CollectInterval int
		BatchCount      int
	}
}

type ConfigOption func(*Config)

func NewConfig(opts ...ConfigOption) (*Config, error) {
	config := defaultConfig()

	for _, fn := range opts {
		fn(config)
	}

	if config.ConfigFilePath != "" {
		err := readConfigFile(config)
		if err != nil {
			return nil, err
		}
	}

	if config.ApplicationName == "" {
		return nil, errors.New("pinpoint config error: application name is missing")
	}

	if config.AgentId == "" {
		config.AgentId = randomString(MaxAgentIdLength)
		log("config").Info("agentId is automatically generated: ", config.AgentId)
	}

	return config, nil
}

func defaultConfig() *Config {
	config := new(Config)

	config.ApplicationName = ""
	config.ApplicationType = ServiceTypeGoApp
	config.AgentId = ""

	config.Collector.Host = "localhost"
	config.Collector.AgentPort = 9991
	config.Collector.StatPort = 9992
	config.Collector.SpanPort = 9993

	config.LogLevel = logrus.InfoLevel

	config.Sampling.Rate = 1
	config.Sampling.NewThroughput = 0
	config.Sampling.ContinueThroughput = 0

	config.Stat.CollectInterval = 5000 //ms
	config.Stat.BatchCount = 6

	return config
}

func readConfigFile(config *Config) error {
	f, err := os.Open(config.ConfigFilePath)
	if err != nil {
		log("config").Error("pinpoint config file error - ", err)
		return err
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	err = dec.Decode(config)
	if err != nil {
		log("config").Error("yaml config file is corrupted - ", err)
	}

	return err
}

func WithAppName(name string) ConfigOption {
	return func(c *Config) {
		c.ApplicationName = name
	}
}

func WithAppType(typ int32) ConfigOption {
	return func(c *Config) {
		c.ApplicationType = typ
	}
}

func WithAgentId(id string) ConfigOption {
	return func(c *Config) {
		if len(id) > MaxAgentIdLength {
			id = id[:MaxAgentIdLength]
		}
		c.AgentId = id
	}
}

func WithConfigFile(filePath string) ConfigOption {
	return func(c *Config) {
		c.ConfigFilePath = filePath
	}
}

func WithCollectorHost(host string) ConfigOption {
	return func(c *Config) {
		c.Collector.Host = host
	}
}

func WithCollectorAgentPort(port int) ConfigOption {
	return func(c *Config) {
		c.Collector.AgentPort = port
	}
}

func WithCollectorSpanPort(port int) ConfigOption {
	return func(c *Config) {
		c.Collector.SpanPort = port
	}
}

func WithCollectorStatPort(port int) ConfigOption {
	return func(c *Config) {
		c.Collector.StatPort = port
	}
}

func WithLogLevel(level string) ConfigOption {
	return func(c *Config) {
		l, e := logrus.ParseLevel(level)
		if e != nil {
			log("config").Error("invalid log level: ", e)
			l = logrus.InfoLevel
		}
		c.LogLevel = l
	}
}

func WithSamplingRate(rate int) ConfigOption {
	return func(c *Config) {
		c.Sampling.Rate = rate
	}
}

func WithSamplingNewThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.Sampling.NewThroughput = tps
	}
}

func WithSamplingContinueThroughput(tps int) ConfigOption {
	return func(c *Config) {
		c.Sampling.ContinueThroughput = tps
	}
}

func WithStatCollectInterval(interval int) ConfigOption {
	return func(c *Config) {
		c.Stat.CollectInterval = interval
	}
}

func WithStatBatchCount(count int) ConfigOption {
	return func(c *Config) {
		c.Stat.BatchCount = count
	}
}

func (config *Config) String() string {
	j, _ := json.Marshal(config)
	return string(j)
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
