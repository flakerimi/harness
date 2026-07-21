package app

import (
	"context"
	"fmt"
	"os"

	"github.com/flakerimi/harness/channel"
	"github.com/flakerimi/harness/channel/apns"
	"github.com/flakerimi/harness/channel/telegram"
	"github.com/flakerimi/harness/channel/webhook"
	"github.com/flakerimi/harness/connector/plugin"
	"github.com/flakerimi/harness/inbox"
	"github.com/flakerimi/harness/profile"
)

// Deliverers is the built-in channel registry every surface shares: telegram,
// webhook, push (apns), and app — the identity's own app, which persists to
// its inbox (the durable record) then announces with a best-effort push
// banner. Exec plugins are the fallback for kinds the binary doesn't know;
// discovery runs per delivery so a dropped plugin works without a restart.
func Deliverers() *channel.Registry {
	push := apns.Deliverer{DataDir: profile.DataDir}
	r := channel.NewRegistry()
	r.Register("telegram", telegram.Deliverer{})
	r.Register("webhook", webhook.Deliverer{})
	r.Register("push", push)
	r.Register("apns", push)
	r.Register("app", channel.DelivererFunc(func(ctx context.Context, dest, text string) error {
		if _, err := inbox.NewStore(profile.DataDir(dest)).Add(text); err != nil {
			return fmt.Errorf("app inbox: %w", err)
		}
		if err := push.Deliver(ctx, dest, text); err != nil {
			fmt.Fprintf(os.Stderr, "deliver: app:%s stored, push banner failed: %v\n", dest, err)
		}
		return nil
	}))
	r.Fallback = func(ctx context.Context, kind, dest, text string) (bool, error) {
		plugs, _ := plugin.Discover(ctx, PluginDirs("")...)
		if p, ok := plugin.FindDeliverer(plugs, kind); ok {
			return true, p.Deliver(ctx, kind, dest, text)
		}
		return false, nil
	}
	return r
}
