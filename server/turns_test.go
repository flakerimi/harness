package server

import (
	"context"
	"testing"
	"time"

	"github.com/flakerimi/harness/agent"
)

func TestTurnSurvivesSubscriberLoss(t *testing.T) {
	m := newTurnManager(4, time.Minute)
	release := make(chan struct{})
	err := m.Start("p/s", func(ctx context.Context, h agent.Handler) {
		h.OnText("one")
		<-release
		h.OnText("two")
		h.OnStop("end")
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, cancel, running := m.Subscribe("p/s", 0)
	if !running {
		t.Fatal("expected running turn")
	}
	cancel()       // client vanishes mid-turn
	close(release) // turn keeps going regardless

	waitIdle(t, m, "p/s")
	replay, evicted, live2, _, running2 := m.Subscribe("p/s", 1)
	if evicted || running2 || live2 != nil {
		t.Fatalf("finished turn: evicted=%v running=%v live=%v", evicted, running2, live2)
	}
	// frames after seq 1: text "two" + stop
	if len(replay) != 2 || replay[0].Event != "text" || replay[1].Event != "stop" {
		t.Fatalf("replay = %+v", replay)
	}
}

func TestSecondStartIsBusy(t *testing.T) {
	m := newTurnManager(4, time.Minute)
	release := make(chan struct{})
	_ = m.Start("p/s", func(ctx context.Context, h agent.Handler) { <-release })
	if err := m.Start("p/s", func(ctx context.Context, h agent.Handler) {}); err != errTurnBusy {
		t.Fatalf("err = %v, want errTurnBusy", err)
	}
	close(release)
	waitIdle(t, m, "p/s")
}

func TestConcurrencyCap(t *testing.T) {
	m := newTurnManager(1, time.Minute)
	release := make(chan struct{})
	_ = m.Start("p/a", func(ctx context.Context, h agent.Handler) { <-release })
	if err := m.Start("p/b", func(ctx context.Context, h agent.Handler) {}); err != errTurnCapacity {
		t.Fatalf("err = %v, want errTurnCapacity", err)
	}
	close(release)
	waitIdle(t, m, "p/a")
}

func TestLiveSubscriberSeesFrames(t *testing.T) {
	m := newTurnManager(4, time.Minute)
	release := make(chan struct{})
	_ = m.Start("p/s", func(ctx context.Context, h agent.Handler) {
		<-release
		h.OnText("hello")
		h.OnStop("end")
	})
	_, _, live, cancel, _ := m.Subscribe("p/s", 0)
	defer cancel()
	close(release)
	var got []frame
	for f := range live {
		got = append(got, f)
	}
	if len(got) != 2 || got[0].Event != "text" || got[1].Event != "stop" {
		t.Fatalf("live frames = %+v", got)
	}
}

// waitIdle polls until the key's turn has finished (test helper, 2s cap).
func waitIdle(t *testing.T, m *turnManager, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.Running(key); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("turn never finished")
}
