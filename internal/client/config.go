package client

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config はクライアントの設定を保持する
type Config struct {
	ServerAddr string         `yaml:"server"`
	AuthToken  string         `yaml:"auth_token"`
	Tunnels    []TunnelConfig `yaml:"tunnels"`
	LogLevel   string         `yaml:"log_level"`
}

// TunnelConfig は 1 つのトンネルの設定
type TunnelConfig struct {
	LocalPort  int    `yaml:"local_port"`
	RemotePort int    `yaml:"remote_port"`
	Protocol   string `yaml:"protocol"`
}

// Validate は設定値のバリデーションを行う
func (c *Config) Validate() error {
	if c.ServerAddr == "" {
		return fmt.Errorf("server address is required")
	}
	if c.AuthToken == "" {
		return fmt.Errorf("auth_token is required")
	}
	if len(c.Tunnels) == 0 {
		return fmt.Errorf("at least one tunnel must be configured")
	}
	for i, t := range c.Tunnels {
		if t.LocalPort <= 0 {
			return fmt.Errorf("tunnel[%d]: local_port must be > 0", i)
		}
		if t.Protocol == "" {
			c.Tunnels[i].Protocol = "tcp"
		}
	}
	return nil
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
