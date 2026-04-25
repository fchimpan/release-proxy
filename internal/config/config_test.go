package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad_EmptyPath(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg, Config{}) {
		t.Errorf("expected zero-value Config, got %+v", cfg)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg, Config{}) {
		t.Errorf("expected zero-value Config, got %+v", cfg)
	}
}

func TestLoad_Valid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release-proxy.json")
	body := `{
		"minimum-release-age": "7d",
		"minimum-release-age-exclude": ["github.com/foo/", "internal.corp/"],
		"upstream": "https://example.com",
		"port": "9090",
		"log-level": "debug",
		"cache-ttl": "30m"
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{
		MinimumReleaseAge:        "7d",
		MinimumReleaseAgeExclude: []string{"github.com/foo/", "internal.corp/"},
		Upstream:                 "https://example.com",
		Port:                     "9090",
		LogLevel:                 "debug",
		CacheTTL:                 "30m",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
