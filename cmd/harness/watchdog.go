package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// egressWatch probes outbound connectivity on an interval and answers the
// server's Ready hook. The failure mode it exists for: a container whose NAT
// wedges — inbound traffic (health checks) keeps working while every outbound
// dial times out, so the daemon looks alive while its Telegram poller and
// model calls are dead. With this watch, /healthz turns 503 after the
// threshold and the platform's health monitor restarts the container.
type egressWatch struct {
	target    string
	threshold int

	mu          sync.Mutex
	consecutive int
	lastErr     error
}

func newEgressWatch(target string, threshold int) *egressWatch {
	return &egressWatch{target: target, threshold: threshold}
}

// run probes until ctx ends. State transitions are logged; steady states are
// silent.
func (w *egressWatch) run(ctx context.Context, interval time.Duration) {
	client := &http.Client{Timeout: 10 * time.Second}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, w.target, nil)
		if err == nil {
			var resp *http.Response
			resp, err = client.Do(req)
			if resp != nil {
				resp.Body.Close() // any HTTP response proves egress; status is irrelevant
			}
		}

		w.mu.Lock()
		was := w.consecutive
		if err != nil && ctx.Err() == nil {
			w.consecutive++
			w.lastErr = err
			if w.consecutive == w.threshold {
				fmt.Fprintf(os.Stderr, "watchdog: egress DOWN (%d consecutive probe failures, last: %v) — reporting unhealthy\n", w.consecutive, err)
			}
		} else if err == nil {
			if was >= w.threshold {
				fmt.Fprintln(os.Stderr, "watchdog: egress restored")
			}
			w.consecutive = 0
			w.lastErr = nil
		}
		w.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Ready reports the current verdict for /healthz: an error once the failure
// threshold is reached, nil otherwise.
func (w *egressWatch) Ready() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.consecutive >= w.threshold {
		return fmt.Errorf("egress dead: %d consecutive probe failures (last: %v)", w.consecutive, w.lastErr)
	}
	return nil
}
