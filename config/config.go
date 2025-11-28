package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

// RateLimitConfig 限流配置
type RateLimitConfig struct {
	Requests int           `yaml:"requests"`
	Duration time.Duration `yaml:"duration"`
}

// Config 应用配置
type Config struct {
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`

	Redis struct {
		Addr     string `yaml:"addr"`
		Password string `yaml:"password"`
		DB       int    `yaml:"db"`
	} `yaml:"redis"`

	RabbitMQ struct {
		URL string `yaml:"url"`
	} `yaml:"rabbitmq"`

	ZooKeeper struct {
		Hosts    []string      `yaml:"hosts"`
		Timeout  time.Duration `yaml:"timeout"`
		RootPath string        `yaml:"root_path"`
	} `yaml:"zookeeper"`

	RateLimit RateLimitConfig `yaml:"rate_limit"`

	Seckill struct {
		ProductID    string `yaml:"product_id"`
		TotalStock   int    `yaml:"total_stock"`
		SeckillPrice int    `yaml:"seckill_price"`
	} `yaml:"seckill"`
}

func LoadConfig() *Config {
	file, err := os.Open("config/config.yaml")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	var cfg Config
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		panic(err)
	}

	return &cfg
}

// 获取限流配置的辅助方法
func (c *Config) GetRateLimitConfig() RateLimitConfig {
	return c.RateLimit
}
