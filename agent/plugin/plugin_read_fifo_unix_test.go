//go:build unix

package plugin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
)

// fifoDeadline bounds every probe below. A loader read that opens a
// writerless FIFO with a blocking open parks the whole load there forever, so
// each probe runs in its own goroutine and fails this tripwire instead of
// hanging the suite. The shape matches the FIFO tripwires in internal/plugins,
// agent/command, and agent/skill.
const fifoDeadline = 5 * time.Second

// withinFIFODeadline runs call in its own goroutine and fails the test if it
// has not returned within fifoDeadline, naming what blocked.
func withinFIFODeadline[T any](t *testing.T, what string, call func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- call() }()
	select {
	case result := <-done:
		return result
	case <-time.After(fifoDeadline):
		t.Fatalf("%s did not return (a FIFO was opened with a blocking open)", what)
		var zero T
		return zero
	}
}

// mkfifoAt creates a writerless named pipe at path.
func mkfifoAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}
}

// TestLoadRefusesFIFOPluginJSONWithoutBlocking pins the manifest read: a
// writerless FIFO where plugin.json should be must fail the load, not park it.
func TestLoadRefusesFIFOPluginJSONWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	mkfifoAt(t, filepath.Join(dir, ".claude-plugin", "plugin.json"))

	err := withinFIFODeadline(t, "Load(FIFO plugin.json)", func() error {
		_, loadErr := Load(dir)
		return loadErr
	})
	if err == nil {
		t.Fatal("a FIFO plugin.json loaded")
	}
}

// TestManifestNameRefusesFIFOPluginJSONWithoutBlocking pins the same read on
// the batch path LoadAllFailSoft uses to select manifests.
func TestManifestNameRefusesFIFOPluginJSONWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	mkfifoAt(t, filepath.Join(dir, ".claude-plugin", "plugin.json"))

	err := withinFIFODeadline(t, "ManifestName(FIFO plugin.json)", func() error {
		_, nameErr := ManifestName(dir)
		return nameErr
	})
	if err == nil {
		t.Fatal("a FIFO plugin.json named a plugin")
	}
}

// TestLoadRefusesFIFOHooksJSONWithoutBlocking pins the default hooks.json
// read: the load must report the unreadable hooks file instead of parking.
func TestLoadRefusesFIFOHooksJSONWithoutBlocking(t *testing.T) {
	dir := makePluginDir(t, "fifo-hooks")
	mkfifoAt(t, filepath.Join(dir, "hooks", "hooks.json"))

	err := withinFIFODeadline(t, "Load(FIFO hooks.json)", func() error {
		_, loadErr := Load(dir)
		return loadErr
	})
	if err == nil {
		t.Fatal("a FIFO hooks.json loaded")
	}
}

// TestLoadRefusesFIFOHooksFileDeclaredInManifestWithoutBlocking pins the
// manifest-declared hooks-file read, which takes the same seam.
func TestLoadRefusesFIFOHooksFileDeclaredInManifestWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	metaDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"fifo-declared-hooks","hooks":"./hooks/custom.json"}`
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	mkfifoAt(t, filepath.Join(dir, "hooks", "custom.json"))

	err := withinFIFODeadline(t, "Load(FIFO declared hooks file)", func() error {
		_, loadErr := Load(dir)
		return loadErr
	})
	if err == nil {
		t.Fatal("a FIFO manifest-declared hooks file loaded")
	}
}

// TestLoadRefusesFIFOMCPJSONWithoutBlocking pins the .mcp.json read: the file
// layer degrades to a warning, so the load must finish, with the warning.
func TestLoadRefusesFIFOMCPJSONWithoutBlocking(t *testing.T) {
	dir := makePluginDir(t, "fifo-mcp")
	mkfifoAt(t, filepath.Join(dir, ".mcp.json"))

	lp := withinFIFODeadline(t, "Load(FIFO .mcp.json)", func() Instance {
		inst, _ := Load(dir)
		return inst
	})
	if len(lp.MCPConfigWarnings) == 0 {
		t.Fatal("a FIFO .mcp.json produced no MCP config warning")
	}
}

// TestScanEvenerwideDirRefusesFIFOWithoutBlocking pins the evener-wide command
// component read. It runs from the same session-init path as the plugin
// loaders, so a FIFO command file must be skipped with a warning, not park the
// scan.
func TestScanEvenerwideDirRefusesFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	mkfifoAt(t, filepath.Join(dir, "pipe.md"))

	out := map[string]Command{}
	var warnings []events.WarningData
	withinFIFODeadline(t, "scanEvenerwideDir(FIFO command)", func() struct{} {
		scanEvenerwideDir(dir, "project", out, &warnings)
		return struct{}{}
	})
	if len(out) != 0 {
		t.Fatalf("scanEvenerwideDir parsed a FIFO command: %v", out)
	}
	if len(warnings) == 0 {
		t.Fatal("scanEvenerwideDir reported no unreadable-command warning for a FIFO")
	}
}

// TestLoadRefusesFIFOCommandComponentWithoutBlocking pins a component-file
// read: the FIFO command is skipped while the plugin's other commands load.
func TestLoadRefusesFIFOCommandComponentWithoutBlocking(t *testing.T) {
	dir := makePluginDir(t, "fifo-command")
	writeAgentFile(t, dir, "commands", "good.md", "run it\n")
	mkfifoAt(t, filepath.Join(dir, "commands", "pipe.md"))

	lp := withinFIFODeadline(t, "Load(FIFO command component)", func() Instance {
		inst, _ := Load(dir)
		return inst
	})
	if _, err := ResolveCommand(lp.Commands, "fifo-command:good"); err != nil {
		t.Fatalf("healthy command did not load: %v", err)
	}
	if len(lp.SkippedComponents) != 1 || filepath.Base(lp.SkippedComponents[0].File) != "pipe.md" {
		t.Fatalf("SkippedComponents = %v, want the FIFO command", lp.SkippedComponents)
	}
}

// TestLoadRefusesSymlinkedFIFOManifestWithoutBlocking pins the same-handle
// admission: a plugin.json symlink resolving to a writerless FIFO is refused.
func TestLoadRefusesSymlinkedFIFOManifestWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	mkfifoAt(t, filepath.Join(dir, "manifest-pipe"))
	metaDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "manifest-pipe"), filepath.Join(metaDir, "plugin.json")); err != nil {
		t.Fatal(err)
	}

	err := withinFIFODeadline(t, "Load(symlink to FIFO plugin.json)", func() error {
		_, loadErr := Load(dir)
		return loadErr
	})
	if err == nil {
		t.Fatal("a symlink to a FIFO plugin.json loaded")
	}
}
