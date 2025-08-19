package config

import (
	"os"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Server struct {
	Addr string `yaml:"addr"`
}

type Redis struct {
	Addr     string `yaml:"addr"`
	DB       int    `yaml:"db"`
	Password string `yaml:"password"`
}

type Identity struct {
	Source string `yaml:"source"`
}

type Limit struct {
	RPS   float64 `yaml:"rps"`
	Burst int64   `yaml:"burst"`
	Cost  int64   `yaml:"cost"`
}

type Limits struct {
	Default Limit            `yaml:"default"`
	Routes  map[string]Limit `yaml:"routes"`
}

type Config struct {
	Server   Server   `yaml:"server"`
	Redis    Redis    `yaml:"redis"`
	Identity Identity `yaml:"identity"`
	Limits   Limits   `yaml:"limits"`
}

func Load(path string) (*Config, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, err
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func MustEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
