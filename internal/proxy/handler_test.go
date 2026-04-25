package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/fchimpan/release-proxy/internal/upstream"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		cooldown   time.Duration
		modulePath string
		endpoint   string
		err        bool
	}{
		{
			name:       "list",
			path:       "/7d/golang.org/x/text/@v/list",
			cooldown:   7 * 24 * time.Hour,
			modulePath: "golang.org/x/text",
			endpoint:   "/@v/list",
		},
		{
			name:       "latest",
			path:       "/7d/golang.org/x/text/@latest",
			cooldown:   7 * 24 * time.Hour,
			modulePath: "golang.org/x/text",
			endpoint:   "/@latest",
		},
		{
			name:       "info",
			path:       "/7d/golang.org/x/text/@v/v0.3.0.info",
			cooldown:   7 * 24 * time.Hour,
			modulePath: "golang.org/x/text",
			endpoint:   "/@v/v0.3.0.info",
		},
		{
			name:       "mod",
			path:       "/7d/golang.org/x/text/@v/v0.3.0.mod",
			cooldown:   7 * 24 * time.Hour,
			modulePath: "golang.org/x/text",
			endpoint:   "/@v/v0.3.0.mod",
		},
		{
			name:       "zip",
			path:       "/7d/golang.org/x/text/@v/v0.3.0.zip",
			cooldown:   7 * 24 * time.Hour,
			modulePath: "golang.org/x/text",
			endpoint:   "/@v/v0.3.0.zip",
		},
		{
			name:       "hours",
			path:       "/24h/github.com/foo/bar/@v/list",
			cooldown:   24 * time.Hour,
			modulePath: "github.com/foo/bar",
			endpoint:   "/@v/list",
		},
		{
			name:       "v2_module",
			path:       "/7d/github.com/foo/bar/v2/@v/list",
			cooldown:   7 * 24 * time.Hour,
			modulePath: "github.com/foo/bar/v2",
			endpoint:   "/@v/list",
		},
		{
			name: "empty_path",
			path: "/",
			err:  true,
		},
		{
			name: "no_endpoint",
			path: "/7d/golang.org/x/text",
			err:  true,
		},
		{
			name: "invalid_cooldown",
			path: "/bad/golang.org/x/text/@v/list",
			err:  true,
		},
		{
			name: "latest_must_terminate_path",
			path: "/7d/example.com/mod/@latest/extra",
			err:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// -1 = "no default configured": prefix-less paths are an error.
			cooldown, modulePath, endpoint, err := parsePath(tt.path, -1)
			if tt.err {
				if err == nil {
					t.Errorf("expected error for path %q", tt.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for path %q: %v", tt.path, err)
			}
			if cooldown != tt.cooldown {
				t.Errorf("cooldown = %v, want %v", cooldown, tt.cooldown)
			}
			if modulePath != tt.modulePath {
				t.Errorf("modulePath = %q, want %q", modulePath, tt.modulePath)
			}
			if endpoint != tt.endpoint {
				t.Errorf("endpoint = %q, want %q", endpoint, tt.endpoint)
			}
		})
	}
}

func TestParsePath_DefaultCooldown(t *testing.T) {
	defaultCooldown := 7 * 24 * time.Hour

	// path without cooldown prefix, using default
	cooldown, modulePath, endpoint, err := parsePath("/golang.org/x/text/@v/list", defaultCooldown)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cooldown != defaultCooldown {
		t.Errorf("cooldown = %v, want %v", cooldown, defaultCooldown)
	}
	if modulePath != "golang.org/x/text" {
		t.Errorf("modulePath = %q, want %q", modulePath, "golang.org/x/text")
	}
	if endpoint != "/@v/list" {
		t.Errorf("endpoint = %q, want %q", endpoint, "/@v/list")
	}

	// same path without default should error
	_, _, _, err = parsePath("/golang.org/x/text/@v/list", -1)
	if err == nil {
		t.Error("expected error when no cooldown prefix and no default")
	}

	// explicit 0 default: prefix-less requests pass with no filtering (distinct from "unset").
	cooldown0, _, _, err := parsePath("/golang.org/x/text/@v/list", 0)
	if err != nil {
		t.Fatalf("unexpected error with explicit 0 default: %v", err)
	}
	if cooldown0 != 0 {
		t.Errorf("cooldown = %v, want 0 (explicit no-filter)", cooldown0)
	}
}

