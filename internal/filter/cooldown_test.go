package filter

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
		err   bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		{"1y", 365 * 24 * time.Hour, false},
		{"10y", 3650 * 24 * time.Hour, false},
		{"0s", 0, false},
		{"", 0, true},
		{"bad", 0, true},
		{"7x", 0, true},
		{"-1d", 0, true},
		{"-7d", 0, true},
		{"-30m", 0, true},
		{"-1h", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDuration(tt.input)
			if tt.err {
				if err == nil {
					t.Errorf("ParseDuration(%q) expected error, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseDuration(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestShouldExclude(t *testing.T) {
	f := NewCooldownFilter([]string{
		"github.com/mycompany/",
		"internal.corp/",
	})

	tests := []struct {
		module string
		want   bool
	}{
		{"github.com/mycompany/lib", true},
		{"github.com/mycompany/other/pkg", true},
		{"internal.corp/tools", true},
		{"github.com/other/lib", false},
		{"golang.org/x/text", false},
	}

	for _, tt := range tests {
		t.Run(tt.module, func(t *testing.T) {
			if got := f.ShouldExclude(tt.module); got != tt.want {
				t.Errorf("ShouldExclude(%q) = %v, want %v", tt.module, got, tt.want)
			}
		})
	}
}

func TestShouldExclude_EmptyList(t *testing.T) {
	f := NewCooldownFilter(nil)
	if f.ShouldExclude("anything") {
		t.Error("empty exclusion list should never exclude")
	}
}

func TestIsWithinCooldown(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	f := NewCooldownFilter(nil)
	f.Now = func() time.Time { return now }

	tests := []struct {
		name     string
		t        time.Time
		cooldown time.Duration
		want     bool
	}{
		{"within", now.Add(-3 * 24 * time.Hour), 7 * 24 * time.Hour, true},
		{"outside", now.Add(-10 * 24 * time.Hour), 7 * 24 * time.Hour, false},
		{"exact_boundary", now.Add(-7 * 24 * time.Hour), 7 * 24 * time.Hour, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.IsWithinCooldown(tt.t, tt.cooldown); got != tt.want {
				t.Errorf("IsWithinCooldown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterVersionList(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	f := NewCooldownFilter(nil)
	f.Now = func() time.Time { return now }

	cooldown := 7 * 24 * time.Hour

	timestamps := map[string]time.Time{
		"v1.0.0": now.Add(-30 * 24 * time.Hour), // 30 days ago - ok
		"v1.1.0": now.Add(-10 * 24 * time.Hour), // 10 days ago - ok
		"v1.2.0": now.Add(-3 * 24 * time.Hour),  // 3 days ago - filtered
		"v1.3.0": now.Add(-1 * 24 * time.Hour),  // 1 day ago - filtered
	}

	fetcher := func(ctx context.Context, version string) (time.Time, error) {
		t, ok := timestamps[version]
		if !ok {
			return time.Time{}, fmt.Errorf("unknown version %s", version)
		}
		return t, nil
	}

	versions := []string{"v1.0.0", "v1.1.0", "v1.2.0", "v1.3.0"}
	got, err := f.FilterVersionList(context.Background(), versions, cooldown, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"v1.0.0", "v1.1.0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, v := range got {
		if v != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, v, want[i])
		}
	}
}

func TestFilterVersionList_FailOpen(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	f := NewCooldownFilter(nil)
	f.Now = func() time.Time { return now }

	// fetcher that fails for v1.1.0
	fetcher := func(ctx context.Context, version string) (time.Time, error) {
		if version == "v1.1.0" {
			return time.Time{}, fmt.Errorf("upstream error")
		}
		return now.Add(-30 * 24 * time.Hour), nil
	}

	versions := []string{"v1.0.0", "v1.1.0"}
	got, err := f.FilterVersionList(context.Background(), versions, 7*24*time.Hour, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// v1.1.0 should be included because fetch failed (fail-open)
	if len(got) != 2 {
		t.Fatalf("expected 2 versions (fail-open), got %v", got)
	}
}

func TestFilterVersionList_Empty(t *testing.T) {
	f := NewCooldownFilter(nil)

	got, err := f.FilterVersionList(context.Background(), nil, 7*24*time.Hour, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}
}

func TestFilterVersionList_PreservesOrder(t *testing.T) {
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	f := NewCooldownFilter(nil)
	f.Now = func() time.Time { return now }

	fetcher := func(ctx context.Context, version string) (time.Time, error) {
		return now.Add(-30 * 24 * time.Hour), nil // all old enough
	}

	versions := []string{"v1.3.0", "v1.0.0", "v1.2.0", "v1.1.0"}
	got, err := f.FilterVersionList(context.Background(), versions, 7*24*time.Hour, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, v := range got {
		if v != versions[i] {
			t.Errorf("order changed: got[%d] = %q, want %q", i, v, versions[i])
		}
	}
}
