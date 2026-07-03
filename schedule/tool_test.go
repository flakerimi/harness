package schedule

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flakerimi/harness/tool"
)

func run(t *testing.T, tl tool.Tool, input map[string]any) tool.Result {
	t.Helper()
	raw, _ := json.Marshal(input)
	res, err := tl.Run(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("%s: %v", tl.Spec().Name, err)
	}
	return res
}

func TestScheduleToolsManageOwnDuties(t *testing.T) {
	store := NewStore(t.TempDir())
	tools := NewTools(store, "personal", "mock", "telegram:1")
	add, list, remove := tools[0], tools[1], tools[2]

	if !add.Spec().Writes || !remove.Spec().Writes || list.Spec().Writes {
		t.Error("add/remove are writes; list is not")
	}

	// Add a daily brief; it lands with the identity's profile + deliver.
	res := run(t, add, map[string]any{"id": "morning-brief", "spec": "daily 07:00", "prompt": "run the chief-of-staff skill"})
	if res.IsError || !strings.Contains(res.Content, "morning-brief") {
		t.Fatalf("add: %+v", res)
	}
	tasks, _ := store.Load()
	if len(tasks) != 1 || tasks[0].Profile != "personal" || tasks[0].Deliver != "telegram:1" || tasks[0].Provider != "mock" {
		t.Fatalf("stored = %+v", tasks)
	}

	// Duplicate id fails; bad spec fails.
	if res := run(t, add, map[string]any{"id": "morning-brief", "spec": "daily 07:00", "prompt": "x"}); !res.IsError {
		t.Error("duplicate id should error")
	}
	if res := run(t, add, map[string]any{"id": "x", "spec": "sometimes", "prompt": "x"}); !res.IsError {
		t.Error("bad spec should error")
	}

	// List shows own tasks only.
	store.Add(Task{ID: "biz-review", Profile: "business", Prompt: "other identity", Spec: "daily 09:00"}, time.Now())
	out := run(t, list, nil)
	if !strings.Contains(out.Content, "morning-brief") || strings.Contains(out.Content, "biz-review") {
		t.Errorf("list = %q", out.Content)
	}

	// Remove: own works, foreign refused, unknown errors.
	if res := run(t, remove, map[string]any{"id": "biz-review"}); !res.IsError || !strings.Contains(res.Content, "not yours") {
		t.Errorf("foreign remove = %+v", res)
	}
	if res := run(t, remove, map[string]any{"id": "morning-brief"}); res.IsError {
		t.Errorf("own remove = %+v", res)
	}
	if res := run(t, remove, map[string]any{"id": "gone"}); !res.IsError {
		t.Error("unknown id should error")
	}
}
