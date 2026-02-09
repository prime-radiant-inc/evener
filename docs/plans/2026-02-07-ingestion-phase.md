# Ingestion Phase: English Requirements to .dot Pipeline

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `kilroy attractor ingest` CLI command that takes English requirements text and produces a validated `.dot` pipeline file by invoking Claude Code with the `english-to-dotfile` skill.

**Architecture:** New CLI subcommand `ingest` spawns `claude -p` with `--append-system-prompt-file` pointing at `skills/english-to-dotfile/SKILL.md`, passes the user's requirements as the prompt, captures the `.dot` output, validates it via `engine.Prepare()`, and writes it to disk. The skill needs a minor modification to instruct the model to emit only the raw `.dot` content (no markdown fences, no commentary) so the output is machine-parseable.

**Tech Stack:** Go, Claude Code CLI (`claude -p`), existing `engine.Prepare()` validator, existing `codergen_router.go` patterns for CLI subprocess management.

---

## Context for the Implementer

### Key Files You Need to Know

| File | What It Does |
|------|-------------|
| `cmd/kilroy/main.go` | CLI entry point. Manual arg parsing (no framework). `attractor()` dispatches to `attractorRun`, `attractorResume`, `attractorValidate`. |
| `internal/attractor/engine/codergen_router.go` | Shows how to invoke `claude -p` as subprocess. See `runCLI()` at line 184 and `defaultCLIInvocation()` at line 343. |
| `internal/attractor/engine/engine.go` | `Prepare(dotSource []byte)` parses + validates `.dot` files. Returns `(*model.Graph, []validate.Diagnostic, error)`. |
| `internal/attractor/engine/config.go` | `RunConfigFile` struct, `normalizeProviderKey()`, `envOr()`. |
| `skills/english-to-dotfile/SKILL.md` | The skill document. Will be passed via `--append-system-prompt-file` to Claude Code. |

### How Claude Code CLI Is Currently Invoked (for reference)

```go
// codergen_router.go:343-358
exe = envOr("KILROY_CLAUDE_PATH", "claude")
args = []string{"-p", "--output-format", "stream-json", "--model", modelID}
```

For ingestion, we want a **simpler** invocation: we don't need stream-json, we want plain text output (the `.dot` file content). We'll use `--output-format text`.

### Claude Code CLI Flags We'll Use

```bash
claude -p \
  --append-system-prompt-file skills/english-to-dotfile/SKILL.md \
  --output-format text \
  --model claude-sonnet-4-5 \
  --max-turns 3 \
  --dangerously-skip-permissions \
  "Build DTTF per the spec at demo/dttf/dttf-v1.md"
```

- `--append-system-prompt-file`: Loads skill as system prompt addition (keeps Claude Code defaults)
- `--output-format text`: Raw text output (no JSON wrapping)
- `--model`: Configurable, defaults to `claude-sonnet-4-5`
- `--max-turns 3`: Ingestion is a generation task, not agentic coding — 3 turns is plenty
- `--dangerously-skip-permissions`: No TTY available for permission prompts

### Important Constraint: `.dot` Extraction

Claude Code with the skill will produce a `.dot` file, but it may also produce commentary text around it. The skill must be modified to instruct: "Output ONLY the digraph content. No markdown fences, no explanatory text before or after." Then the ingest command extracts the `digraph ... { ... }` block from stdout.

---

## Task 1: Modify SKILL.md for Machine-Parseable Output

**Files:**
- Modify: `skills/english-to-dotfile/SKILL.md`

**Why:** The skill currently doesn't tell the model to output raw `.dot` content only. When invoked programmatically, we need to reliably extract the digraph from the output.

**Step 1: Add output format section to SKILL.md**

Add the following section after the "## When to Use" section and before "## Process":

```markdown
## Output Format

When invoked programmatically (via CLI), output ONLY the raw `.dot` file content. No markdown fences, no explanatory text before or after the digraph. The output must start with `digraph` and end with the closing `}`.

When invoked interactively (in conversation), you may include explanatory text.
```

**Step 2: Verify the edit**

Run: `grep -n "Output Format" skills/english-to-dotfile/SKILL.md`
Expected: One match at the line you inserted.

**Step 3: Commit**

