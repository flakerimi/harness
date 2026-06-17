package connector

import (
	"context"
	"testing"

	"github.com/flakerimi/harness/tool"
)

func TestRegistryAggregatesTools(t *testing.T) {
	r := NewRegistry()
	r.Add(NewNative("builtin", tool.ReadFile{}, tool.WebFetch{}))

	reg, err := r.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("read_file"); !ok {
		t.Error("read_file not aggregated")
	}
	if _, ok := reg.Get("web_fetch"); !ok {
		t.Error("web_fetch not aggregated")
	}
}

func TestNativeStatusConnected(t *testing.T) {
	n := NewNative("builtin", tool.ReadFile{})
	if st := n.Status(context.Background()); !st.Connected {
		t.Errorf("native connector should report Connected, got %+v", st)
	}
}
