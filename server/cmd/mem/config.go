package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// cliConfig is the persisted CLI state at ~/.mem/config.yaml.
type cliConfig struct {
	Server    string `yaml:"server"`
	Email     string `yaml:"email,omitempty"`
	Token     string `yaml:"token,omitempty"`
	Workspace string `yaml:"workspace,omitempty"`
}

func configPath() (string, error) {
	if v := os.Getenv("MEM_CONFIG"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mem", "config.yaml"), nil
}

func loadConfig() (*cliConfig, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &cliConfig{Server: "http://localhost:8787"}, nil
		}
		return nil, err
	}
	var cfg cliConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Server == "" {
		cfg.Server = "http://localhost:8787"
	}
	return &cfg, nil
}

func saveConfig(c *cliConfig) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// resolveConfig merges persistent config with --server flag override.
func resolveConfig(serverOverride string) (*cliConfig, error) {
	c, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if serverOverride == "" {
		serverOverride = cliServerOverride
	}
	if serverOverride != "" {
		c.Server = serverOverride
	}
	if v := os.Getenv("MEM_SERVER"); v != "" && serverOverride == "" {
		c.Server = v
	}
	if v := os.Getenv("MEM_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("MEM_WORKSPACE"); v != "" {
		c.Workspace = v
	}
	if cliWorkspaceOverride != "" {
		c.Workspace = cliWorkspaceOverride
	}
	return c, nil
}