```bash
git add skills/english-to-dotfile/SKILL.md
git commit -m "Add machine-parseable output format guidance to english-to-dotfile skill"
```

---

## Task 2: Add `ingest` Subcommand Skeleton

**Files:**
- Create: `cmd/kilroy/ingest.go`
- Modify: `cmd/kilroy/main.go`

**Step 1: Write the failing test**

Create `cmd/kilroy/ingest_test.go`:

```go
package main

import (
	"testing"
)

func TestParseIngestArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(*testing.T, *ingestOptions)
	}{
		{
			name:    "missing requirements",
			args:    []string{},
			wantErr: true,
		},
		{
			name: "requirements from positional arg",
			args: []string{"Build a solitaire game"},
			check: func(t *testing.T, o *ingestOptions) {
				if o.requirements != "Build a solitaire game" {
					t.Errorf("requirements = %q, want %q", o.requirements, "Build a solitaire game")
				}
			},
		},
		{
			name: "output flag",
			args: []string{"--output", "pipeline.dot", "Build a solitaire game"},
			check: func(t *testing.T, o *ingestOptions) {
				if o.outputPath != "pipeline.dot" {
					t.Errorf("outputPath = %q, want %q", o.outputPath, "pipeline.dot")
				}
			},
		},
		{
			name: "model flag",
			args: []string{"--model", "claude-opus-4-6", "Build a solitaire game"},
			check: func(t *testing.T, o *ingestOptions) {
				if o.model != "claude-opus-4-6" {
					t.Errorf("model = %q, want %q", o.model, "claude-opus-4-6")
				}
			},
		},
		{
			name: "skill flag",
			args: []string{"--skill", "/tmp/custom-skill.md", "Build a solitaire game"},
			check: func(t *testing.T, o *ingestOptions) {
				if o.skillPath != "/tmp/custom-skill.md" {
					t.Errorf("skillPath = %q, want %q", o.skillPath, "/tmp/custom-skill.md")
				}
			},
		},
		{
			name: "default model",
			args: []string{"Build a solitaire game"},
			check: func(t *testing.T, o *ingestOptions) {
				if o.model != "claude-sonnet-4-5" {
					t.Errorf("model = %q, want default %q", o.model, "claude-sonnet-4-5")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseIngestArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseIngestArgs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && tt.check != nil {
				tt.check(t, opts)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/kilroy/ -run TestParseIngestArgs -v`
Expected: FAIL — `parseIngestArgs` and `ingestOptions` don't exist yet.

**Step 3: Write `cmd/kilroy/ingest.go`**

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ingestOptions struct {
	requirements string
	outputPath   string
	model        string
	skillPath    string
	repoPath     string
	validate     bool
}

func parseIngestArgs(args []string) (*ingestOptions, error) {
	opts := &ingestOptions{
		model:    "claude-sonnet-4-5",
		validate: true,
	}

	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--output", "-o":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--output requires a value")
			}
			opts.outputPath = args[i]
		case "--model":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--model requires a value")
			}
			opts.model = args[i]
		case "--skill":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--skill requires a value")
			}
			opts.skillPath = args[i]
		case "--repo":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--repo requires a value")
			}
			opts.repoPath = args[i]
		case "--no-validate":
			opts.validate = false
		default:
			if strings.HasPrefix(args[i], "-") {
				return nil, fmt.Errorf("unknown flag: %s", args[i])
			}
			positional = append(positional, args[i])
		}
	}

	if len(positional) == 0 {
		return nil, fmt.Errorf("requirements text is required (positional argument)")
	}
	opts.requirements = strings.Join(positional, " ")

	if opts.repoPath == "" {
		// Default to current directory.
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		opts.repoPath = cwd
	}

	if opts.skillPath == "" {
		// Default: look for skill relative to repo root.
		candidate := filepath.Join(opts.repoPath, "skills", "english-to-dotfile", "SKILL.md")
		if _, err := os.Stat(candidate); err == nil {
			opts.skillPath = candidate
		}
	}

	return opts, nil
}

