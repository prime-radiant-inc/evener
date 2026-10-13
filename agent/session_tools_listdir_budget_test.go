package agent

// Tool-surface tests for the budgeted list_dir walk (#2375): the list_dir
// handler must stop discarding its context, drive the environment's budgeted
// walk, and render the walk's truncation honestly — no exhaustive total when
// the budget stopped the walk, the cap reported instead, and pages that stay
// consecutive slices of the one canonical listing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// listdirBudget_call runs one list_dir tool call over env with an explicit
// context, the way the session dispatches it.
func listdirBudget_call(ctx context.Context, t *testing.T, reg *toolpkg.Registry, env execenv.ExecutionEnvironment, args map[string]any) toolpkg.ExecResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	return reg.ExecuteCall(ctx, env, llm.ToolCallData{ID: "c1", Name: "list_dir", Arguments: raw})
}

// listdirBudget_flat builds a directory of six one-byte files, a..f.txt, so
// every page boundary and footer count is stable and no name carries a
// standalone digit.
func listdirBudget_flat(t *testing.T) (env *execenv.LocalExecutionEnvironment, root string) {
	t.Helper()
	root = t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return execenv.NewLocalExecutionEnvironment(root), root
}

// listdirBudget_body splits a rendered listing into its entry lines and its
// footer, the way formatDirListing assembles them (entries, blank line,
// footer).
func listdirBudget_body(out string) (lines []string, footer string) {
	body, footer, found := strings.Cut(out, "\n\n")
	if !found {
		return strings.Split(out, "\n"), ""
	}
	return strings.Split(body, "\n"), footer
}

// TestListDirToolSurfacesTheWalkBudgetNote pins the honest partial total: a
// page whose walk was stopped by the budget must not claim an exhaustive
// "of N" total it never computed — the model would read it as the subtree's
// size — and must say which cap the walk stopped at, the same TruncatedAt
// convention glob's budget reports through.
func TestListDirToolSurfacesTheWalkBudgetNote(t *testing.T) {
	t.Parallel()
	reg := w3sub_shellReg(t, nil)
	env, root := listdirBudget_flat(t)

	res := listdirBudget_call(t.Context(), t, reg, env, map[string]any{"path": root, "limit": 2.0})
	if res.IsError {
		t.Fatalf("list_dir over a six-entry directory errored: %s", res.Output)
	}
	_, footer := listdirBudget_body(res.Output)
	if !strings.Contains(footer, "list_dir(offset=2)") {
		t.Fatalf("footer must offer the next page:\n%s", footer)
	}
	if !descriptionMentions(footer, 3) {
		t.Fatalf("footer must report the walk's cap of 3 entries (page of 2 plus the one that proves more exist):\n%s", footer)
	}
	if descriptionMentions(footer, 6) {
		t.Fatalf("footer claimed an exhaustive total of 6 from a walk the budget stopped at 3:\n%s", footer)
	}
}

// TestListDirToolPagesCoherentlyUnderTheBudget pins the pagination contract
// under the budget: every page's entries are the consecutive slice of the one
// canonical listing — the same sequence an exhaustive walk produces — and a
// page whose walk completes reverts to the exact-total footer with no cap
// note.
func TestListDirToolPagesCoherentlyUnderTheBudget(t *testing.T) {
	t.Parallel()
	reg := w3sub_shellReg(t, nil)
	env, root := listdirBudget_flat(t)

	full := listdirBudget_call(t.Context(), t, reg, env, map[string]any{"path": root, "limit": 1000.0})
	if full.IsError {
		t.Fatalf("list_dir control page errored: %s", full.Output)
	}
	fullLines, _ := listdirBudget_body(full.Output)
	if len(fullLines) != 6 {
		t.Fatalf("control listing has %d entry lines, want 6:\n%s", len(fullLines), full.Output)
	}

	for i, page := range []struct{ offset, limit float64 }{{0, 2}, {2, 2}, {4, 2}} {
		res := listdirBudget_call(t.Context(), t, reg, env, map[string]any{"path": root, "offset": page.offset, "limit": page.limit})
		if res.IsError {
			t.Fatalf("page %d errored: %s", i, res.Output)
		}
		lines, footer := listdirBudget_body(res.Output)
		if !slices.Equal(lines, fullLines[int(page.offset):int(page.offset+page.limit)]) {
			t.Fatalf("page %d entries = %v, want the consecutive slice %v (footer: %s)", i, lines, fullLines[int(page.offset):int(page.offset+page.limit)], footer)
		}
		if i < 2 {
			if !descriptionMentions(footer, int(page.offset+page.limit)+1) {
				t.Fatalf("capped page %d must report the walk's cap of %d:\n%s", i, int(page.offset+page.limit)+1, footer)
			}
			continue
		}
		// The final page's walk completes, so its footer states the exhaustive
		// total of 6 — asserted as a count read, not footer wording — and no
		// cap to report: a wrong cap here would carry this page's own need
		// of 7.
		if !descriptionMentions(footer, 6) {
			t.Fatalf("completed page must state the exact total of 6:\n%s", footer)
		}
		if descriptionMentions(footer, 7) {
			t.Fatalf("completed page reported a walk cap it never hit:\n%s", footer)
		}
	}
}

