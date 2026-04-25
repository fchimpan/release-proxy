package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fchimpan/release-proxy/internal/filter"
	"github.com/fchimpan/release-proxy/internal/upstream"
)

// Config holds runtime configuration for the proxy handler.
type Config struct {
	UpstreamURL     string
	Exclusions      []string
	DefaultCooldown time.Duration
	CacheTTL        time.Duration
}

// Handler is the HTTP handler for the release-proxy.
type Handler struct {
	client          *upstream.Client
	filter          *filter.CooldownFilter
	logger          *slog.Logger
	defaultCooldown time.Duration
}

// NewHandler creates a proxy handler with the given configuration.
// ctx controls the lifecycle of background goroutines (cache reaper).
func NewHandler(ctx context.Context, cfg Config, logger *slog.Logger) *Handler {
	return &Handler{
		client:          upstream.NewClient(ctx, cfg.UpstreamURL, cfg.CacheTTL, logger),
		filter:          filter.NewCooldownFilter(cfg.Exclusions),
		logger:          logger,
		defaultCooldown: cfg.DefaultCooldown,
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
		return
	}

	// Return 404 for sumdb requests so the go client falls back to direct
	// (https://go.dev/ref/mod#module-proxy — "If a proxy does not support sumdb...").
	if strings.HasPrefix(r.URL.Path, "/sumdb/") {
		h.logger.DebugContext(ctx, "sumdb request not proxied", "path", r.URL.Path)
		http.NotFound(w, r)
		return
	}

	start := time.Now()
	rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	cooldown, modulePath, endpoint, err := parsePath(r.URL.Path, h.defaultCooldown)
	if err != nil {
		h.logger.WarnContext(ctx, "invalid request path",
			"method", r.Method,
			"path", r.URL.Path,
			"error", err,
		)
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	excluded := h.filter.ShouldExclude(modulePath)

	defer func() {
		h.logger.InfoContext(ctx, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"module", modulePath,
			"endpoint", endpoint,
			"cooldown", cooldown.String(),
			"excluded", excluded,
			"status", rw.statusCode,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}()

	if excluded {
		h.proxyPassthrough(ctx, rw, modulePath, endpoint)
		return
	}

	switch {
	case endpoint == "/@v/list":
		h.handleList(ctx, rw, modulePath, cooldown)
	case endpoint == "/@latest":
		h.handleLatest(ctx, rw, modulePath, cooldown)
	default:
		if version, ok := parseVersionEndpoint(endpoint); ok {
			h.handleVersion(ctx, rw, modulePath, version, endpoint, cooldown)
			return
		}
		h.proxyPassthrough(ctx, rw, modulePath, endpoint)
	}
}

func (h *Handler) handleList(ctx context.Context, w http.ResponseWriter, module string, cooldown time.Duration) {
	versions, err := h.client.FetchList(ctx, module)
	if err != nil {
		h.handleUpstreamError(ctx, w, module, err)
		return
	}

	fetcher := func(ctx context.Context, version string) (time.Time, error) {
		info, err := h.client.FetchInfo(ctx, module, version)
		if err != nil {
			return time.Time{}, err
		}
		return info.Time, nil
	}

	filtered, err := h.filter.FilterVersionList(ctx, versions, cooldown, fetcher)
	if err != nil {
		h.logger.ErrorContext(ctx, "filter version list failed", "module", module, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.logger.DebugContext(ctx, "filtered version list",
		"module", module,
		"total", len(versions),
		"after_filter", len(filtered),
		"cooldown", cooldown,
	)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, v := range filtered {
		fmt.Fprintln(w, v)
	}
}

func (h *Handler) handleLatest(ctx context.Context, w http.ResponseWriter, module string, cooldown time.Duration) {
	info, err := h.client.FetchLatestInfo(ctx, module)
	if err != nil {
		h.handleUpstreamError(ctx, w, module, err)
		return
	}

	if h.filter.IsWithinCooldown(info.Time, cooldown) {
		h.logger.DebugContext(ctx, "latest version within cooldown",
			"module", module,
			"version", info.Version,
			"release_time", info.Time,
			"cooldown", cooldown,
		)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(info); err != nil {
		h.logger.ErrorContext(ctx, "encode latest response", "module", module, "error", err)
	}
}

// handleVersion enforces cooldown on version-specific endpoints (.info/.mod/.zip)
// by resolving the release time via FetchInfo first.
func (h *Handler) handleVersion(ctx context.Context, w http.ResponseWriter, module, version, endpoint string, cooldown time.Duration) {
	info, err := h.client.FetchInfo(ctx, module, version)
	if err != nil {
		h.handleUpstreamError(ctx, w, module, err)
		return
	}

	if h.filter.IsWithinCooldown(info.Time, cooldown) {
		h.logger.DebugContext(ctx, "version within cooldown",
			"module", module,
			"version", version,
			"release_time", info.Time,
			"cooldown", cooldown,
		)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// .info responses are reconstituted from the cached InfoResponse to avoid a
	// duplicate upstream fetch. Optional upstream fields (e.g. Origin) are not preserved.
	if strings.HasSuffix(endpoint, ".info") {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(info); err != nil {
			h.logger.ErrorContext(ctx, "encode info response", "module", module, "version", version, "error", err)
		}
		return
	}

	h.proxyPassthrough(ctx, w, module, endpoint)
}

func (h *Handler) proxyPassthrough(ctx context.Context, w http.ResponseWriter, module, endpoint string) {
	h.client.ProxyRequest(ctx, w, module+endpoint)
}

func (h *Handler) handleUpstreamError(ctx context.Context, w http.ResponseWriter, module string, err error) {
	var httpErr *upstream.HTTPError
	if errors.As(err, &httpErr) {
		h.logger.DebugContext(ctx, "upstream error", "module", module, "status", httpErr.StatusCode)
		http.Error(w, http.StatusText(httpErr.StatusCode), httpErr.StatusCode)
		return
	}
	h.logger.ErrorContext(ctx, "upstream request failed", "module", module, "error", err)
	http.Error(w, "bad gateway", http.StatusBadGateway)
}

// parsePath extracts cooldown, module path, and endpoint from the request path.
// Expected format: /{cooldown}/{module...}/@v/{endpoint} or /{cooldown}/{module...}/@latest
//
// Examples:
//
//	/7d/golang.org/x/text/@v/list         -> 7d, golang.org/x/text, /@v/list
//	/7d/golang.org/x/text/@latest         -> 7d, golang.org/x/text, /@latest
//	/7d/golang.org/x/text/@v/v0.3.0.info  -> 7d, golang.org/x/text, /@v/v0.3.0.info
//	/7d/golang.org/x/text/@v/v0.3.0.mod   -> 7d, golang.org/x/text, /@v/v0.3.0.mod
//	/7d/golang.org/x/text/@v/v0.3.0.zip   -> 7d, golang.org/x/text, /@v/v0.3.0.zip
func parsePath(path string, defaultCooldown time.Duration) (cooldown time.Duration, modulePath string, endpoint string, err error) {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return 0, "", "", fmt.Errorf("empty path")
	}

	// extract cooldown prefix
	slashIdx := strings.Index(path, "/")
	if slashIdx < 0 {
		return 0, "", "", fmt.Errorf("missing module path")
	}
	cooldownStr := path[:slashIdx]
	rest := path[slashIdx+1:] // module + endpoint

	cooldown, parseErr := filter.ParseDuration(cooldownStr)
	if parseErr != nil {
		// no cooldown prefix; treat entire path as module+endpoint
		if defaultCooldown > 0 {
			cooldown = defaultCooldown
			rest = path
		} else {
			return 0, "", "", fmt.Errorf("invalid cooldown %q and no default configured: %w", cooldownStr, parseErr)
		}
	}

	// find endpoint: /@latest must terminate the path; /@v/ marks the start of a sub-endpoint.
	if mod, found := strings.CutSuffix(rest, "/@latest"); found {
		modulePath = mod
		endpoint = "/@latest"
	} else if idx := strings.Index(rest, "/@v/"); idx >= 0 {
		modulePath = rest[:idx]
		endpoint = rest[idx:]
	} else {
		return 0, "", "", fmt.Errorf("no valid endpoint in path: %s", rest)
	}

	if modulePath == "" {
		return 0, "", "", fmt.Errorf("empty module path")
	}

	return cooldown, modulePath, endpoint, nil
}

// parseVersionEndpoint extracts the version from a /@v/{version}.{info,mod,zip} endpoint.
// Returns ok=false for /@v/list or any path that doesn't match.
func parseVersionEndpoint(endpoint string) (version string, ok bool) {
	rest, found := strings.CutPrefix(endpoint, "/@v/")
	if !found {
		return "", false
	}
	for _, suffix := range []string{".info", ".mod", ".zip"} {
		if v, found := strings.CutSuffix(rest, suffix); found {
			return v, true
		}
	}
	return "", false
}
