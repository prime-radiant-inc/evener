# Content Pane Max Width Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cap the Serf hub workspace content column at a sensible width and center it on wide screens.

**Architecture:** Keep `#workspace` as the app-shell flex item and apply the max-width behavior to its existing child content sections. Add one named CSS custom property on `#workspace` and a shared width/margin rule for `.workspace-header`, `.conversation`, and `.workspace-input`.

**Tech Stack:** Go `html/template` embedded assets, CSS in `cmd/serf-hub/assets/style.css`, Go tests via `go test ./cmd/serf-hub`.

## Global Constraints

- Do not cap `#workspace`; it must keep filling the app-shell flex area between sidebar and side panes.
- The sidebar, sidebar resizer, pane splitter, and side panes must keep their existing layout behavior.
- The content column must remain fluid below the cap and centered above the cap.
- The standalone thread document should inherit the same behavior through the shared workspace partial and stylesheet.
- Do not redesign message bubbles, typography, composer controls, or make the width user-configurable.
- Before changing tests, follow `docs/testing.md`: default tests must be deterministic and must not depend on provider credentials, network access, quota, current model behavior, or ambient developer machine state.

---

## File Structure

- Modify `cmd/serf-hub/assets/style.css`: define `--workspace-content-max-w` on `#workspace` and apply shared `width: min(100%, var(--workspace-content-max-w))` plus `margin-inline: auto` to `.workspace-header`, `.conversation`, and `.workspace-input`.
- Modify `cmd/serf-hub/web_test.go`: add a deterministic static asset contract test that reads the embedded CSS asset and asserts the cap/centering rules exist. This test is intentionally narrow because the behavior is CSS-only and there is no browser layout test harness in the default suite.

---

### Task 1: CSS content-column cap

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/web_test.go`

**Interfaces:**
- Consumes: existing `#workspace`, `.workspace-header`, `.conversation`, and `.workspace-input` selectors.
- Produces: CSS contract using custom property `--workspace-content-max-w` and centered content sections.

- [ ] **Step 1: Write the failing test**

Add this test near the other web rendering/static tests in `cmd/serf-hub/web_test.go`:

```go
func TestWebWorkspaceContentColumnCSSContract(t *testing.T) {
	data, err := assetsFS.ReadFile("assets/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(data)

	checks := []string{
		"--workspace-content-max-w: 832px;",
		".workspace-header,\n.conversation,\n.workspace-input",
		"width: min(100%, var(--workspace-content-max-w));",
		"margin-inline: auto;",
	}
	for _, want := range checks {
		if !strings.Contains(css, want) {
			t.Fatalf("style.css missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./cmd/serf-hub -run TestWebWorkspaceContentColumnCSSContract -count=1
```

Expected: FAIL with a message like `style.css missing "--workspace-content-max-w: 832px;"`.

- [ ] **Step 3: Add the CSS custom property and shared rule**

In `cmd/serf-hub/assets/style.css`, replace the existing single-line `#workspace` rule around the app shell section:

```css
#workspace { position: relative; flex: 1; min-width: 0; height: 100vh; display: flex; flex-direction: column; overflow: hidden; }
```

with this expanded rule and shared content-column rule immediately after it:

```css
#workspace {
  --workspace-content-max-w: 832px;
  position: relative;
  flex: 1;
  min-width: 0;
  height: 100vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.workspace-header,
.conversation,
.workspace-input {
  width: min(100%, var(--workspace-content-max-w));
  margin-inline: auto;
}
```

- [ ] **Step 4: Run targeted test to verify it passes**

Run:

```bash
go test ./cmd/serf-hub -run TestWebWorkspaceContentColumnCSSContract -count=1
```

Expected: PASS.

- [ ] **Step 5: Run the hub web test package**

Run:

```bash
go test ./cmd/serf-hub -count=1
```

Expected: PASS.

- [ ] **Step 6: Review diff for scope**

Run:

```bash
git diff -- cmd/serf-hub/assets/style.css cmd/serf-hub/web_test.go
```

Expected: only the CSS content-column contract test and the CSS cap/centering rules changed.

- [ ] **Step 7: Commit implementation**

Run:

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/web_test.go
git commit -m "fix(web): center capped workspace content"
```

Expected: commit succeeds and includes only those two files.

---

## Self-Review

- Spec coverage: Task 1 implements the CSS-only approach from the design: `#workspace` remains full-width, the three child content sections get a shared cap and auto margins, and thread document behavior is inherited through shared CSS.
- Placeholder scan: no placeholder tasks or unspecified implementation steps remain.
- Type/name consistency: the plan uses exact existing selector names from `cmd/serf-hub/templates/partials/workspace.html` and the custom property name is consistent in test and CSS.
