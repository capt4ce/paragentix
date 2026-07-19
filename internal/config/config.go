package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     ServerConfig       `yaml:"server"`
	Storage    StorageConfig      `yaml:"storage"`
	Model      ModelConfig        `yaml:"model"`
	Profiles   map[string]Profile `yaml:"profiles"`
	Discord    DiscordConfig      `yaml:"discord"`
	MCPServers []MCPServerConfig  `yaml:"mcp_servers"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
}
type StorageConfig struct {
	Path string `yaml:"path"`
}

type ModelConfig struct {
	Provider  string `yaml:"provider"`
	Model     string `yaml:"model"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

type Profile struct {
	Name           string   `yaml:"name"`
	WorkspaceRoots []string `yaml:"workspace_roots"`
	FileAccess     string   `yaml:"file_access"`
	EnabledTools   []string `yaml:"enabled_tools"`
	SkillsDir      string   `yaml:"skills_dir"`
}

type DiscordConfig struct {
	Enabled           bool     `yaml:"enabled"`
	TokenEnv          string   `yaml:"token_env"`
	DefaultProfile    string   `yaml:"default_profile"`
	AllowedChannelIDs []string `yaml:"allowed_channel_ids"`
}

type MCPServerConfig struct {
	Name      string   `yaml:"name"`
	Transport string   `yaml:"transport"`
	Command   string   `yaml:"command"`
	Args      []string `yaml:"args"`
	URL       string   `yaml:"url"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil && os.IsNotExist(err) {
		b, err = os.ReadFile("config.example.yaml")
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Storage.Path == "" {
		c.Storage.Path = "./data/agent.db"
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{"default": {Name: "default", FileAccess: "workspace"}}
	}
	return c, nil
}