// TestListDirToolClampsModelSuppliedDepth pins the depth bound through the
// tool: a model-supplied depth of 1000 lists exactly what the enforced
// maximum lists, and no more.
func TestListDirToolClampsModelSuppliedDepth(t *testing.T) {
	t.Parallel()
	reg := w3sub_shellReg(t, nil)
	root := t.TempDir()
	dir := root
	for i := 1; i <= 24; i++ {
		dir = filepath.Join(dir, "sub")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := execenv.NewLocalExecutionEnvironment(root)

	clamped, err := env.ListDirectory(root, execenv.MaxListDirDepth)
	if err != nil {
		t.Fatalf("legacy walk at the clamp: %v", err)
	}
	deep, err := env.ListDirectory(root, 24)
	if err != nil {
		t.Fatalf("legacy walk past the clamp: %v", err)
	}

	toolOut := listdirBudget_call(t.Context(), t, reg, env, map[string]any{"path": root, "depth": 1000.0})
	if toolOut.IsError {
		t.Fatalf("list_dir at depth 1000 errored: %s", toolOut.Output)
	}
	want := listdirBudget_call(t.Context(), t, reg, env, map[string]any{"path": root, "depth": float64(execenv.MaxListDirDepth)})
	if want.IsError {
		t.Fatalf("list_dir at the clamp errored: %s", want.Output)
	}
	if toolOut.Output != want.Output {
		t.Fatalf("depth 1000 listing differs from the depth-%d listing the clamp enforces:\n%s\n---\n%s", execenv.MaxListDirDepth, toolOut.Output, want.Output)
	}
	if !descriptionMentions(toolOut.Output, len(clamped)) || descriptionMentions(toolOut.Output, len(deep)) {
		t.Fatalf("depth 1000 must list the %d entries of the clamped walk, not the %d of a full descent:\n%s", len(clamped), len(deep), toolOut.Output)
	}
}

// TestListDirToolPassesCancellationThroughToTheWalk pins the ctx wiring the
// old handler discarded: a cancelled context must abort the walk and surface
// the cancellation as the tool call's error.
func TestListDirToolPassesCancellationThroughToTheWalk(t *testing.T) {
	t.Parallel()
	reg := w3sub_shellReg(t, nil)
	env, root := listdirBudget_flat(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res := listdirBudget_call(ctx, t, reg, env, map[string]any{"path": root})
	if !res.IsError || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("list_dir under a cancelled context = (isError=%t, err=%v), want the walk's context.Canceled", res.IsError, res.Err)
	}
}

// TestDefListDirDepthProseMatchesEnforcedClamp pins list_dir's advertised
// depth clamp to the constant the walk enforces: a model sizing a depth
// against the advertised maximum must not work from a wrong number, the same
// contract the limit prose pin holds.
func TestDefListDirDepthProseMatchesEnforcedClamp(t *testing.T) {
	t.Parallel()
	def := toolpkg.DefListDir()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("list_dir properties = %T, want map[string]any", def.Parameters["properties"])
	}
	depth, ok := props["depth"].(map[string]any)
	if !ok {
		t.Fatalf("list_dir missing depth property; got properties: %v", props)
	}
	desc, _ := depth["description"].(string)
	if !descriptionMentions(desc, execenv.MaxListDirDepth) {
		t.Errorf("list_dir depth description should state the enforced clamp of %d, got: %q", execenv.MaxListDirDepth, desc)
	}
}

// TestListDirToolStopsTheWalkAtTheRenderedCharacterBudget pins the tool-level
// half of the character contract: the walk the tool budgets must stop at the
// same rendered-size bound the footer's page enforces, so a page that fills
// the character budget well before the entry limit reports the walk's own
// stop position instead of walking on toward a page it will never render.
func TestListDirToolStopsTheWalkAtTheRenderedCharacterBudget(t *testing.T) {
	t.Parallel()
	reg := w3sub_shellReg(t, nil)
	root := t.TempDir()
	// Seventy 250-character names: each renders as 266 budgeted characters,
	// so the 16k-character page fits 60 entries and the walk stops on the
	// 61st as its lookahead.
	for i := 1; i <= 70; i++ {
		name := fmt.Sprintf("long%04d%s", i, strings.Repeat("x", 242))
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := execenv.NewLocalExecutionEnvironment(root)

	res := listdirBudget_call(t.Context(), t, reg, env, map[string]any{"path": root})
	if res.IsError {
		t.Fatalf("list_dir over long names errored: %s", res.Output)
	}
	_, footer := listdirBudget_body(res.Output)
	if !strings.Contains(footer, "list_dir(offset=60)") {
		t.Fatalf("footer must offer the next page where the rendered page cut:\n%s", footer)
	}
	if !descriptionMentions(footer, 61) {
		t.Fatalf("footer must report the walk's stop position of 61 (60 rendered entries plus the lookahead):\n%s", footer)
	}
	if descriptionMentions(footer, 70) {
		t.Fatalf("footer claimed the exhaustive total of 70 from a walk the character budget stopped at 61:\n%s", footer)
	}
}
