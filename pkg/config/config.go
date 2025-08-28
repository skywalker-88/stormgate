package config

import (
	"os"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// ---- Server configuration ----

type Server struct {
	Addr string `yaml:"addr"`
}

type Identity struct {
	// "header:X-API-Key" or "ip"
	Source string `yaml:"source"`
}

// ---- Redis configuration ----

type Redis struct {
	Addr     string `yaml:"addr"`
	DB       int    `yaml:"db"`
	Password string `yaml:"password"`
}

// ---- Rate limiting policy ----

type Limit struct {
	RPS   float64 `yaml:"rps"`
	Burst int64   `yaml:"burst"`
	Cost  int64   `yaml:"cost"`
}

type Limits struct {
	Default      Limit            `yaml:"default"`
	Routes       map[string]Limit `yaml:"routes"`
	GlobalClient Limit            `yaml:"global_client"`
}

// ---- Anomaly detection policy ----

type Anomaly struct {
	Enabled               bool    `yaml:"enabled"`
	WindowSeconds         int     `yaml:"window_seconds"`
	Buckets               int     `yaml:"buckets"`
	ThresholdMultiplier   float64 `yaml:"threshold_multiplier"`
	EWMAAlpha             float64 `yaml:"ewma_alpha"`
	TTLSeconds            int     `yaml:"ttl_seconds"`
	EvictEverySeconds     int     `yaml:"evict_every_seconds"`
	KeepSuspiciousSeconds int     `yaml:"keep_suspicious_seconds"`
}

// ---- Mitigation policy ----

type StepRamp struct {
	Enabled     bool      `yaml:"enabled"`
	Steps       []float64 `yaml:"steps"`        // e.g., [0.5, 0.75, 1.0]
	StepSeconds int       `yaml:"step_seconds"` // informational; enforcement can choose how to use it
}

type RepeatOffender struct {
	WindowSeconds int `yaml:"window_seconds"` // M
	Threshold     int `yaml:"threshold"`      // N anomalies in window -> block
}

type Allowlist struct {
	Clients []string `yaml:"clients"` // client IDs (IP or API key) that skip mitigation
}

type Mitigation struct {
	MinRPS             float64        `yaml:"min_rps"`
	MinBurst           int            `yaml:"min_burst"`
	OverrideTTLSeconds int            `yaml:"override_ttl_seconds"`
	BlockTTLSeconds    int            `yaml:"block_ttl_seconds"`
	StepRamp           StepRamp       `yaml:"step_ramp"`
	RepeatOffender     RepeatOffender `yaml:"repeat_offender"`
	Allowlist          Allowlist      `yaml:"allowlist"`
}

// ---------------------------

type Config struct {
	Server     Server     `yaml:"server"`
	Redis      Redis      `yaml:"redis"`
	Identity   Identity   `yaml:"identity"`
	Limits     Limits     `yaml:"limits"`
	Anomaly    Anomaly    `yaml:"anomaly"`
	Mitigation Mitigation `yaml:"mitigation"`
}

func Load() (*Config, error) {
	// Allow env override if caller passes empty path.
	path := os.Getenv("STORMGATE_CONFIG")
	if path == "" {
		path = "configs/policies.yaml"
	}

	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, err
	}
	var cfg Config
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		Tag: "yaml",
	}); err != nil {
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
