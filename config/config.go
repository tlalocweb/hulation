package config

import (
	"fmt"
	"os"

	"github.com/IzumaNetworks/conftagz"
	"gopkg.in/yaml.v2"
)

const (
	DEFAULT_PORT       = 8080
	DEFAULT_STORE_TYPE = "bolt"
	DEFAULT_STORE_PATH = "izvisitors.db"
)

type DBConfig struct {
	Username string `yaml:"username,omitempty" env:"DB_USERNAME" test:"~.+" default:"default"`
	Password string `yaml:"password,omitempty" env:"DB_PASSWORD"`
	Host     string `yaml:"host,omitempty" env:"DB_HOST" test:"~.+" default:"localhost"`
	DBName   string `yaml:"dbname,omitempty" env:"DB_NAME" test:"~.+" default:"db"`
	Port     int    `yaml:"port,omitempty" env:"DB_PORT" test:"<65536,>0" default:"9000"`
}

type Config struct {
	Port     int       `yaml:"port,omitempty" env:"APP_PORT" test:">1024,<65536" default:"8080"`
	DBConfig *DBConfig `yaml:"dbconfig,omitempty"`
	// Store *store.StoreConfig `yaml:"store"`
	// List of IP addresses to accept connections from
	// AcceptIPs []string `yaml:"accept_ips"`
}

func LoadConfig(filename string) (*Config, error) {
	var cfg Config

	buf, err := os.ReadFile(filename)
	if err != nil {
		return &cfg, fmt.Errorf("read file: %s", err.Error())
	}

	err = yaml.Unmarshal(buf, &cfg)
	if err != nil {
		return nil, fmt.Errorf("yaml parse: %s,", err.Error())
	}

	_, err = conftagz.Process(nil, &cfg)
	if err != nil {
		return nil, fmt.Errorf("bad config: %s,", err.Error())
	}

	return &cfg, nil
}

func GetDSNFromConfig(cfg *Config) string {
	return fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s",
		cfg.DBConfig.Username, cfg.DBConfig.Password, cfg.DBConfig.Host, cfg.DBConfig.Port, cfg.DBConfig.DBName)
}