func TestParsePath_NegativePrefix(t *testing.T) {
	// "-" prefix must always be rejected, even when a default cooldown is set —
	// otherwise the proxy would silently treat "-1d/..." as a module name.
	for _, defaultCooldown := range []time.Duration{-1, 0, 7 * 24 * time.Hour} {
		_, _, _, err := parsePath("/-7d/example.com/mod/@v/list", defaultCooldown)
		if err == nil {
			t.Errorf("expected error for negative cooldown prefix (default=%v)", defaultCooldown)
			continue
		}
		if !strings.Contains(err.Error(), "invalid cooldown prefix") {
			t.Errorf("default=%v: error = %q, want 'invalid cooldown prefix'", defaultCooldown, err.Error())
		}
	}
}

func TestHandler_Sumdb_Returns404(t *testing.T) {
	h := NewHandler(t.Context(), Config{UpstreamURL: "http://example.invalid"}, slog.Default())

	req := httptest.NewRequest("GET", "/sumdb/sum.golang.org/supported", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (so go client falls back to direct sumdb)", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_Healthz(t *testing.T) {
	h := NewHandler(t.Context(), Config{UpstreamURL: "http://example.com"}, slog.Default())

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("healthz body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestHandler_List(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	// mock upstream server
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/@v/list"):
			fmt.Fprintln(w, "v1.0.0")
			fmt.Fprintln(w, "v1.1.0")
			fmt.Fprintln(w, "v1.2.0")
		case strings.HasSuffix(r.URL.Path, "v1.0.0.info"):
			json.NewEncoder(w).Encode(upstream.InfoResponse{
				Version: "v1.0.0",
				Time:    now.Add(-30 * 24 * time.Hour),
			})
		case strings.HasSuffix(r.URL.Path, "v1.1.0.info"):
			json.NewEncoder(w).Encode(upstream.InfoResponse{
				Version: "v1.1.0",
				Time:    now.Add(-10 * 24 * time.Hour),
			})
		case strings.HasSuffix(r.URL.Path, "v1.2.0.info"):
			json.NewEncoder(w).Encode(upstream.InfoResponse{
				Version: "v1.2.0",
				Time:    now.Add(-2 * 24 * time.Hour), // within 7d cooldown
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamSrv.Close()

	h := NewHandler(t.Context(), Config{
		UpstreamURL: upstreamSrv.URL,
		CacheTTL:    1 * time.Hour,
	}, slog.Default())
	h.filter.Now = func() time.Time { return now }

	req := httptest.NewRequest("GET", "/7d/example.com/mod/@v/list", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := strings.TrimSpace(rec.Body.String())
	lines := strings.Split(body, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 versions, got %d: %v", len(lines), lines)
	}
	if lines[0] != "v1.0.0" || lines[1] != "v1.1.0" {
		t.Errorf("unexpected versions: %v", lines)
	}
}

func TestHandler_Latest_WithinCooldown(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(upstream.InfoResponse{
			Version: "v1.2.0",
			Time:    now.Add(-2 * 24 * time.Hour), // 2 days ago, within 7d cooldown
		})
	}))
	defer upstreamSrv.Close()

	h := NewHandler(t.Context(), Config{
		UpstreamURL: upstreamSrv.URL,
		CacheTTL:    1 * time.Hour,
	}, slog.Default())
	h.filter.Now = func() time.Time { return now }

	req := httptest.NewRequest("GET", "/7d/example.com/mod/@latest", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (version within cooldown)", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_Latest_OutsideCooldown(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(upstream.InfoResponse{
			Version: "v1.0.0",
			Time:    now.Add(-30 * 24 * time.Hour),
		})
	}))
	defer upstreamSrv.Close()

	h := NewHandler(t.Context(), Config{
		UpstreamURL: upstreamSrv.URL,
		CacheTTL:    1 * time.Hour,
	}, slog.Default())
	h.filter.Now = func() time.Time { return now }

	req := httptest.NewRequest("GET", "/7d/example.com/mod/@latest", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var info upstream.InfoResponse
	if err := json.NewDecoder(rec.Body).Decode(&info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if info.Version != "v1.0.0" {
		t.Errorf("version = %q, want %q", info.Version, "v1.0.0")
	}
}

func TestHandler_Version_OutsideCooldown(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".info") {
			json.NewEncoder(w).Encode(upstream.InfoResponse{
				Version: "v1.0.0",
				Time:    now.Add(-30 * 24 * time.Hour),
			})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Write([]byte("fake-zip-content"))
	}))
	defer upstreamSrv.Close()

	h := NewHandler(t.Context(), Config{
		UpstreamURL: upstreamSrv.URL,
		CacheTTL:    1 * time.Hour,
	}, slog.Default())
	h.filter.Now = func() time.Time { return now }

	req := httptest.NewRequest("GET", "/7d/example.com/mod/@v/v1.0.0.zip", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "fake-zip-content" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "fake-zip-content")
	}
}

