package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":8080" || cfg.Storage.Path != "./data/agent.db" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.Profiles["default"].Name != "default" {
		t.Fatalf("default profile missing: %+v", cfg.Profiles)
	}
}
