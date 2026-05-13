package config

import (
	"log"
	"os"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ServerPort    int       `yaml:"server_port"`
	ServerAddr    string    `yaml:"server_addr"`
	BasePath      string    `yaml:"base_path"`
	OpenAIBaseUrl string    `yaml:"openai_base_url"`
	Log           LogConfig `yaml:"log"`
}

var globalConf atomic.Value // stores *Config

var (
	defaultConf  = &Config{}
	configWarned sync.Once
)

func UnmarshalYamlFile(filePath string, out interface{}) error {
	yamlFile, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(yamlFile, out)
}

func LoadConfigFromPath(path string) (*Config, error) {
	var c Config
	err := UnmarshalYamlFile(path, &c)
	if err != nil {
		return nil, err
	}
	globalConf.Store(&c)
	return &c, nil
}

func GetConfig() *Config {
	v := globalConf.Load()
	if v == nil {
		configWarned.Do(func() {
			log.Printf("config not loaded yet, using default config")
		})
		return defaultConf
	}
	return v.(*Config)
}
