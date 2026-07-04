package httpx

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newReq(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestDo_RetriesTransientThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 { // fail twice, then succeed
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(WithBackoff(2*time.Millisecond, 10*time.Millisecond))
	resp, err := c.Do(context.Background(), newReq(t, srv.URL))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (2 retries)", calls.Load())
	}
}

func TestDo_PermanentStatusNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(WithBackoff(2*time.Millisecond, 10*time.Millisecond))
	resp, err := c.Do(context.Background(), newReq(t, srv.URL))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 returned to caller", resp.StatusCode)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (404 must not retry)", calls.Load())
	}
}

func TestDo_HonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Tiny backoff, so any wait >= ~1s must come from Retry-After.
	c := New(WithBackoff(1*time.Millisecond, 5*time.Millisecond))
	start := time.Now()
	resp, err := c.Do(context.Background(), newReq(t, srv.URL))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("waited %v, expected ~1s from Retry-After", elapsed)
	}
}

func TestDo_ContextCancelStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // always fail
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	c := New(WithBackoff(20*time.Millisecond, 100*time.Millisecond), WithMaxElapsed(time.Hour))
	resp, err := c.Do(ctx, newReq(t, srv.URL))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error on context cancel")
	}
}

func TestDo_LogsRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := New(WithBackoff(2*time.Millisecond, 10*time.Millisecond), WithLogger(logger))
	resp, err := c.Do(context.Background(), newReq(t, srv.URL))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()

	out := buf.String()
	if !strings.Contains(out, "httpx retry") || !strings.Contains(out, "status 429") {
		t.Errorf("expected a retry log mentioning status 429, got: %q", out)
	}
}

func TestParseRetryAfter(t *testing.T) {
	epoch := time.Now().Add(30 * time.Second).Unix()
	cases := []struct {
		name   string
		header http.Header
		wantLo time.Duration
		wantHi time.Duration
	}{
		{"seconds", http.Header{"Retry-After": {"45"}}, 45 * time.Second, 45 * time.Second},
		{"http-date", http.Header{"Retry-After": {time.Now().Add(20 * time.Second).UTC().Format(http.TimeFormat)}}, 18 * time.Second, 21 * time.Second},
		{"ratelimit-reset-seconds", http.Header{"Ratelimit-Reset": {"12"}}, 12 * time.Second, 12 * time.Second},
		{"x-ratelimit-reset-epoch", http.Header{"X-Ratelimit-Reset": {fmt.Sprint(epoch)}}, 27 * time.Second, 31 * time.Second},
		{"none", http.Header{}, 0, 0},
	}
	for _, c := range cases {
		got := parseRetryAfter(c.header)
		if got < c.wantLo || got > c.wantHi {
			t.Errorf("%s: got %v, want [%v,%v]", c.name, got, c.wantLo, c.wantHi)
		}
	}
}