func attractorIngest(args []string) {
	opts, err := parseIngestArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "usage: kilroy attractor ingest [flags] <requirements>")
		fmt.Fprintln(os.Stderr, "  --output, -o    Output .dot file path (default: stdout)")
		fmt.Fprintln(os.Stderr, "  --model         LLM model (default: claude-sonnet-4-5)")
		fmt.Fprintln(os.Stderr, "  --skill         Path to skill .md file (default: auto-detect)")
		fmt.Fprintln(os.Stderr, "  --repo          Repository root (default: cwd)")
		fmt.Fprintln(os.Stderr, "  --no-validate   Skip .dot validation")
		os.Exit(1)
	}

	dotContent, err := runIngest(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if opts.outputPath != "" {
		if err := os.WriteFile(opts.outputPath, []byte(dotContent), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", opts.outputPath, len(dotContent))
	} else {
		fmt.Print(dotContent)
	}
}
```

**Step 4: Wire into main.go**

Add `"ingest"` case to the `attractor()` switch in `cmd/kilroy/main.go`:

```go
// In the attractor() function, add this case:
case "ingest":
    attractorIngest(args[1:])
```

Also update the `usage()` function to include:

```go
fmt.Fprintln(os.Stderr, "  kilroy attractor ingest [--output <file.dot>] [--model <model>] [--skill <skill.md>] <requirements>")
```

**Step 5: Create stub `runIngest` so it compiles**

Add to `cmd/kilroy/ingest.go` (temporary stub):

```go
func runIngest(opts *ingestOptions) (string, error) {
	return "", fmt.Errorf("not implemented")
}
```

**Step 6: Run tests**

Run: `go test ./cmd/kilroy/ -run TestParseIngestArgs -v`
Expected: ALL PASS

**Step 7: Commit**

```bash
git add cmd/kilroy/ingest.go cmd/kilroy/ingest_test.go cmd/kilroy/main.go
git commit -m "Add kilroy attractor ingest subcommand skeleton with arg parsing"
```

---

## Task 3: Implement .dot Extraction from LLM Output

**Files:**
- Create: `internal/attractor/ingest/extract.go`
- Create: `internal/attractor/ingest/extract_test.go`

**Why:** Claude Code's text output will contain the `.dot` digraph, but may also contain preamble text, markdown fences, or trailing commentary. We need a reliable extractor.

**Step 1: Write the failing test**

Create `internal/attractor/ingest/extract_test.go`:

```go
package ingest

import (
	"testing"
)

func TestExtractDigraph(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(*testing.T, string)
	}{
		{
			name:  "clean digraph only",
			input: "digraph foo {\n    start [shape=Mdiamond]\n    exit [shape=Msquare]\n    start -> exit\n}",
			check: func(t *testing.T, got string) {
				if got[:7] != "digraph" {
					t.Errorf("should start with 'digraph', got %q", got[:20])
				}
			},
		},
		{
			name:  "digraph with leading text",
			input: "Here is the pipeline:\n\ndigraph foo {\n    start [shape=Mdiamond]\n    exit [shape=Msquare]\n    start -> exit\n}",
			check: func(t *testing.T, got string) {
				if got[:7] != "digraph" {
					t.Errorf("should start with 'digraph', got %q", got[:20])
				}
			},
		},
		{
			name:  "digraph in markdown code fence",
			input: "```dot\ndigraph foo {\n    start [shape=Mdiamond]\n    exit [shape=Msquare]\n    start -> exit\n}\n```",
			check: func(t *testing.T, got string) {
				if got[:7] != "digraph" {
					t.Errorf("should start with 'digraph', got %q", got[:20])
				}
				if got[len(got)-1] != '}' {
					t.Errorf("should end with '}', got %q", got[len(got)-5:])
				}
			},
		},
		{
			name:  "digraph with trailing text",
			input: "digraph foo {\n    start [shape=Mdiamond]\n    exit [shape=Msquare]\n    start -> exit\n}\n\nThis pipeline has 2 nodes.",
			check: func(t *testing.T, got string) {
				if got[len(got)-1] != '}' {
					t.Errorf("should end with '}', got %q", got[len(got)-10:])
				}
			},
		},
		{
			name:  "nested braces in prompts",
			input: "digraph foo {\n    n1 [prompt=\"if (x) { return }\"]\n    start [shape=Mdiamond]\n    exit [shape=Msquare]\n    start -> n1 -> exit\n}",
			check: func(t *testing.T, got string) {
				if got[:7] != "digraph" {
					t.Errorf("should start with 'digraph'")
				}
				if got[len(got)-1] != '}' {
					t.Errorf("should end with '}'")
				}
			},
		},
		{
			name:    "no digraph found",
			input:   "I couldn't generate the pipeline because the requirements are unclear.",
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractDigraph(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractDigraph() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/ingest/ -run TestExtractDigraph -v`
Expected: FAIL — package doesn't exist.

**Step 3: Implement the extractor**

Create `internal/attractor/ingest/extract.go`:

```go
package ingest

import (
	"fmt"
	"strings"
)

// ExtractDigraph extracts a DOT digraph block from LLM output text.
// It handles: raw digraph, markdown-fenced digraphs, leading/trailing commentary.
// It uses brace counting (respecting quoted strings) to find the matching closing brace.
func ExtractDigraph(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("empty input")
	}

	// Find "digraph" keyword.
	idx := strings.Index(text, "digraph")
	if idx == -1 {
		return "", fmt.Errorf("no digraph found in output")
	}

	// Start from the digraph keyword.
	sub := text[idx:]

	// Find the opening brace.
	openIdx := strings.Index(sub, "{")
	if openIdx == -1 {
		return "", fmt.Errorf("digraph has no opening brace")
	}

	// Count braces to find the matching close, respecting quoted strings.
	depth := 0
	inQuote := false
	escape := false
	closeIdx := -1

	for i := openIdx; i < len(sub); i++ {
		ch := sub[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' && inQuote {
			escape = true
			continue
		}
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				closeIdx = i
				break
			}
		}
	}

	if closeIdx == -1 {
		return "", fmt.Errorf("unmatched braces in digraph")
	}

	return sub[:closeIdx+1], nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/ingest/ -run TestExtractDigraph -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add internal/attractor/ingest/extract.go internal/attractor/ingest/extract_test.go
git commit -m "Add digraph extraction from LLM text output"
```

---

## Task 4: Implement `runIngest` — Claude Code CLI Invocation

**Files:**
- Modify: `cmd/kilroy/ingest.go` (replace stub `runIngest`)
- Create: `internal/attractor/ingest/ingest.go`
- Create: `internal/attractor/ingest/ingest_test.go`

**Why:** This is the core: spawn `claude -p` with the skill as system prompt, capture output, extract and validate the `.dot` content.

**Step 1: Write the failing test**

Create `internal/attractor/ingest/ingest_test.go`:

```go
package ingest

import (
	"context"
	"testing"
)

func TestBuildCLIArgs(t *testing.T) {
	tests := []struct {
		name      string
		opts      Options
		wantExe   string
		wantArgs  []string
		checkArgs func(*testing.T, []string)
	}{
		{
			name: "basic invocation",
			opts: Options{
				Model:        "claude-sonnet-4-5",
				SkillPath:    "/repo/skills/english-to-dotfile/SKILL.md",
				Requirements: "Build a solitaire game",
			},
			wantExe: "claude",
			checkArgs: func(t *testing.T, args []string) {
				assertContains(t, args, "-p")
				assertContains(t, args, "--output-format")
				assertContains(t, args, "text")
				assertContains(t, args, "--model")
				assertContains(t, args, "claude-sonnet-4-5")
				assertContains(t, args, "--append-system-prompt-file")
				assertContains(t, args, "/repo/skills/english-to-dotfile/SKILL.md")
				assertContains(t, args, "--max-turns")
				assertContains(t, args, "--dangerously-skip-permissions")
			},
		},
		{
			name: "custom model",
			opts: Options{
				Model:        "claude-opus-4-6",
				SkillPath:    "/repo/skills/english-to-dotfile/SKILL.md",
				Requirements: "Build DTTF",
			},
			checkArgs: func(t *testing.T, args []string) {
				assertContains(t, args, "claude-opus-4-6")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exe, args := buildCLIArgs(tt.opts)
			if tt.wantExe != "" && exe != tt.wantExe {
				t.Errorf("exe = %q, want %q", exe, tt.wantExe)
			}
			if tt.checkArgs != nil {
				tt.checkArgs(t, args)
			}
		})
	}
}

func TestRunIngestRequiresSkill(t *testing.T) {
	_, err := Run(context.Background(), Options{
		Requirements: "Build something",
		SkillPath:    "/nonexistent/SKILL.md",
		Model:        "claude-sonnet-4-5",
	})
	if err == nil {
		t.Fatal("expected error for missing skill file")
	}
}

func assertContains(t *testing.T, slice []string, want string) {
	t.Helper()
	for _, s := range slice {
		if s == want {
			return
		}
	}
	t.Errorf("args %v does not contain %q", slice, want)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/ingest/ -run TestBuildCLIArgs -v`
Expected: FAIL — `buildCLIArgs`, `Options`, `Run` don't exist.

**Step 3: Implement `internal/attractor/ingest/ingest.go`**

```go
package ingest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	attrengine "github.com/strongdm/kilroy/internal/attractor/engine"
)

// Options configures an ingestion run.
type Options struct {
	Requirements string // The English requirements text.
	SkillPath    string // Path to the SKILL.md file.
	Model        string // LLM model ID.
	RepoPath     string // Repository root (working directory for claude).
	Validate     bool   // Whether to validate the .dot output.
	MaxTurns     int    // Max turns for claude (default 3).
}

// Result contains the output of an ingestion run.
type Result struct {
	DotContent string   // The extracted .dot file content.
	RawOutput  string   // The full raw output from Claude Code.
	Warnings   []string // Any validation warnings.
}

func buildCLIArgs(opts Options) (string, []string) {
	exe := envOr("KILROY_CLAUDE_PATH", "claude")
	maxTurns := opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 3
	}

	args := []string{
		"-p",
		"--output-format", "text",
		"--model", opts.Model,
		"--max-turns", fmt.Sprintf("%d", maxTurns),
		"--dangerously-skip-permissions",
	}

	if opts.SkillPath != "" {
		args = append(args, "--append-system-prompt-file", opts.SkillPath)
	}

	// The requirements are the prompt — appended last.
	args = append(args, opts.Requirements)

	return exe, args
}

// Run executes the ingestion: invokes Claude Code with the skill and requirements,
// extracts the .dot content, and optionally validates it.
func Run(ctx context.Context, opts Options) (*Result, error) {
	// Verify skill file exists.
	if _, err := os.Stat(opts.SkillPath); err != nil {
		return nil, fmt.Errorf("skill file not found: %s: %w", opts.SkillPath, err)
	}

	exe, args := buildCLIArgs(opts)

	cmd := exec.CommandContext(ctx, exe, args...)
	if opts.RepoPath != "" {
		cmd.Dir = opts.RepoPath
	}
	// No stdin — avoid interactive prompts.
	cmd.Stdin = strings.NewReader("")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	rawOutput := stdout.String()
	if err != nil {
		return nil, fmt.Errorf("claude invocation failed (exit %v): %s\nstderr: %s",
			err, truncateStr(rawOutput, 500), truncateStr(stderr.String(), 500))
	}

	// Extract the digraph from the output.
	dotContent, err := ExtractDigraph(rawOutput)
	if err != nil {
		return nil, fmt.Errorf("failed to extract digraph from output: %w\nraw output (first 1000 chars): %s",
			err, truncateStr(rawOutput, 1000))
	}

	result := &Result{
		DotContent: dotContent,
		RawOutput:  rawOutput,
	}

	// Optionally validate.
	if opts.Validate {
		_, diags, err := attrengine.Prepare([]byte(dotContent))
		if err != nil {
			return result, fmt.Errorf("generated .dot failed validation: %w", err)
		}
		for _, d := range diags {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %s (%s)", d.Severity, d.Message, d.Rule))
		}
	}

	return result, nil
}

func envOr(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
```

**Step 4: Update `cmd/kilroy/ingest.go` to call `ingest.Run`**

Replace the `runIngest` stub with:

```go
func runIngest(opts *ingestOptions) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := ingest.Run(ctx, ingest.Options{
		Requirements: opts.requirements,
		SkillPath:    opts.skillPath,
		Model:        opts.model,
		RepoPath:     opts.repoPath,
		Validate:     opts.validate,
	})
	if err != nil {
		return "", err
	}

	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	return result.DotContent, nil
}
```

Add imports at the top of `cmd/kilroy/ingest.go`:

```go
import (
	"context"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/ingest"
)
```

Remove the duplicate imports that are no longer needed (the stub used `fmt` and `os` already).

**Step 5: Run tests**

Run: `go test ./internal/attractor/ingest/ -v && go test ./cmd/kilroy/ -v`
Expected: ALL PASS (unit tests for arg building and extraction pass; the `TestRunIngestRequiresSkill` test passes because it checks the skill-file-not-found error path)

**Step 6: Commit**

```bash
git add internal/attractor/ingest/ingest.go internal/attractor/ingest/ingest_test.go cmd/kilroy/ingest.go
git commit -m "Implement runIngest: Claude Code CLI invocation with skill and validation"
```

---

## Task 5: Integration Test with Validation

**Files:**
- Create: `internal/attractor/ingest/integration_test.go`

**Why:** Verify that the full pipeline works end-to-end: build CLI args, extract digraph from realistic LLM output samples, and validate the extracted `.dot` against `engine.Prepare()`.

**Step 1: Write the integration test**

Create `internal/attractor/ingest/integration_test.go`:

```go
package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/strongdm/kilroy/internal/attractor/engine"
)

