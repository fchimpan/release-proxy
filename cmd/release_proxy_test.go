package cmd

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"
)

func TestParseExclusions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "github.com/foo/", []string{"github.com/foo/"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"trim_spaces", " a , b ", []string{"a", "b"}},
		{"skip_empty_segments", ",,a,,", []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseExclusions(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseExclusions(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	tests := []struct {
		name       string
		value      string
		defaultVal time.Duration
		want       time.Duration
	}{
		{"empty_uses_default", "", time.Hour, time.Hour},
		{"valid_minutes", "5m", time.Hour, 5 * time.Minute},
		{"valid_days", "7d", 0, 7 * 24 * time.Hour},
		{"invalid_uses_default", "garbage", time.Hour, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDuration(context.Background(), "TEST_KEY", tt.value, tt.defaultVal, logger)
			if got != tt.want {
				t.Errorf("parseDuration(value=%q, default=%v) = %v, want %v", tt.value, tt.defaultVal, got, tt.want)
			}
		})
	}
}

func TestRun_GracefulShutdown(t *testing.T) {
	getenv := stubEnv(map[string]string{
		"RELEASE_PROXY_PORT":      "0",
		"RELEASE_PROXY_LOG_LEVEL": "error",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: Run must still bind, enter the select, see ctx.Done, and shutdown cleanly.

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, getenv, io.Discard)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run() did not return within timeout")
	}
}

func TestRun_ListenError(t *testing.T) {
	getenv := stubEnv(map[string]string{
		"RELEASE_PROXY_PORT":      "not-a-port",
		"RELEASE_PROXY_LOG_LEVEL": "error",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, getenv, io.Discard)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from invalid port, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within timeout")
	}
}

func stubEnv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}
