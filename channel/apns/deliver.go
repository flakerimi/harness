package apns

import (
	"context"
	"fmt"
	"strings"
)

// Deliverer alerts every device registered for an identity; dest is the
// profile name, and DataDir maps it to the directory holding the token store.
// The APNs client is built from env at delivery time so a long-lived daemon
// picks up rotated credentials without a restart. Dead tokens (wiped phone,
// reinstalled app) are pruned rather than failing the delivery; the remaining
// devices still get the alert.
type Deliverer struct {
	DataDir func(profile string) string
}

func (d Deliverer) Deliver(ctx context.Context, dest, text string) error {
	client, err := FromEnv()
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("push delivery needs APNS_KEY_B64/APNS_KEY_FILE + APNS_KEY_ID + APNS_TEAM_ID + APNS_TOPIC")
	}
	store := NewTokenStore(d.DataDir(dest))
	tokens := store.List()
	if len(tokens) == 0 {
		return fmt.Errorf("push: no devices registered for %q", dest)
	}
	var firstErr error
	for _, t := range tokens {
		err := client.Push(ctx, t.Token, "", text)
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), "Unregistered") || strings.Contains(err.Error(), "BadDeviceToken") {
			_ = store.Remove(t.Token)
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