// TestExtractAndValidateResearchDotFiles tests that existing research .dot files
// can be extracted and validated by the engine.
func TestExtractAndValidateResearchDotFiles(t *testing.T) {
	// Find the repo root by walking up from the test file.
	repoRoot := findRepoRoot(t)

	dotFiles := []string{
		"research/refactor-test-vague.dot",
		"research/refactor-test-moderate.dot",
		"research/refactor-test-complex.dot",
	}

	for _, rel := range dotFiles {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			path := filepath.Join(repoRoot, rel)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("file not found (expected in CI): %s", path)
			}

			// Test extraction from raw content (simulates clean LLM output).
			extracted, err := ExtractDigraph(string(content))
			if err != nil {
				t.Fatalf("ExtractDigraph failed: %v", err)
			}

			// Test extraction from wrapped content (simulates LLM with commentary).
			wrapped := "Here is the pipeline:\n\n```dot\n" + string(content) + "\n```\n\nThis pipeline has multiple stages."
			extracted2, err := ExtractDigraph(wrapped)
			if err != nil {
				t.Fatalf("ExtractDigraph (wrapped) failed: %v", err)
			}

			// Both should produce the same result.
			if extracted != extracted2 {
				t.Error("wrapped extraction produced different result than raw")
			}

			// Validate via engine.Prepare.
			_, diags, err := engine.Prepare([]byte(extracted))
			if err != nil {
				t.Fatalf("engine.Prepare failed: %v", err)
			}
			for _, d := range diags {
				t.Logf("diagnostic: %s: %s (%s)", d.Severity, d.Message, d.Rule)
			}
		})
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}
```

**Step 2: Run the integration test**

Run: `go test ./internal/attractor/ingest/ -run TestExtractAndValidateResearchDotFiles -v`
Expected: PASS — all 3 research .dot files should extract and validate cleanly.

**Step 3: Commit**

```bash
git add internal/attractor/ingest/integration_test.go
git commit -m "Add integration test: extract and validate existing research .dot files"
```

---

## Task 6: End-to-End Manual Test

**Files:**
- No new files. This is a manual verification task.

**Why:** Verify the full `kilroy attractor ingest` command works against a real Claude Code invocation.

**Step 1: Build the binary**

Run: `go build -o /tmp/kilroy ./cmd/kilroy/`
Expected: Clean build, binary at `/tmp/kilroy`.

**Step 2: Run ingestion with vague input**

Run:
```bash
/tmp/kilroy attractor ingest \
  --output /tmp/test-ingest-vague.dot \
  --skill skills/english-to-dotfile/SKILL.md \
  "solitaire plz"
