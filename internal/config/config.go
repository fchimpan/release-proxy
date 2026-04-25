package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Config is the JSON schema for the optional config file.
// Keys mirror pnpm's vocabulary (minimum-release-age, minimum-release-age-exclude).
type Config struct {
	MinimumReleaseAge        string   `json:"minimum-release-age"`
	MinimumReleaseAgeExclude []string `json:"minimum-release-age-exclude"`
	Upstream                 string   `json:"upstream"`
	Port                     string   `json:"port"`
	LogLevel                 string   `json:"log-level"`
	CacheTTL                 string   `json:"cache-ttl"`
}

// Load reads a JSON config from path. A missing file returns a zero-valued
// Config with no error — the file is optional.
func Load(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}
