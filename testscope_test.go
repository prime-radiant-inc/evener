package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// makeVariable returns a Makefile variable's value, as make expands it, without
// running any target. The print rule arrives through a tiny wrapper makefile
// rather than `make --eval`, which GNU make only gained in 4.0 and so is absent
// from Apple's /usr/bin/make 3.81 (issue #3077). `include` and pattern rules
// predate 3.81, so the wrapper parses everywhere the Makefile does.
func makeVariable(t *testing.T, name string) []string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "print-var.mk")
	const rule = "include Makefile\n\nprint-var-%:\n\t@echo $($*)\n"
	if err := os.WriteFile(wrapper, []byte(rule), 0o600); err != nil {
		t.Fatalf("write wrapper makefile: %v", err)
	}
	out, err := exec.Command("make", "--no-print-directory", "-s", "-f", wrapper, "print-var-"+name).CombinedOutput()
	if err != nil {
		t.Fatalf("make print-var-%s: %v\n%s", name, err, out)
	}
	return strings.Fields(string(out))
}

// CI's tests job runs `make test` once per scope on separate runners (root and
// nonroot). Together they must cover every Go module exactly once: a module in
// neither would silently drop out of CI, and one in both would run twice.
func TestMakeTestScopesPartitionTheGoModules(t *testing.T) {
	modules := makeVariable(t, "GO_MODULES")
	root := makeVariable(t, "SCOPE_MODULES_root")
	nonroot := makeVariable(t, "SCOPE_MODULES_nonroot")
	if len(modules) == 0 || len(root) == 0 || len(nonroot) == 0 {
		t.Fatalf("empty module list: GO_MODULES=%q root=%q nonroot=%q", modules, root, nonroot)
	}
	both := append(slices.Clone(root), nonroot...)
	slices.Sort(both)
	want := slices.Clone(modules)
	slices.Sort(want)
	if !slices.Equal(both, want) {
		t.Fatalf("root %q + nonroot %q = %q, want each of GO_MODULES %q exactly once", root, nonroot, both, want)
	}
}

func TestMakeTestRefusesAnUnknownScope(t *testing.T) {
	out, err := exec.Command("make", "--no-print-directory", "test", "TEST_SCOPE=bogus").CombinedOutput()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("make test TEST_SCOPE=bogus: err = %v, want exit 2\n%s", err, out)
	}
	if !strings.Contains(string(out), "TEST_SCOPE") {
		t.Fatalf("refusal does not name TEST_SCOPE\n%s", out)
	}
}
