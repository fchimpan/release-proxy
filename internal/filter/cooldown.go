package filter

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CooldownFilter filters module versions based on their release age.
type CooldownFilter struct {
	Exclusions []string
	Now        func() time.Time
}

// NewCooldownFilter creates a CooldownFilter with the given exclusion prefixes.
func NewCooldownFilter(exclusions []string) *CooldownFilter {
	return &CooldownFilter{
		Exclusions: exclusions,
		Now:        time.Now,
	}
}

// ShouldExclude returns true if the module path matches any exclusion prefix.
func (f *CooldownFilter) ShouldExclude(modulePath string) bool {
	for _, prefix := range f.Exclusions {
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(modulePath, prefix) {
			return true
		}
	}
	return false
}

// InfoFetcher resolves the release timestamp for a given version.
type InfoFetcher func(ctx context.Context, version string) (time.Time, error)

// FilterVersionList returns only versions whose release time is older than the cooldown.
// Versions whose timestamp cannot be resolved are included (fail-open).
func (f *CooldownFilter) FilterVersionList(ctx context.Context, versions []string, cooldown time.Duration, fetch InfoFetcher) ([]string, error) {
	if len(versions) == 0 {
		return nil, nil
	}

	cutoff := f.Now().Add(-cooldown)
	filtered := make([]string, 0, len(versions))

	type result struct {
		version string
		t       time.Time
		err     error
	}

	ch := make(chan result, len(versions))
	sem := make(chan struct{}, 10) // bounded concurrency

	for _, v := range versions {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		go func(ver string) {
			defer func() { <-sem }()
			t, err := fetch(ctx, ver)
			ch <- result{version: ver, t: t, err: err}
		}(v)
	}

	// collect results, preserving original order
	results := make(map[string]result, len(versions))
	for range versions {
		select {
		case r := <-ch:
			results[r.version] = r
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	for _, v := range versions {
		r, ok := results[v]
		if !ok || r.err != nil {
			// fail-open: include versions we can't check
			filtered = append(filtered, v)
			continue
		}
		if !r.t.After(cutoff) {
			filtered = append(filtered, v)
		}
	}

	return filtered, nil
}

// IsWithinCooldown returns true if the given time is within the cooldown window.
func (f *CooldownFilter) IsWithinCooldown(t time.Time, cooldown time.Duration) bool {
	cutoff := f.Now().Add(-cooldown)
	return t.After(cutoff)
}

// ParseDuration parses a non-negative cooldown duration string.
// Supports: "30m" (minutes), "24h" (hours), "7d" (days), "2w" (weeks), "1y" (years; 365d).
// Combined forms like "1d12h" are not supported; use a single unit.
// Negative durations are rejected — they would silently disable filtering.
func ParseDuration(s string) (time.Duration, error) {
	d, err := parseDurationRaw(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("negative duration %q not allowed", s)
	}
	return d, nil
}

func parseDurationRaw(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	// Try standard time.ParseDuration first (handles h, m, s, etc.)
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Handle custom suffixes: d (days), w (weeks), y (years)
	suffix := s[len(s)-1]
	numStr := s[:len(s)-1]

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}

	switch suffix {
	case 'd':
		return time.Duration(num * float64(24*time.Hour)), nil
	case 'w':
		return time.Duration(num * float64(7*24*time.Hour)), nil
	case 'y':
		return time.Duration(num * float64(365*24*time.Hour)), nil
	default:
		return 0, fmt.Errorf("unsupported duration unit %q in %q", string(suffix), s)
	}
}
