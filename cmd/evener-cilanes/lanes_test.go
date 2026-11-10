package cilanes

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The catalog names ci.yml's jobs and matrix cells by hand, so this pins
// them: each catalog job exists and reads its own name from the skip
// output, each matrix lane is a cell of that job's matrix under the key the
// exclude output uses, and each cell of a catalogued job is catalogued. A
// cell the catalog missed would be skipped with the rest of its job, never
// having been planned.
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
	for name, job := range workflow.Jobs {
		// A job catalogued as one lane (web-browser) runs or skips whole.
		if _, perCell := matrixKeys[name]; !perCell {
			continue
		}
		for key, values := range job.Strategy.Matrix {
			cells, ok := values.([]any)
			if !ok {
				continue
			}
			if key != matrixKeys[name] {
				t.Errorf("%s: matrix key %q is not the catalog's %q", name, key, matrixKeys[name])
				continue
			}
			for _, cell := range cells {
				if !slices.ContainsFunc(catalog, func(l lane) bool { return l.job == name && l.cell == cell }) {
					t.Errorf("%s: matrix cell %v is not in the catalog", name, cell)
				}
			}
		}
	}
}
