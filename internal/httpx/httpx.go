// Package httpx is a small HTTP client that retries transient failures with
// exponential backoff (cenkalti/backoff) and honors server rate-limit headers.
// Every outbound ingest request goes through it so retry/backoff behavior is
// consistent instead of re-implemented per client.
package httpx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// Defaults.
const (
	defaultPerAttemptTimeout = 45 * time.Second
	defaultMaxElapsed        = 5 * time.Minute // total budget across retries for one Do
	defaultMaxInterval       = 60 * time.Second
	defaultMaxServerWait     = 120 * time.Second // cap on an honored Retry-After
)

// Client wraps an *http.Client with retry + backoff + header respect.
type Client struct {
	http            *http.Client
	initialInterval time.Duration
	maxElapsed      time.Duration
	maxInterval     time.Duration
	maxServerWait   time.Duration
	logger          *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout sets the per-attempt HTTP timeout.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.http.Timeout = d } }

// WithMaxElapsed caps the total time (across retries) spent in one Do.
func WithMaxElapsed(d time.Duration) Option { return func(c *Client) { c.maxElapsed = d } }

// WithLogger logs each retry (attempt, status/error, wait).
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.logger = l } }

// WithBackoff overrides the initial and max backoff intervals (mainly for tests).
func WithBackoff(initial, max time.Duration) Option {
	return func(c *Client) { c.initialInterval, c.maxInterval = initial, max }
}

// New builds a Client.
func New(opts ...Option) *Client {
	c := &Client{
		http:            &http.Client{Timeout: defaultPerAttemptTimeout},
		initialInterval: 1 * time.Second,
		maxElapsed:      defaultMaxElapsed,
		maxInterval:     defaultMaxInterval,
		maxServerWait:   defaultMaxServerWait,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Do issues req, retrying transient failures (network errors, 408, 429, 5xx)
// with backoff and honoring Retry-After / RateLimit-Reset / X-RateLimit-Reset.
// It returns the first non-retryable response (2xx or a permanent 4xx) for the
// caller to handle, or an error once the budget is exhausted / ctx is done.
// Bodies of retried responses are drained and closed; the caller owns the body
// of the returned response.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = c.initialInterval
	bo.Multiplier = 2
	bo.RandomizationFactor = 0.3 // jitter
	bo.MaxInterval = c.maxInterval
	bo.MaxElapsedTime = c.maxElapsed
	bo.Reset()

	for attempt := 1; ; attempt++ {
		resp, err := c.http.Do(req.Clone(ctx))

		var serverWait time.Duration
		var reason string
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			reason = err.Error()
		case !retryableStatus(resp.StatusCode):
			return resp, nil
		default:
			serverWait = parseRetryAfter(resp.Header)
			reason = fmt.Sprintf("status %d", resp.StatusCode)
			drain(resp.Body)
		}

		wait := bo.NextBackOff()
		if wait == backoff.Stop {
			return nil, fmt.Errorf("httpx: %s after %d attempts: %s", req.URL.Host, attempt, reason)
		}
		if serverWait > 0 {
			if serverWait > c.maxServerWait {
				serverWait = c.maxServerWait
			}
			if serverWait > wait {
				wait = serverWait
			}
		}
		if c.logger != nil {
			c.logger.WarnContext(ctx, "httpx retry",
				slog.String("host", req.URL.Host), slog.Int("attempt", attempt),
				slog.String("reason", reason), slog.Duration("wait", wait))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// retryableStatus reports whether a status warrants a retry. 429 and 5xx are
// transient; other 4xx are the caller's problem (permanent).
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

// parseRetryAfter reads the server's requested wait from, in order: Retry-After
// (delay-seconds or HTTP-date), RateLimit-Reset (seconds), X-RateLimit-Reset
// (seconds, or a Unix epoch). Returns 0 if none present/parseable.
func parseRetryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	for _, key := range []string{"Ratelimit-Reset", "X-Ratelimit-Reset"} {
		v := h.Get(key)
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		if n > 1_000_000_000 { // looks like a Unix epoch (post-2001)
			if d := time.Until(time.Unix(n, 0)); d > 0 {
				return d
			}
			continue
		}
		if n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<16))
	_ = body.Close()
}
