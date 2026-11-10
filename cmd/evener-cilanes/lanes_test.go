package cilanes

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The catalog names ci.yml's jobs and matrix cells by hand. A name that
// drifts fails safe (the lane runs), so this pins the names: each catalog
// job exists, reads its own name from the skip output, and each matrix lane
// is a cell of that job's matrix under the key the exclude output uses.
func TestCatalogMatchesTheWorkflow(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If       string `yaml:"if"`
			Strategy struct {
				Matrix map[string]any `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, l := range catalog {
		job, ok := workflow.Jobs[l.job]
		if !ok {
			t.Errorf("%s: no such job in ci.yml", l.id())
			continue
		}
		if !strings.Contains(job.If, "outputs.skip") || !strings.Contains(job.If, "'"+l.job+"'") {
			t.Errorf("%s: if %q does not read its own name from the skip output", l.id(), job.If)
		}
		if l.cell == "" {
			continue
		}
		key := matrixKeys[l.job]
		values, _ := job.Strategy.Matrix[key].([]any)
		if !slices.ContainsFunc(values, func(v any) bool { return v == l.cell }) {
			t.Errorf("%s: matrix %s = %v lacks the cell", l.id(), key, values)
		}
		exclude, _ := job.Strategy.Matrix["exclude"].(string)
		if !strings.Contains(exclude, "outputs.exclude_"+strings.ReplaceAll(l.job, "-", "_")) {
			t.Errorf("%s: matrix exclude %q does not read the job's exclude output", l.id(), exclude)
		}
	}
}
