package config

import (
	"hulation/log"
	"hulation/store"
	"os"

	"gopkg.in/yaml.v2"
)

const (
	DEFAULT_PORT       = 8080
	DEFAULT_STORE_TYPE = "bolt"
	DEFAULT_STORE_PATH = "izvisitors.db"
)

type Config struct {
	Port  int                `yaml:"port,omitempty" env:"APP_PORT" test:">1024" default:"8080"`
	Store *store.StoreConfig `yaml:"store"`
	// List of IP addresses to accept connections from
	// AcceptIPs []string `yaml:"accept_ips"`
}

func ReadConfig(filename string) (*Config, error) {
	var cfg Config

	cfg.Port = DEFAULT_PORT
	cfg.Store = &store.StoreConfig{
		Type: DEFAULT_STORE_TYPE,
		Path: DEFAULT_STORE_PATH,
	}

	buf, err := os.ReadFile(filename)
	if err != nil {
		return &cfg, err
	}

	err = yaml.Unmarshal(buf, &cfg)
	if err != nil {
		return nil, err
	}

	override, err := utils.EnvFieldSubstitution(&cfg, nil)

	for _, v := range override {
		log.Infof("saw ENV %s", v)
	}

	return &cfg, nil
}
