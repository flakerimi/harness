package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/flakerimi/harness/tool"
)

// writePlugin drops an executable shell-script plugin into dir.
func writePlugin(t *testing.T, dir, file, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin fixtures need a POSIX sh")
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const fixture = `
case "$1" in
  spec)
    echo '{"name":"testplug","description":"test plugin","tools":[{"name":"greet","description":"greets","input_schema":{"type":"object","properties":{"who":{"type":"string"}}},"writes":false},{"name":"zap","description":"mutates","writes":true}],"delivers":["echofile"]}'
    ;;
  run)
    if [ "$2" = "greet" ]; then
      input=$(cat)
      echo "hello from plugin: $input (root=$HARNESS_ROOT)"
    else
      echo "boom" >&2
      exit 1
    fi
    ;;
  deliver)
    cat > "$3"
    ;;
esac
`

func TestDiscoverRunDeliver(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "testplug", fixture)
	// A non-executable file is ignored, not an error.
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("docs"), 0o644)

	plugs, errs := Discover(context.Background(), dir, filepath.Join(dir, "does-not-exist"))
	if len(errs) != 0 {
		t.Fatalf("discover errs: %v", errs)
	}
	if len(plugs) != 1 || plugs[0].Manifest.Name != "testplug" {
		t.Fatalf("plugs = %+v", plugs)
	}

	// Tools arrive with schema + writes flag intact.
	conn := New(plugs[0])
	tools, err := conn.Tools(context.Background())
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools = %v, %v", tools, err)
	}
	byName := map[string]tool.Tool{}
	for _, tl := range tools {
		byName[tl.Spec().Name] = tl
	}
	if !byName["zap"].Spec().Writes || byName["greet"].Spec().Writes {
		t.Error("writes flags should mirror the manifest")
	}

	// run: stdin JSON in, stdout out, env vars carry the mediated Env.
	in, _ := json.Marshal(map[string]string{"who": "flak"})
	res, err := byName["greet"].Run(context.Background(), in, &tool.Env{Root: "/sandbox"})
	if err != nil || res.IsError {
		t.Fatalf("greet: %v %s", err, res.Content)
	}
	if !strings.Contains(res.Content, `"who":"flak"`) || !strings.Contains(res.Content, "root=/sandbox") {
		t.Errorf("greet result = %q", res.Content)
	}

	// A failing tool surfaces stderr as the error result.
	res, err = byName["zap"].Run(context.Background(), []byte(`{}`), nil)
	if err != nil || !res.IsError || !strings.Contains(res.Content, "boom") {
		t.Errorf("zap = %v %+v", err, res)
	}

	// deliver: kind routed to the advertising plugin, text on stdin.
	p, ok := FindDeliverer(plugs, "echofile")
	if !ok {
		t.Fatal("echofile deliverer not found")
	}
	dest := filepath.Join(dir, "delivered.txt")
	if err := p.Deliver(context.Background(), "echofile", dest, "message in a bottle\n"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dest)
	if !strings.Contains(string(data), "message in a bottle") {
		t.Errorf("delivered = %q", data)
	}
	if _, ok := FindDeliverer(plugs, "sms"); ok {
		t.Error("unadvertised kind must not match")
	}
}

func TestDiscoverDupesAndBrokenManifests(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writePlugin(t, first, "dupe", fixture)
	writePlugin(t, second, "dupe2", fixture) // same manifest name "testplug"
	writePlugin(t, second, "broken", `echo "not json"`)

	plugs, errs := Discover(context.Background(), first, second)
	if len(plugs) != 1 {
		t.Errorf("first dir should win the name: %+v", plugs)
	}
	if len(errs) != 2 {
		t.Errorf("dupe + broken should each warn: %v", errs)
	}
}
