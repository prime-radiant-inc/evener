package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// TestResearchSmokeFix_SolvableByScriptedSession runs a real session with real
// tools against a copy of the smoke-fix fixture and then runs the committed
// verifier. It is the offline proof that the environment is solvable and the
// verifier is correct.
func TestResearchSmokeFix_SolvableByScriptedSession(t *testing.T) {
	root := repoRootForResearch(t)
	envDir := filepath.Join(root, "research", "environments", "smoke-fix")
	work := t.TempDir()
	if err := copyTreeForResearch(filepath.Join(work, "repo"), filepath.Join(envDir, "repo")); err != nil {
		t.Fatal(err)
	}
	task, err := os.ReadFile(filepath.Join(envDir, "task.md"))
	if err != nil {
		t.Fatal(err)
	}

	// The fix the scripted session applies: the BUG comment line and the
	// bugged return below it are replaced together by the correct return.
	// The expected post-fix content is derived from the committed fixture so
	// the expectation can never drift from what the edit is scripted to do.
	const bugSpan = "\t// BUG: divides by len(xs)+1, skewing every result.\n\treturn sum / float64(len(xs)+1)"
	const fixSpan = "\treturn sum / float64(len(xs))"
	fixture, err := os.ReadFile(filepath.Join(envDir, "repo", "calc", "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fixture), bugSpan) {
		t.Fatalf("fixture calc.go no longer carries the bugged span:\n%s", fixture)
	}
	// NEGATIVE CONTROL: the committed verifier must FAIL against the
	// pristine (unfixed) workdir copy. Every other path in this suite runs
	// verify.sh only against already-fixed code, so a vacuously-green
	// verifier (exit 0 on anything) would pass the whole suite and silently
	// invalidate every gate verdict — and verifier-driven scoring is this
	// loop's core oracle. This one assertion is the discrimination proof.
	negative := exec.Command(filepath.Join(envDir, "verify.sh"), filepath.Join(work, "repo"))
	if out, err := negative.CombinedOutput(); err == nil {
		t.Fatalf("verify.sh passed against the pristine unfixed workdir; it cannot discriminate a real fix:\n%s", out)
	}
	fixed := strings.Replace(string(fixture), bugSpan, fixSpan, 1)
	calcPath := filepath.Join(work, "repo", "calc", "calc.go")

	fa := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		// Round 1: run the failing test.
		func(r llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call1", Name: "shell",
					Arguments: json.RawMessage(`{"command":"go test ./...","description":"run tests"}`)}},
			}}}
		},
		// Round 2: fix the file.
		func(r llm.Request) llm.Response {
			args := map[string]any{
				"file_path":  calcPath,
				"old_string": bugSpan,
				"new_string": fixSpan,
			}
			raw, _ := json.Marshal(args)
			return llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call2", Name: "edit_file", Arguments: raw}},
			}}}
		},
		// Round 3: confirm the fix.
		func(r llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call3", Name: "shell",
					Arguments: json.RawMessage(`{"command":"go test ./...","description":"confirm fix"}`)}},
			}}}
		},
		// Round 4: report the fix. The terminal communicate carries the
		// session's final answer, which ProcessInput returns.
		func(r llm.Request) llm.Response {
			return finalResponse("Fixed: Average divided by len(xs)+1; changed to len(xs). Tests pass.")
		},
	}}

	c := llm.NewClient()
	c.Register(fa)
	// StateDir is set below, so NewSession launches the background session
	// namer on the first prompt. Route that call to a dedicated scripted
	// provider so it cannot consume the openai adapter's response script.
	profile := withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2"))

	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(filepath.Join(work, "repo")), SessionConfig{
		NonInteractive: true,
		StateDir:       t.TempDir(),
		AgentsDocPath:  filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// TRIPWIRE: the scripted loop plus three go-toolchain invocations
	// normally finish in ~2s; the 2m bound only fires on a genuine hang
	// (cold build cache under a loaded suite), never as the mechanism.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := sess.ProcessInput(ctx, string(task), nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The loop must have run the whole script: three tool-call rounds whose
	// results fed the next model request, then the terminal answer turn.
	if got := len(fa.Requests()); got != 4 {
		t.Fatalf("scripted session made %d model requests, want 4", got)
	}
	const wantAnswer = "Fixed: Average divided by len(xs)+1; changed to len(xs). Tests pass."
	if strings.TrimSpace(out) != wantAnswer {
		t.Fatalf("ProcessInput output = %q, want %q", out, wantAnswer)
	}

	// The file must now be fixed in the workdir.
	got, err := os.ReadFile(calcPath)
	if err != nil || string(got) != fixed {
		t.Fatalf("workdir not fixed:\n%s\nerr=%v", got, err)
	}
	// The committed verifier must pass against the workdir.
	verify := exec.Command(filepath.Join(envDir, "verify.sh"), filepath.Join(work, "repo"))
	if out, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("verify.sh failed: %v\n%s", err, out)
	}
}

// repoRootForResearch walks up from the working directory to the repository
// root (the directory containing go.work).
func repoRootForResearch(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

// copyTreeForResearch copies a regular-file directory tree from src to dst.
func copyTreeForResearch(dst, src string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