```

Expected:
- Claude Code is invoked (you'll see it running briefly)
- A `.dot` file is written to `/tmp/test-ingest-vague.dot`
- stderr shows: `wrote /tmp/test-ingest-vague.dot (N bytes)`
- No validation errors

**Step 3: Validate the generated file independently**

Run: `/tmp/kilroy attractor validate --graph /tmp/test-ingest-vague.dot`
Expected: `ok: test-ingest-vague.dot`

**Step 4: Run ingestion with spec path**

Run:
```bash
/tmp/kilroy attractor ingest \
  --output /tmp/test-ingest-dttf.dot \
  --skill skills/english-to-dotfile/SKILL.md \
  "Build DTTF per the spec at demo/dttf/dttf-v1.md"
```

Expected:
- A larger `.dot` file is written
- Validates cleanly
- The `.dot` file should NOT have an `expand_spec` node (spec already exists)
- All nodes should reference `demo/dttf/dttf-v1.md`

**Step 5: Verify the generated .dot file content**

For the DTTF output, manually check:
- `grep 'expand_spec' /tmp/test-ingest-dttf.dot` → should find nothing
- `grep 'demo/dttf/dttf-v1.md' /tmp/test-ingest-dttf.dot` → should find multiple references
- `grep 'class="verify"' /tmp/test-ingest-dttf.dot` → should find matches for all verify nodes
- `grep 'fallback_retry_target' /tmp/test-ingest-dttf.dot` → should find one match
- `grep 'Goal: \$goal' /tmp/test-ingest-dttf.dot` → should find matches in all impl prompts

**Step 6: Commit test artifacts (optional)**

If the generated files look good:
```bash
cp /tmp/test-ingest-vague.dot research/e2e-ingest-vague.dot
cp /tmp/test-ingest-dttf.dot research/e2e-ingest-dttf.dot
git add research/e2e-ingest-vague.dot research/e2e-ingest-dttf.dot
git commit -m "Add end-to-end ingestion test artifacts"
```

---

## Task 7: Update Usage and Documentation

**Files:**
- Modify: `cmd/kilroy/main.go` (usage text already added in Task 2)

**Step 1: Verify usage is complete**

Run: `/tmp/kilroy` (no args)

Expected output includes the ingest command:
```
usage:
  kilroy attractor run --graph <file.dot> --config <run.yaml> [--run-id <id>] [--logs-root <dir>]
  kilroy attractor resume --logs-root <dir>
  kilroy attractor resume --cxdb <http_base_url> --context-id <id>
  kilroy attractor resume --run-branch <attractor/run/...> [--repo <path>]
  kilroy attractor validate --graph <file.dot>
  kilroy attractor ingest [--output <file.dot>] [--model <model>] [--skill <skill.md>] <requirements>
