//go:build unix

package skill

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

// TestDiscoverSkipsAFIFOSkillSourceWithoutBlocking pins the live discovery
// path against a writerless FIFO named SKILL.md: discovery must refuse it
// before any read instead of blocking session init on its open. The bounded
// wait is the same tripwire shape as execenv's offArmReadOrFailFast; a
// regression that opens the source with a blocking open fails visibly rather
// than hanging the suite.
func TestDiscoverSkipsAFIFOSkillSourceWithoutBlocking(t *testing.T) {
	t.Parallel()
	extra := t.TempDir()
	dir := filepath.Join(extra, "probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "SKILL.md")
	if err := syscall.Mkfifo(source, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	type result struct{ catalog Catalog }
	done := make(chan result, 1)
	go func() {
		done <- result{Discover(execenv.NewLocalExecutionEnvironment(t.TempDir()), DiscoverOptions{ExtraDirs: []string{extra}})}
	}()
	var catalog Catalog
	select {
	case res := <-done:
		catalog = res.catalog
	case <-time.After(5 * time.Second):
		t.Fatalf("Discover did not return (a FIFO source was opened without O_NONBLOCK)")
	}
	if _, ok := catalog.Entries["probe"]; ok {
		t.Fatalf("Discover admitted a FIFO source at %s", source)
	}
	for _, d := range catalog.Diagnostics {
		if d.Category == "unreadable_source" && d.Source == source {
			return
		}
	}
	t.Fatalf("Discover reported no unreadable-source diagnostic for %s: %+v", source, catalog.Diagnostics)
}
