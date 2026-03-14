package server

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config はサーバーの設定を保持する
type Config struct {
	ControlPort int    `yaml:"control_port"`
	AuthToken   string `yaml:"auth_token"`
	PortRange   struct {
		Min int `yaml:"min"`
		Max int `yaml:"max"`
	} `yaml:"port_range"`
	LogLevel string `yaml:"log_level"`
}

// Validate は設定値のバリデーションを行う
func (c *Config) Validate() error {
	if c.AuthToken == "" {
		return fmt.Errorf("auth_token is required")
	}
	min, max := c.EffectivePortRange()
	if min >= max {
		return fmt.Errorf("port_range.min (%d) must be less than port_range.max (%d)", min, max)
	}
	if min < 1024 {
		return fmt.Errorf("port_range.min (%d) must be >= 1024", min)
	}
	return nil
}

// EffectivePortRange はゼロ値のときにデフォルトを返す
func (c *Config) EffectivePortRange() (int, int) {
	min := c.PortRange.Min
	max := c.PortRange.Max
	if min == 0 {
		min = 10000
	}
	if max == 0 {
		max = 20000
	}
	return min, max
}

// EffectiveControlPort はゼロ値のときにデフォルトを返す
func (c *Config) EffectiveControlPort() int {
	if c.ControlPort == 0 {
		return 4610
	}
	return c.ControlPort
}

// LoadConfig は YAML ファイルから設定を読み込む
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", path, err)
	}
	return &cfg, nil
}
