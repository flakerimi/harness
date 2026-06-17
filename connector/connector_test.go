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

// fakeConn is a non-native connector for testing namespacing.
type fakeConn struct{ tools []tool.Tool }

func (fakeConn) Name() string                                 { return "fake" }
func (fakeConn) Status(context.Context) Status                { return Status{Connected: true} }
func (f fakeConn) Tools(context.Context) ([]tool.Tool, error) { return f.tools, nil }

func TestExternalConnectorToolsAreNamespaced(t *testing.T) {
	r := NewRegistry()
	r.Add(NewNative("builtin", tool.ReadFile{}))         // native → plain name
	r.Add(fakeConn{tools: []tool.Tool{tool.ReadFile{}}}) // external → prefixed

	reg, err := r.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("read_file"); !ok {
		t.Error("native read_file should keep its plain name")
	}
	if _, ok := reg.Get("fake__read_file"); !ok {
		t.Error("external connector's read_file should be namespaced to fake__read_file")
	}
}
