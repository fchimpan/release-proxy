package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fchimpan/release-proxy/internal/config"
	"github.com/fchimpan/release-proxy/internal/filter"
	"github.com/fchimpan/release-proxy/internal/proxy"
)

const defaultConfigPath = "release-proxy.json"

// Run wires up dependencies and runs the release-proxy server until ctx is cancelled.
// getenv and stdout are injected so tests can stub the environment and silence output.
//
// Configuration precedence per setting: env var > config file > built-in default.
// Config file path: $RELEASE_PROXY_CONFIG, falling back to ./release-proxy.json (optional).
func Run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	configPathExplicit := getenv("RELEASE_PROXY_CONFIG")
	configPath := cmp.Or(configPathExplicit, defaultConfigPath)

	// When the user explicitly sets RELEASE_PROXY_CONFIG, a missing/unreadable
	// file is a startup error (typo detection). For the implicit default path,
	// missing-file is fine — the config file is optional.
	if configPathExplicit != "" {
		if _, err := os.Stat(configPathExplicit); err != nil {
			return fmt.Errorf("config file: %w", err)
		}
	}
	fileCfg, configErr := config.Load(configPath)

	logLevel := cmp.Or(getenv("RELEASE_PROXY_LOG_LEVEL"), fileCfg.LogLevel, "info")
	logger := newLogger(logLevel, stdout)

	if configErr != nil {
		if configPathExplicit != "" {
			return fmt.Errorf("load config: %w", configErr)
		}
		logger.WarnContext(ctx, "config file ignored", "path", configPath, "error", configErr)
	}

	exclusions := parseExclusions(getenv("RELEASE_PROXY_MINIMUM_RELEASE_AGE_EXCLUDE"))
	if exclusions == nil {
		exclusions = fileCfg.MinimumReleaseAgeExclude
	}

	// Cooldown semantics:
	//   empty value         → not configured (sentinel: -1; prefix-less requests are 400)
	//   parse error         → startup failure (typo detection, vs. silent fallback to 0)
	//   "0s" or any valid d → applied as default (0s = explicit no-filter, distinct from unset)
	defaultCooldown := time.Duration(-1)
	if ageStr := cmp.Or(getenv("RELEASE_PROXY_MINIMUM_RELEASE_AGE"), fileCfg.MinimumReleaseAge); ageStr != "" {
		d, err := filter.ParseDuration(ageStr)
		if err != nil {
			return fmt.Errorf("invalid RELEASE_PROXY_MINIMUM_RELEASE_AGE %q: %w", ageStr, err)
		}
		defaultCooldown = d
	}

	cfg := proxy.Config{
		UpstreamURL:     cmp.Or(getenv("RELEASE_PROXY_UPSTREAM"), fileCfg.Upstream, "https://proxy.golang.org"),
		Exclusions:      exclusions,
		DefaultCooldown: defaultCooldown,
		CacheTTL: parseDuration(ctx, "RELEASE_PROXY_CACHE_TTL",
			cmp.Or(getenv("RELEASE_PROXY_CACHE_TTL"), fileCfg.CacheTTL), time.Hour, logger),
	}
	port := cmp.Or(getenv("RELEASE_PROXY_PORT"), fileCfg.Port, "8080")

	// Bind synchronously so listen errors surface immediately and so tests / readiness
	// probes can rely on the listener being ready by the time Run reaches the select.
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return fmt.Errorf("listen on :%s: %w", port, err)
	}

	srv := &http.Server{
		Handler:      proxy.NewHandler(ctx, cfg, logger),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		defer close(serveErr)
		logger.InfoContext(ctx, "starting release-proxy",
			"addr", ln.Addr().String(),
			"upstream", cfg.UpstreamURL,
			"minimum_release_age", cfg.DefaultCooldown,
			"cache_ttl", cfg.CacheTTL,
			"exclusions", cfg.Exclusions,
			"config_file", configPath,
		)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err, ok := <-serveErr:
		if ok && err != nil {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.InfoContext(ctx, "shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.ErrorContext(shutdownCtx, "shutdown error", "error", err)
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func newLogger(levelStr string, w io.Writer) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(levelStr)); err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

func parseExclusions(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	exclusions := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			exclusions = append(exclusions, p)
		}
	}
	return exclusions
}

func parseDuration(ctx context.Context, key, value string, defaultVal time.Duration, logger *slog.Logger) time.Duration {
	if value == "" {
		return defaultVal
	}
	d, err := filter.ParseDuration(value)
	if err != nil {
		logger.WarnContext(ctx, "invalid duration, using default", "key", key, "value", value, "default", defaultVal, "error", err)
		return defaultVal
	}
	return d
}
