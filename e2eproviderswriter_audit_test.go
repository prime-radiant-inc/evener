package evener_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// e2eProvidersWriterDir is the only place an e2e harness may spell the
// providers.toml schema. Its Render function is the one writer; shell
// harnesses call the built test/e2e/providers/cmd binary.
const e2eProvidersWriterDir = "test/e2e/providers"

// TestE2EProvidersTOMLHasOneWriter keeps the e2e harnesses' providers.toml
// schema in one place (#4445). Three harnesses once hand-wrote the same block,
// each in its own way, so a registry schema change could leave one behind with
// no test noticing (#4411, #4437). A harness may point the writer at a fake
// backend, but may not grow another copy of the schema.
func TestE2EProvidersTOMLHasOneWriter(t *testing.T) {
	t.Parallel()
	var offenders []string
	for _, root := range []string{"scripts/e2e", "test/e2e"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path == e2eProvidersWriterDir || d.Name() == "testdata" {
					return fs.SkipDir
				}
				return nil
			}
			switch filepath.Ext(path) {
			case ".sh", ".toml", ".go":
			default:
				return nil
			}
			isShell := filepath.Ext(path) == ".sh"
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for i, line := range strings.Split(string(body), "\n") {
				trimmed := strings.TrimSpace(line)
				// A comment may name the schema; only code may write it.
				if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
					continue
				}
				declaresSchema := strings.Contains(trimmed, "[providers.") ||
					strings.Contains(trimmed, "[instances.")
				// A shell redirect into a providers.toml path is a writer even
				// if its schema is generated. A Go harness is caught by the
				// schema literal above; its WriteFile of the writer's own
				// output is the intended call, not a copy.
				writesConfig := isShell &&
					strings.Contains(trimmed, "providers.toml") &&
					strings.Contains(trimmed, ">")
				if !declaresSchema && !writesConfig {
					continue
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, trimmed))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Strings(offenders)
	for _, offender := range offenders {
		t.Errorf("%s writes the providers.toml schema by hand; call the one writer in %s "+
			"(Render, or its cmd binary from a shell harness) so a schema change reaches every harness at once",
			offender, e2eProvidersWriterDir)
	}
}
