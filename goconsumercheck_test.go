package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// consumerCheckScript is the check these tests drive; it lives with the other
// gate scripts so the documented recipe and the executable check cannot drift.
const consumerCheckScript = "scripts/gate/go-module-consumer-check.sh"

// writeConsumerCheckTree writes root-relative files into a test tree.
func writeConsumerCheckTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// writeConsumerCheckWork writes a go.work using the named module directories,
// all relative to root, and returns its path.
func writeConsumerCheckWork(t *testing.T, root string, uses ...string) string {
	t.Helper()
	var body strings.Builder
	body.WriteString("go 1.21.0\n\nuse (\n")
	for _, use := range uses {
		body.WriteString("\t./" + use + "\n")
	}
	body.WriteString(")\n")
	writeConsumerCheckTree(t, root, map[string]string{"go.work": body.String()})
	return filepath.Join(root, "go.work")
}

// runConsumerCheck runs the check with workfile as GOWORK and returns its
// combined output and exit status.
func runConsumerCheck(t *testing.T, workfile string, dirs ...string) (string, int) {
	t.Helper()
	args := append([]string{consumerCheckScript}, dirs...)
	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(), "GOWORK="+workfile, "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run %s: %v\n%s", consumerCheckScript, err, out)
	}
	return string(out), exit.ExitCode()
}

// writeConsumerCheckLibAndDep writes the complete lib-plus-dep fixture used by
// the passing and discovery cases.
func writeConsumerCheckLibAndDep(t *testing.T, root string) {
	t.Helper()
	writeConsumerCheckTree(t, root, map[string]string{
		"lib/go.mod":     "module example.com/lib\n\ngo 1.21.0\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep v0.0.0 => ../dep\n",
		"lib/lib.go":     "package lib\n\nimport _ \"example.com/dep/pkg\"\n",
		"dep/go.mod":     "module example.com/dep\n\ngo 1.21.0\n",
		"dep/pkg/pkg.go": "package pkg\n",
	})
}

