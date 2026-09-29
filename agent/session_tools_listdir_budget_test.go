package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// A list_dir page must bound the walk, not only the render: asking for a
// one-entry page over a directory the walk could enumerate whole must stop the
// traversal at the page instead of reading every entry and reporting an exact
// total (SAFE-02). The footer then has to report the partial page honestly
// rather than claim a total the walk never counted.
func TestListDirBudget_BoundsWalkAtPage(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)
	reg := w3sub_shellReg(t, nil)

	res := w3sub_call(t, reg, env, "list_dir", map[string]any{"path": "", "limit": 1.0})
	if res.IsError {
		t.Fatalf("list_dir(limit=1) errored: %q", res.Output)
	}
	if strings.Contains(res.Output, "of 5 entries") {
		t.Fatalf("list_dir enumerated the whole directory to report an exact total instead of stopping at the page:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "f00") || !strings.Contains(res.Output, "list_dir(offset=1)") {
		t.Fatalf("expected the first entry and a partial-page footer:\n%s", res.Output)
	}

	// Paging still advances through the same ordering.
	res = w3sub_call(t, reg, env, "list_dir", map[string]any{"path": "", "offset": 1.0, "limit": 1.0})
	if res.IsError || !strings.Contains(res.Output, "f01") {
		t.Fatalf("second page did not return f01: err=%t output=%q", res.IsError, res.Output)
	}
}
