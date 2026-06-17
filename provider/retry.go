package provider

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxAttempts bounds how many times a streaming request is tried before giving
// up (1 initial + retries).
const maxAttempts = 4

// httpDoRetry performs an HTTP request with exponential backoff on transient
// failures — network errors, HTTP 429, and 5xx. newReq must build a fresh
// request on each call, since the body is consumed per attempt. A Retry-After
// header is honored (capped). On the final attempt the response is returned
// as-is (even if 429/5xx) so the caller can read and report the error body.
func httpDoRetry(ctx context.Context, client *http.Client, newReq func() (*http.Request, error)) (*http.Response, error) {
	backoff := 500 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt == maxAttempts || ctx.Err() != nil {
				return nil, err
			}
			if !sleepCtx(ctx, backoff) {
				return nil, ctx.Err()
			}
			backoff *= 2
			continue
		}
		if isTransient(resp.StatusCode) && attempt < maxAttempts {
			wait := retryAfter(resp.Header.Get("Retry-After"), backoff)
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			_ = resp.Body.Close()
			if !sleepCtx(ctx, wait) {
				return nil, ctx.Err()
			}
			backoff *= 2
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

// isTransient reports whether an HTTP status is worth retrying.
func isTransient(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code <= 599)
}

// retryAfter parses a Retry-After header (delta-seconds form), capped at 30s,
// falling back to the backoff when absent or unparseable.
func retryAfter(header string, fallback time.Duration) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
		return min(time.Duration(secs)*time.Second, 30*time.Second)
	}
	return fallback
}

// sleepCtx waits d or until ctx is cancelled; it reports false on cancellation.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