func TestHandler_Version_WithinCooldown(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	var zipServed bool
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".info") {
			json.NewEncoder(w).Encode(upstream.InfoResponse{
				Version: "v1.2.0",
				Time:    now.Add(-2 * 24 * time.Hour), // within 7d cooldown
			})
			return
		}
		zipServed = true
		w.Write([]byte("zip-data"))
	}))
	defer upstreamSrv.Close()

	for _, ext := range []string{".info", ".mod", ".zip"} {
		t.Run(ext, func(t *testing.T) {
			h := NewHandler(t.Context(), Config{
				UpstreamURL: upstreamSrv.URL,
				CacheTTL:    1 * time.Hour,
			}, slog.Default())
			h.filter.Now = func() time.Time { return now }

			req := httptest.NewRequest("GET", "/7d/example.com/mod/@v/v1.2.0"+ext, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
		})
	}

	if zipServed {
		t.Error("zip body was served despite version being within cooldown")
	}
}

func TestHandler_Version_Info_ServesFromCache(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)

	var infoCalls int
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".info") {
			infoCalls++
			json.NewEncoder(w).Encode(upstream.InfoResponse{
				Version: "v1.0.0",
				Time:    now.Add(-30 * 24 * time.Hour),
			})
			return
		}
		t.Errorf("unexpected upstream path: %s", r.URL.Path)
	}))
	defer upstreamSrv.Close()

	h := NewHandler(t.Context(), Config{
		UpstreamURL: upstreamSrv.URL,
		CacheTTL:    1 * time.Hour,
	}, slog.Default())
	h.filter.Now = func() time.Time { return now }

	req := httptest.NewRequest("GET", "/7d/example.com/mod/@v/v1.0.0.info", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var info upstream.InfoResponse
	if err := json.NewDecoder(rec.Body).Decode(&info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if info.Version != "v1.0.0" {
		t.Errorf("version = %q, want %q", info.Version, "v1.0.0")
	}
	if infoCalls != 1 {
		t.Errorf("upstream .info calls = %d, want 1 (no duplicate fetch)", infoCalls)
	}
}

func TestParseVersionEndpoint(t *testing.T) {
	tests := []struct {
		endpoint string
		version  string
		ok       bool
	}{
		{"/@v/v1.0.0.info", "v1.0.0", true},
		{"/@v/v1.0.0.mod", "v1.0.0", true},
		{"/@v/v1.0.0.zip", "v1.0.0", true},
		{"/@v/v0.0.0-20210101120000-abcdef123456.zip", "v0.0.0-20210101120000-abcdef123456", true},
		{"/@v/list", "", false},
		{"/@latest", "", false},
		{"/@v/v1.0.0.txt", "", false},
		{"/something/else", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			v, ok := parseVersionEndpoint(tt.endpoint)
			if v != tt.version || ok != tt.ok {
				t.Errorf("parseVersionEndpoint(%q) = (%q, %v), want (%q, %v)", tt.endpoint, v, ok, tt.version, tt.ok)
			}
		})
	}
}

func TestHandler_ExcludedModule(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// should receive the list request directly (no filtering)
		fmt.Fprintln(w, "v1.0.0")
		fmt.Fprintln(w, "v1.1.0")
	}))
	defer upstreamSrv.Close()

	h := NewHandler(t.Context(), Config{
		UpstreamURL: upstreamSrv.URL,
		Exclusions:  []string{"github.com/mycompany/"},
		CacheTTL:    1 * time.Hour,
	}, slog.Default())

	req := httptest.NewRequest("GET", "/7d/github.com/mycompany/lib/@v/list", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := strings.TrimSpace(rec.Body.String())
	lines := strings.Split(body, "\n")
	if len(lines) != 2 {
		t.Errorf("excluded module should return all versions, got %d", len(lines))
	}
}