// TestGoModuleConsumerCheck pins the check's contract (#4709): a consumer of one
// module catches a missing direct requirement and a missing transitive
// requirement, reports a consumer setup failure as setup rather than as a
// module defect, and does not cross into nested modules the way a
// `<module>/...` wildcard does.
func TestGoModuleConsumerCheck(t *testing.T) {
	t.Run("complete module and replaced dependency pass", func(t *testing.T) {
		root := t.TempDir()
		work := writeConsumerCheckWork(t, root, "lib", "dep")
		writeConsumerCheckLibAndDep(t, root)
		out, status := runConsumerCheck(t, work, filepath.Join(root, "lib"), filepath.Join(root, "dep"))
		if status != 0 {
			t.Fatalf("status = %d, want 0\n%s", status, out)
		}
		for _, want := range []string{"PASS  example.com/lib", "PASS  example.com/dep"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("missing direct requirement is a gap", func(t *testing.T) {
		root := t.TempDir()
		work := writeConsumerCheckWork(t, root, "libdirect", "dep")
		writeConsumerCheckTree(t, root, map[string]string{
			"libdirect/go.mod": "module example.com/libdirect\n\ngo 1.21.0\n",
			"libdirect/lib.go": "package libdirect\n\nimport _ \"example.com/dep/pkg\"\n",
			"dep/go.mod":       "module example.com/dep\n\ngo 1.21.0\n",
			"dep/pkg/pkg.go":   "package pkg\n",
		})
		out, status := runConsumerCheck(t, work, filepath.Join(root, "libdirect"), filepath.Join(root, "dep"))
		if status != 1 {
			t.Fatalf("status = %d, want 1 (missing requirement)\n%s", status, out)
		}
		for _, want := range []string{"missing module requirement", "example.com/dep"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("missing transitive requirement is a gap", func(t *testing.T) {
		root := t.TempDir()
		work := writeConsumerCheckWork(t, root, "lib", "mid", "leaf")
		writeConsumerCheckTree(t, root, map[string]string{
			"lib/go.mod":      "module example.com/lib\n\ngo 1.21.0\n\nrequire example.com/mid v0.0.0\n\nreplace example.com/mid v0.0.0 => ../mid\n",
			"lib/lib.go":      "package lib\n\nimport _ \"example.com/mid\"\n",
			"mid/go.mod":      "module example.com/mid\n\ngo 1.21.0\n",
			"mid/mid.go":      "package mid\n\nimport _ \"example.com/leaf/pkg\"\n",
			"leaf/go.mod":     "module example.com/leaf\n\ngo 1.21.0\n",
			"leaf/pkg/pkg.go": "package pkg\n",
		})
		out, status := runConsumerCheck(t, work, filepath.Join(root, "lib"), filepath.Join(root, "mid"), filepath.Join(root, "leaf"))
		if status != 1 {
			t.Fatalf("status = %d, want 1 (missing transitive requirement)\n%s", status, out)
		}
		for _, want := range []string{"missing module requirement", "example.com/leaf"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("missing consumer sums are setup, not a module defect", func(t *testing.T) {
		root := t.TempDir()
		work := writeConsumerCheckWork(t, root, "libext")
		writeConsumerCheckTree(t, root, map[string]string{
			"libext/go.mod": "module example.com/libext\n\ngo 1.21.0\n\nrequire gopkg.in/yaml.v3 v3.0.1\n",
			"libext/lib.go": "package libext\n\nimport _ \"gopkg.in/yaml.v3\"\n",
		})
		out, status := runConsumerCheck(t, work, filepath.Join(root, "libext"))
		if status != 2 {
			t.Fatalf("status = %d, want 2 (consumer setup)\n%s", status, out)
		}
		if !strings.Contains(out, "consumer setup") {
			t.Errorf("output does not name consumer setup:\n%s", out)
		}
		if strings.Contains(out, "missing module requirement") {
			t.Errorf("setup failure was reported as a module requirement:\n%s", out)
		}
	})

	t.Run("setup beside a gap is still setup", func(t *testing.T) {
		root := t.TempDir()
		work := writeConsumerCheckWork(t, root, "libboth", "dep")
		writeConsumerCheckTree(t, root, map[string]string{
			"libboth/go.mod": "module example.com/libboth\n\ngo 1.21.0\n\nrequire gopkg.in/yaml.v3 v3.0.1\n",
			"libboth/lib.go": "package libboth\n\nimport (\n\t_ \"example.com/dep/pkg\"\n\t_ \"gopkg.in/yaml.v3\"\n)\n",
			"dep/go.mod":     "module example.com/dep\n\ngo 1.21.0\n",
			"dep/pkg/pkg.go": "package pkg\n",
		})
		out, status := runConsumerCheck(t, work, filepath.Join(root, "libboth"), filepath.Join(root, "dep"))
		if status != 2 {
			t.Fatalf("status = %d, want 2 (setup wins over the gap)\n%s", status, out)
		}
		for _, want := range []string{"consumer setup", "some packages also looked absent"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "(missing module requirement)") {
			t.Errorf("setup run was reported as a missing requirement:\n%s", out)
		}
	})

	t.Run("nested module is not part of the parent module's packages", func(t *testing.T) {
		root := t.TempDir()
		work := writeConsumerCheckWork(t, root, "prefix", "prefix/sub")
		writeConsumerCheckTree(t, root, map[string]string{
			"prefix/go.mod":     "module example.com/prefix\n\ngo 1.21.0\n",
			"prefix/prefix.go":  "package prefix\n",
			"prefix/sub/go.mod": "module example.com/prefix/sub\n\ngo 1.21.0\n\nrequire example.com/absent v0.0.0\n",
			"prefix/sub/sub.go": "package sub\n\nimport _ \"example.com/absent/pkg\"\n",
		})
		out, status := runConsumerCheck(t, work, filepath.Join(root, "prefix"))
		if status != 0 {
			t.Fatalf("status = %d, want 0: a nested module's packages are not the parent's\n%s", status, out)
		}
		if !strings.Contains(out, "PASS  example.com/prefix") {
			t.Errorf("output lacks the parent module PASS:\n%s", out)
		}
	})

	t.Run("default discovery checks every workspace module", func(t *testing.T) {
		root := t.TempDir()
		work := writeConsumerCheckWork(t, root, "lib", "dep")
		writeConsumerCheckLibAndDep(t, root)
		out, status := runConsumerCheck(t, work)
		if status != 0 {
			t.Fatalf("status = %d, want 0\n%s", status, out)
		}
		for _, want := range []string{"PASS  example.com/lib", "PASS  example.com/dep"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("a directory outside the workspace is setup", func(t *testing.T) {
		root := t.TempDir()
		work := writeConsumerCheckWork(t, root, "lib")
		writeConsumerCheckTree(t, root, map[string]string{
			"lib/go.mod": "module example.com/lib\n\ngo 1.21.0\n",
			"lib/lib.go": "package lib\n",
			"out/go.mod": "module example.com/out\n\ngo 1.21.0\n",
			"out/out.go": "package out\n",
		})
		out, status := runConsumerCheck(t, work, filepath.Join(root, "out"))
		if status != 2 {
			t.Fatalf("status = %d, want 2 (cannot enumerate outside the workspace)\n%s", status, out)
		}
		if !strings.Contains(out, "consumer setup") {
			t.Errorf("output does not name consumer setup:\n%s", out)
		}
	})
}