```

**Step 2: Verify ingest help**

Run: `/tmp/kilroy attractor ingest`

Expected: Shows usage with flag descriptions.

**Step 3: Final commit**

```bash
git add -A
git commit -m "Complete kilroy attractor ingest command: English requirements to validated .dot pipeline"
```

---

## Summary: What Gets Built

```
User: "Build DTTF per the spec at demo/dttf/dttf-v1.md"
  ↓
kilroy attractor ingest --output pipeline.dot "Build DTTF per the spec at demo/dttf/dttf-v1.md"
  ↓
Spawns: claude -p \
  --append-system-prompt-file skills/english-to-dotfile/SKILL.md \
  --output-format text \
  --model claude-sonnet-4-5 \
  --max-turns 3 \
  --dangerously-skip-permissions \
  "Build DTTF per the spec at demo/dttf/dttf-v1.md"
  ↓
Claude Code generates .dot content (guided by SKILL.md)
  ↓
ExtractDigraph() pulls out the digraph block
  ↓
engine.Prepare() validates the .dot syntax and structure
  ↓
pipeline.dot written to disk
  ↓
kilroy attractor run --graph pipeline.dot --config run.yaml
```

### Files Created/Modified

| Action | File |
|--------|------|
| Modify | `skills/english-to-dotfile/SKILL.md` |
| Create | `cmd/kilroy/ingest.go` |
| Create | `cmd/kilroy/ingest_test.go` |
| Modify | `cmd/kilroy/main.go` |
| Create | `internal/attractor/ingest/extract.go` |
| Create | `internal/attractor/ingest/extract_test.go` |
| Create | `internal/attractor/ingest/ingest.go` |
| Create | `internal/attractor/ingest/ingest_test.go` |
| Create | `internal/attractor/ingest/integration_test.go` |
