package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

// InfoResponse is the JSON shape of .info and @latest responses from the Go module proxy.
type InfoResponse struct {
	Version string    `json:"Version"`
	Time    time.Time `json:"Time"`
}

// Client communicates with the upstream Go module proxy.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client

	proxy *httputil.ReverseProxy

	mu    sync.RWMutex
	cache map[string]cacheEntry
	ttl   time.Duration
}

type cacheEntry struct {
	info      InfoResponse
	fetchedAt time.Time
}

// NewClient creates an upstream client with the given base URL and cache TTL.
// The reaper goroutine that evicts expired cache entries runs until ctx is cancelled.
func NewClient(ctx context.Context, baseURL string, cacheTTL time.Duration, logger *slog.Logger) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 32

	c := &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		proxy: &httputil.ReverseProxy{
			Director:  func(*http.Request) {}, // request URL is pre-set by ProxyRequest
			Transport: transport,
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				logger.ErrorContext(r.Context(), "upstream proxy error", "url", r.URL.String(), "error", err)
				w.WriteHeader(http.StatusBadGateway)
			},
		},
		cache: make(map[string]cacheEntry),
		ttl:   cacheTTL,
	}

	if cacheTTL > 0 {
		go c.reapLoop(ctx)
	}
	return c
}

// reapLoop periodically evicts cache entries older than the TTL.
// Without it the cache would grow unbounded over the process lifetime.
func (c *Client) reapLoop(ctx context.Context) {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.mu.Lock()
			for k, v := range c.cache {
				if now.Sub(v.fetchedAt) >= c.ttl {
					delete(c.cache, k)
				}
			}
			c.mu.Unlock()
		}
	}
}

// FetchList fetches /$module/@v/list and returns version strings.
func (c *Client) FetchList(ctx context.Context, module string) ([]string, error) {
	url := fmt.Sprintf("%s/%s/@v/list", c.BaseURL, module)
	body, err := c.get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch list for %s: %w", module, err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read list for %s: %w", module, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	versions := make([]string, 0, len(lines))
	for _, line := range lines {
		v := strings.TrimSpace(line)
		if v != "" {
			versions = append(versions, v)
		}
	}
	return versions, nil
}

// FetchInfo fetches /$module/@v/$version.info with caching.
func (c *Client) FetchInfo(ctx context.Context, module, version string) (*InfoResponse, error) {
	key := module + "@" + version

	c.mu.RLock()
	if entry, ok := c.cache[key]; ok && time.Since(entry.fetchedAt) < c.ttl {
		c.mu.RUnlock()
		return &entry.info, nil
	}
	c.mu.RUnlock()

	url := fmt.Sprintf("%s/%s/@v/%s.info", c.BaseURL, module, version)
	info, err := c.fetchInfoJSON(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch info for %s@%s: %w", module, version, err)
	}

	c.mu.Lock()
	if entry, ok := c.cache[key]; ok && time.Since(entry.fetchedAt) < c.ttl {
		c.mu.Unlock()
		return &entry.info, nil
	}
	c.cache[key] = cacheEntry{info: *info, fetchedAt: time.Now()}
	c.mu.Unlock()

	return info, nil
}

// FetchLatestInfo fetches /$module/@latest.
func (c *Client) FetchLatestInfo(ctx context.Context, module string) (*InfoResponse, error) {
	url := fmt.Sprintf("%s/%s/@latest", c.BaseURL, module)
	info, err := c.fetchInfoJSON(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch latest for %s: %w", module, err)
	}
	return info, nil
}

// ProxyRequest streams an upstream response back to the client.
// Hop-by-hop header stripping and trailer handling are delegated to httputil.ReverseProxy.
func (c *Client) ProxyRequest(ctx context.Context, w http.ResponseWriter, modulePath string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/"+modulePath, nil)
	if err != nil {
		http.Error(w, "invalid upstream URL", http.StatusBadRequest)
		return
	}
	c.proxy.ServeHTTP(w, req)
}

func (c *Client) fetchInfoJSON(ctx context.Context, url string) (*InfoResponse, error) {
	body, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var info InfoResponse
	if err := json.NewDecoder(body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode info from %s: %w", url, err)
	}
	return &info, nil
}

func (c *Client) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request for %s: %w", url, err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s: %w", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &HTTPError{StatusCode: resp.StatusCode, URL: url}
	}

	return resp.Body, nil
}

// HTTPError represents a non-200 response from the upstream proxy.
type HTTPError struct {
	StatusCode int
	URL        string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("upstream returned %d for %s", e.StatusCode, e.URL)
}
