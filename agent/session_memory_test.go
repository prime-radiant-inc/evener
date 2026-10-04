package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/artifactstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func memoryCallResponse(name string, args map[string]any) llm.Response {
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "memory-test", Type: "function", Name: name, Arguments: raw}}}}}
}

// Catches missing native persistence, missing first-request projection, and lost tool results.
func TestMemoryFreshSession(t *testing.T) {
	t.Parallel()
	root, workspace := t.TempDir(), t.TempDir()
	cfg := SessionConfig{MemoryStateRoot: root, MemoryProjectID: "fixture-project"}
	cfg.testOnly.memoryBeforeIO = func(scope, operation string) error { t.Logf("memory I/O %s %s", scope, operation); return nil }
	body := "opaque-lesson-71\n[unresolved](missing)\n(created someday)\n"
	topic := "opaque-topic-83\nnot markdown, no date\n"
	a := newSession(t, withDir(workspace), withConfig(cfg), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_write", map[string]any{"scope": "project", "file_path": "MEMORY.md", "content": body, "intent": "Saving fixture data"})
		},
		func(req llm.Request) llm.Response {
			for _, msg := range req.Messages {
				for _, part := range msg.Content {
					if part.ToolResult != nil && part.ToolResult.Name == "memory_write" && part.ToolResult.IsError {
						t.Fatalf("native write failed: %v", part.ToolResult.Content)
					}
				}
			}
			return memoryCallResponse("memory_write", map[string]any{"scope": "project", "file_path": "nested/unusual name.txt", "content": topic, "intent": "Saving topic data"})
		},
		func(llm.Request) llm.Response { return finalResponse("saved") },
	))
	if _, err := a.ProcessInput(context.Background(), "save", nil); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"MEMORY.md": body, "nested/unusual name.txt": topic} {
		got, err := os.ReadFile(filepath.Join(root, "memory", "projects", "fixture-project", path))
		if err != nil || string(got) != want {
			t.Fatalf("path=%s bytes=%q err=%v", path, got, err)
		}
	}
	assertRead := func(req llm.Request, sentinel string) {
		t.Helper()
		for _, msg := range req.Messages {
			for _, part := range msg.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.Name == "memory_read" && !part.ToolResult.IsError && strings.Contains(fmt.Sprint(part.ToolResult.Content), sentinel) {
					return
				}
			}
		}
		t.Fatal("native read result was not delivered")
	}
	b := newSession(t, withDir(workspace), withConfig(cfg), withSteps(
		func(req llm.Request) llm.Response {
			seen := false
			for _, msg := range req.Messages {
				if strings.Contains(msg.Text(), "opaque-topic-83") {
					t.Fatal("topic was automatically preloaded")
				}
				if strings.Contains(msg.Text(), "opaque-lesson-71") {
					if msg.Role != llm.RoleUser {
						t.Fatalf("index role=%s", msg.Role)
					}
					seen = true
				}
			}
			if !seen {
				t.Fatal("fresh request lost saved index data")
			}
			return memoryCallResponse("memory_read", map[string]any{"scope": "project", "file_path": "MEMORY.md", "intent": "Reading fixture data"})
		},
		func(req llm.Request) llm.Response {
			assertRead(req, "opaque-lesson-71")
			return memoryCallResponse("memory_read", map[string]any{"scope": "project", "file_path": "nested/unusual name.txt", "intent": "Reading topic data"})
		},
		func(req llm.Request) llm.Response { assertRead(req, "opaque-topic-83"); return finalResponse("read") },
	))
	if _, err := b.ProcessInput(context.Background(), "continue", nil); err != nil {
		t.Fatal(err)
	}
}

func memoryExec(t *testing.T, s *Session, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return s.execTool(context.Background(), llm.ToolCallData{ID: "memory-direct", Name: name, Arguments: raw}, "")
}

func TestMemoryFreeFormOperations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root, MemoryProjectID: "fixture-project"}))
	for _, tc := range []struct{ path, body string }{
		{"plain.txt", "arbitrary prose\n"}, {"no-date", "No date\n"}, {"odd-date.txt", "date: yesterday-ish\n"},
		{"layout.bin", "not a canonical layout\n[broken](absent)\n"}, {"empty", ""}, {"nested/unusual name.txt", "opaque-nested-52\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			res := memoryExec(t, s, "memory_write", map[string]any{"scope": "project", "file_path": tc.path, "content": tc.body})
			if res.IsError {
				t.Fatal(res.Output)
			}
			got, err := os.ReadFile(filepath.Join(root, "memory/projects/fixture-project", tc.path))
			if err != nil || string(got) != tc.body {
				t.Fatalf("bytes=%q err=%v", got, err)
			}
		})
	}
	// Independent page and index calls leave incomplete organization intact.
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "project", "file_path": "MEMORY.md", "content": "opaque-index-94\n"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	got, err := os.ReadFile(filepath.Join(root, "memory/projects/fixture-project/nested/unusual name.txt"))
	if err != nil || string(got) != "opaque-nested-52\n" {
		t.Fatalf("page=%q err=%v", got, err)
	}
	res = memoryExec(t, s, "memory_write", map[string]any{"scope": "project", "file_path": "duplicate.txt", "content": "before\nToken\nToken\nafter\n"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	res = memoryExec(t, s, "memory_read", map[string]any{"scope": "project", "file_path": "duplicate.txt", "offset": 2, "limit": 1})
	if res.IsError || res.Output != "   2\tToken\n" {
		t.Fatalf("window=%q error=%t", res.Output, res.IsError)
	}
	res = memoryExec(t, s, "memory_edit", map[string]any{"scope": "project", "file_path": "duplicate.txt", "old_string": "Token", "new_string": "changed"})
	if !res.IsError {
		t.Fatal("duplicate edit accepted")
	}
	got, err = os.ReadFile(filepath.Join(root, "memory/projects/fixture-project/duplicate.txt"))
	if err != nil || string(got) != "before\nToken\nToken\nafter\n" {
		t.Fatalf("failed edit changed bytes=%q err=%v", got, err)
	}
	res = memoryExec(t, s, "memory_edit", map[string]any{"scope": "project", "file_path": "duplicate.txt", "old_string": "Token", "new_string": "changed", "replace_all": true})
	if res.IsError {
		t.Fatal(res.Output)
	}
	got, err = os.ReadFile(filepath.Join(root, "memory/projects/fixture-project/duplicate.txt"))
	if err != nil || string(got) != "before\nchanged\nchanged\nafter\n" {
		t.Fatalf("edit bytes=%q err=%v", got, err)
	}
	for _, tc := range []struct {
		mode         string
		max, context int
		want         string
	}{
		{"content", 1, 1, "duplicate.txt-1-before\nduplicate.txt:2:changed\nduplicate.txt-3-changed"},
		{"files_with_matches", 5, 0, "duplicate.txt"}, {"count", 5, 0, "duplicate.txt:2"},
	} {
		res = memoryExec(t, s, "memory_search", map[string]any{"scope": "project", "path": "", "pattern": "^CHANG(E|ed)", "case_insensitive": true, "glob_filter": "*.txt", "output_mode": tc.mode, "max_results": tc.max, "context_lines": tc.context})
		if res.IsError || !strings.Contains(res.Output, tc.want) {
			t.Fatalf("%s search=%q error=%t", tc.mode, res.Output, res.IsError)
		}
	}
	res = memoryExec(t, s, "memory_delete", map[string]any{"scope": "project", "file_path": "duplicate.txt"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	res = memoryExec(t, s, "memory_delete", map[string]any{"scope": "project", "file_path": "duplicate.txt"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	if _, err := os.Stat(filepath.Join(root, "memory/projects/fixture-project/duplicate.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete=%v", err)
	}
	got, err = os.ReadFile(filepath.Join(root, "memory/projects/fixture-project/MEMORY.md"))
	if err != nil || string(got) != "opaque-index-94\n" {
		t.Fatalf("unrelated=%q err=%v", got, err)
	}
}

func TestMemoryPathAuthority(t *testing.T) {
	t.Parallel()
	root, workspace, outside := t.TempDir(), t.TempDir(), t.TempDir()
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{MemoryStateRoot: root, MemoryProjectID: "fixture-project"}))
	env, err := s.memoryEnvironment("project")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "data"), []byte("opaque-outside-35"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "data"), filepath.Join(env.WorkingDirectory(), "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(env.WorkingDirectory(), "middle")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(env.WorkingDirectory(), "empty-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(env.WorkingDirectory(), "full-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.WorkingDirectory(), "full-dir/data"), []byte("opaque-directory-29"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory_read", "memory_write", "memory_edit", "memory_delete", "memory_search"} {
		for _, path := range []string{filepath.Join(outside, "data"), "../data", "leaf", "middle/data"} {
			args := map[string]any{"scope": "project"}
			switch name {
			case "memory_search":
				args["path"] = path
				args["pattern"] = "opaque"
			default:
				args["file_path"] = path
			}
			if name == "memory_write" {
				args["content"] = "bad"
			}
			if name == "memory_edit" {
				args["old_string"] = "opaque"
				args["new_string"] = "bad"
			}
			if res := memoryExec(t, s, name, args); !res.IsError {
				t.Fatalf("%s path=%q accepted: %s", name, path, res.Output)
			}
		}
	}
	for _, path := range []string{".", "empty-dir", "full-dir"} {
		if res := memoryExec(t, s, "memory_delete", map[string]any{"scope": "project", "file_path": path}); !res.IsError {
			t.Fatalf("deleted directory %s", path)
		}
	}
	for _, args := range []map[string]any{
		{"scope": "unknown", "file_path": "data"}, {"scope": "", "file_path": "data"}, {"file_path": "data"},
		{"scope": "project", "file_path": "data", "root": outside}, {"scope": "project", "file_path": "data", "project_id": "forged"},
	} {
		if res := memoryExec(t, s, "memory_read", args); !res.IsError {
			t.Fatalf("accepted forged args %v", args)
		}
	}
	got, err := os.ReadFile(filepath.Join(outside, "data"))
	if err != nil || string(got) != "opaque-outside-35" {
		t.Fatalf("outside changed=%q err=%v", got, err)
	}
	// A pre-existing symlink in the scope root's relative tail must fail setup.
	for _, relative := range []string{"memory", "memory/personal", "memory/projects/fixture-project"} {
		host := t.TempDir()
		link := filepath.Join(host, relative)
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		other := newSession(t, withConfig(SessionConfig{MemoryStateRoot: host, MemoryProjectID: "fixture-project"}))
		scope := "project"
		if relative == "memory/personal" {
			scope = "personal"
		}
		if res := memoryExec(t, other, "memory_write", map[string]any{"scope": scope, "file_path": "data", "content": "bad"}); !res.IsError {
			t.Fatalf("symlink root %s accepted", relative)
		}
	}
}

func TestMemoryReadGuardIsolation(t *testing.T) {
	t.Parallel()
	host, workspace := t.TempDir(), t.TempDir()
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{MemoryStateRoot: host, MemoryProjectID: "fixture-project"}))
	local := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	local.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeRestricted, FileTool: sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{workspace}, WriteRoots: []string{workspace}}}
	if err := os.WriteFile(filepath.Join(workspace, "same.txt"), []byte("workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"personal", "project"} {
		env, err := s.memoryEnvironment(scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.WriteFile("same.txt", "opaque-"+scope); err != nil {
			t.Fatal(err)
		}
	}
	if res := memoryExec(t, s, "read_file", map[string]any{"file_path": "same.txt"}); res.IsError {
		t.Fatal(res.Output)
	}
	for _, scope := range []string{"personal", "project"} {
		env, err := s.memoryEnvironment(scope)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(env.WorkingDirectory(), "same.txt")
		if local.FileExists(path) {
			t.Fatal("workspace can inspect memory")
		}
		if s.fileReadGuard(env).ReadBeforeWriteWarning(path) == "" {
			t.Fatalf("%s inherited read", scope)
		}
		if res := memoryExec(t, s, "memory_read", map[string]any{"scope": scope, "file_path": "same.txt"}); res.IsError {
			t.Fatal(res.Output)
		}
		if s.fileReadGuard(env).ReadBeforeWriteWarning(path) != "" {
			t.Fatalf("%s actual read not tracked", scope)
		}
		if scope == "personal" {
			project, _ := s.memoryEnvironment("project")
			if s.fileReadGuard(project).ReadBeforeWriteWarning(filepath.Join(project.WorkingDirectory(), "same.txt")) == "" {
				t.Fatal("project inherited personal read")
			}
		}
		if res := memoryExec(t, s, "memory_write", map[string]any{"scope": scope, "file_path": "same.txt", "content": "updated-" + scope}); res.IsError {
			t.Fatal(res.Output)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "updated-"+scope {
			t.Fatalf("bytes=%q err=%v", got, err)
		}
	}
}

func TestMemoryOutputRecovery(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory_read", "memory_search"} {
		t.Run(name, func(t *testing.T) {
			host := t.TempDir()
			wiki := filepath.Join(host, "memory/personal")
			if err := os.MkdirAll(wiki, 0o700); err != nil {
				t.Fatal(err)
			}
			var body, want strings.Builder
			for i := 1; i <= 400; i++ {
				line := fmt.Sprintf("opaque-artifact-%03d-%s", i, strings.Repeat("z", 170))
				body.WriteString(line + "\n")
				if name == "memory_read" {
					fmt.Fprintf(&want, "%4d\t%s\n", i, line)
				} else {
					if i > 1 {
						want.WriteByte('\n')
					}
					fmt.Fprintf(&want, "large.txt:%d:%s", i, line)
				}
			}
			if name == "memory_read" {
				want.WriteString(" 401\t\n")
			}
			if err := os.WriteFile(filepath.Join(wiki, "large.txt"), []byte(body.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := artifactstore.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			var recovered strings.Builder
			stage := 0
			ref := ""
			pages := 0
			step := func(req llm.Request) llm.Response {
				if stage == 0 {
					stage++
					args := map[string]any{"scope": "personal", "file_path": "large.txt"}
					if name == "memory_search" {
						args = map[string]any{"scope": "personal", "pattern": "opaque-artifact", "max_results": 600}
					}
					return memoryCallResponse(name, args)
				}
				var result *llm.ToolResultData
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if part.ToolResult != nil {
							result = part.ToolResult
						}
					}
				}
				if result == nil || result.IsError {
					t.Fatalf("result=%+v", result)
				}
				if stage == 1 {
					if result.Name != name {
						t.Fatalf("result name=%s", result.Name)
					}
					text := fmt.Sprint(result.Content)
					ref = regexp.MustCompile(`artifact:[a-zA-Z0-9]+`).FindString(text)
					if ref == "" {
						t.Fatalf("oversized result was not retained, length=%d", len(text))
					}
					if strings.Contains(text, "opaque-artifact-250-") {
						t.Fatal("middle sentinel not truncated")
					}
					stage++
					return memoryCallResponse("read_transcript", map[string]any{"transcript_ref": ref})
				}
				if result.Name != "read_transcript" {
					t.Fatalf("page name=%s", result.Name)
				}
				var envelope map[string]any
				if err := json.Unmarshal([]byte(fmt.Sprint(result.Content)), &envelope); err != nil {
					t.Fatal(err)
				}
				page, ok := envelope["page"].(map[string]any)
				if !ok {
					t.Fatalf("not a raw page: %v", envelope)
				}
				if page["offset_bytes"] != float64(recovered.Len()) {
					t.Fatalf("page offset=%v want=%d", page["offset_bytes"], recovered.Len())
				}
				data, ok := page["data"].(string)
				if !ok || len(data) == 0 {
					t.Fatalf("page data=%v", page)
				}
				recovered.WriteString(data)
				pages++
				if continuation, ok := envelope["continuation"].(map[string]any); ok {
					return memoryCallResponse("read_transcript", map[string]any{"transcript_ref": ref, "offset_bytes": continuation["offset_bytes"]})
				}
				if recovered.String() != want.String() {
					t.Fatalf("recovered bytes=%d want=%d", recovered.Len(), want.Len())
				}
				return finalResponse("recovered")
			}
			steps := make([]func(llm.Request) llm.Response, 16)
			for i := range steps {
				steps[i] = step
			}
			s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: host, artifactStore: store}), withSteps(steps...))
			if _, err := s.ProcessInput(context.Background(), "recover", nil); err != nil {
				t.Fatal(err)
			}
			if pages < 2 || recovered.String() != want.String() {
				t.Fatalf("pages=%d recovered=%d want=%d", pages, recovered.Len(), want.Len())
			}
		})
	}
}

func TestMemoryDisabledAndUnbound(t *testing.T) {
	t.Parallel()
	for _, cfg := range []SessionConfig{{}, {MemoryStateRoot: t.TempDir(), MemoryProjectID: "fixture-project", DisableMemory: true}} {
		calls := 0
		cfg.testOnly.memoryBeforeIO = func(string, string) error { calls++; return nil }
		s := newSession(t, withConfig(cfg), withSteps(func(req llm.Request) llm.Response {
			for _, def := range req.Tools {
				if strings.HasPrefix(def.Name, "memory_") {
					t.Fatalf("advertised memory %s", def.Name)
				}
			}
			for _, msg := range req.Messages {
				if strings.HasPrefix(msg.Name, "memory_") {
					t.Fatal("disabled context")
				}
			}
			return finalResponse("ordinary work")
		}))
		if _, err := s.ProcessInput(context.Background(), "continue", nil); err != nil {
			t.Fatal(err)
		}
		if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "data", "content": "bad"}); !res.IsError {
			t.Fatal("disabled dispatch accepted")
		}
		if _, err := s.memoryEnvironment("personal"); err == nil {
			t.Fatal("disabled environment accepted")
		}
		if calls != 0 {
			t.Fatalf("disabled I/O=%d", calls)
		}
		if cfg.MemoryStateRoot != "" {
			if _, err := os.Stat(filepath.Join(cfg.MemoryStateRoot, "memory")); !os.IsNotExist(err) {
				t.Fatalf("disabled setup=%v", err)
			}
		}
	}
}

func TestMemoryIndexProjection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wiki := filepath.Join(root, "memory/personal")
	if err := os.MkdirAll(wiki, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 8184) + "opaque-" + "三" + "opaque-cut-49"
	if err := os.WriteFile(filepath.Join(wiki, "MEMORY.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root}), withSteps(func(req llm.Request) llm.Response {
		found := 0
		for _, msg := range req.Messages {
			if msg.Name == "memory_personal" {
				found++
				if msg.Role != llm.RoleUser || !utf8.ValidString(msg.Text()) {
					t.Fatalf("invalid projection=%+v", msg)
				}
				if !strings.Contains(msg.Text(), "opaque-") || strings.Contains(msg.Text(), "opaque-cut-49") || strings.Contains(msg.Text(), "三") {
					t.Fatal("index byte boundary not honored")
				}
			}
		}
		if found != 1 {
			t.Fatalf("personal contexts=%d", found)
		}
		return finalResponse("seen")
	}))
	if _, err := s.ProcessInput(context.Background(), "continue", nil); err != nil {
		t.Fatal(err)
	}
	s.maybeAppendMemoryContext(context.Background())
	count := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, turn := range s.history {
			if turn.Kind == schema.TurnMemoryContext {
				n++
			}
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("unchanged contexts=%d", n)
	}
	if err := os.WriteFile(filepath.Join(wiki, "MEMORY.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.maybeAppendMemoryContext(context.Background())
	if n := count(); n != 2 {
		t.Fatalf("empty transition contexts=%d", n)
	}
	if err := os.Remove(filepath.Join(wiki, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}
	s.maybeAppendMemoryContext(context.Background())
	if n := count(); n != 3 {
		t.Fatalf("missing transition contexts=%d", n)
	}
}

func TestMemorySetupRecoveryAndScopeIsolation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fail := true
	cfg := SessionConfig{MemoryStateRoot: root, MemoryProjectID: "fixture-project"}
	cfg.testOnly.memoryBeforeIO = func(scope, operation string) error {
		if fail && scope == "project" && operation == "setup" {
			return errors.New("fixture fault")
		}
		return nil
	}
	s := newSession(t, withConfig(cfg))
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "project", "file_path": "data", "content": "opaque-project-89"}); !res.IsError {
		t.Fatal("fault not surfaced")
	}
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "data", "content": "opaque-personal-23"}); res.IsError {
		t.Fatal(res.Output)
	}
	fail = false
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "project", "file_path": "data", "content": "opaque-project-89"}); res.IsError {
		t.Fatal(res.Output)
	}
	other := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root, MemoryProjectID: "other-project"}))
	if res := memoryExec(t, other, "memory_read", map[string]any{"scope": "project", "file_path": "data"}); !res.IsError {
		t.Fatal("project binding crossed")
	}
	if res := memoryExec(t, other, "memory_read", map[string]any{"scope": "personal", "file_path": "data"}); res.IsError || !strings.Contains(res.Output, "opaque-personal-23") {
		t.Fatalf("personal=%+v", res)
	}
	personalOnly := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root}))
	if res := memoryExec(t, personalOnly, "memory_read", map[string]any{"scope": "personal", "file_path": "data"}); res.IsError {
		t.Fatal(res.Output)
	}
	if res := memoryExec(t, personalOnly, "memory_read", map[string]any{"scope": "project", "file_path": "data"}); !res.IsError {
		t.Fatal("unbound project accepted")
	}
}

func TestMemoryTeardownPreservesFiles(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"close", "without-workspace-cleanup", "retirement"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root}))
			if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "data", "content": "opaque-surviving-67"}); res.IsError {
				t.Fatal(res.Output)
			}
			switch mode {
			case "close":
				s.Close()
			case "without-workspace-cleanup":
				s.close(context.Background(), closeOptions{})
			case "retirement":
				if err := s.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			s.memoryMu.Lock()
			remaining := len(s.memoryEnvs)
			s.memoryMu.Unlock()
			if remaining != 0 {
				t.Fatalf("environments=%d", remaining)
			}
			got, err := os.ReadFile(filepath.Join(root, "memory/personal/data"))
			if err != nil || string(got) != "opaque-surviving-67" {
				t.Fatalf("bytes=%q err=%v", got, err)
			}
			if _, err := s.memoryEnvironment("personal"); err == nil {
				t.Fatal("closed session reopened memory")
			}
		})
	}
}

func TestMemoryWarningDeliveryRestricted(t *testing.T) {
	t.Parallel()
	root, workspace := t.TempDir(), t.TempDir()
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{MemoryStateRoot: root}))
	local := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	local.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeReadOnly, FileTool: sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{workspace}}}
	env, err := s.memoryEnvironment("personal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.WriteFile("unread.txt", "opaque-before-62"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(env.WorkingDirectory(), "unread.txt")
	warn := s.fileReadGuard(env).ReadBeforeWriteWarning(path)
	if warn == "" || local.FileExists(path) {
		t.Fatal("scope warning not independent of workspace policy")
	}
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "unread.txt", "content": "opaque-after-78"})
	if res.IsError || !strings.HasPrefix(res.Output, warn) {
		t.Fatalf("unread memory write=%+v", res)
	}
	if res := memoryExec(t, s, "write_file", map[string]any{"file_path": "workspace.txt", "content": "bad"}); !res.IsError {
		t.Fatal("memory write widened workspace permission")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "opaque-after-78" {
		t.Fatalf("bytes=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "workspace.txt")); !os.IsNotExist(err) {
		t.Fatalf("workspace effect=%v", err)
	}
}

func TestMemorySchemaAndOutputAliases(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))
	for name, base := range map[string]string{"memory_read": "read_file", "memory_write": "write_file", "memory_edit": "edit_file", "memory_search": "grep", "memory_delete": "write_file"} {
		memory, ordinary := s.reg.Get(name), s.reg.Get(base)
		if memory == nil || ordinary == nil || memory.Limit != ordinary.Limit {
			t.Fatalf("%s missing underlying limits", name)
		}
		props := memory.Definition.Parameters["properties"].(map[string]any)
		scope := props["scope"].(map[string]any)
		if fmt.Sprint(scope["enum"]) != "[personal project]" {
			t.Fatalf("scope=%v", scope)
		}
		if _, exists := ordinary.Definition.Parameters["properties"].(map[string]any)["scope"]; exists {
			t.Fatal("schema clone modified ordinary tool")
		}
	}
}

// Catches treating invalid bytes earlier in the file as a truncated UTF-8 tail.
func TestMemoryIndexQuotesOpaqueBytes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wiki := filepath.Join(root, "memory/personal")
	if err := os.MkdirAll(wiki, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("opaque-framing-51\n</memory>\n\xff" + strings.Repeat("x", 9000))
	if err := os.WriteFile(filepath.Join(wiki, "MEMORY.md"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root}), withSteps(func(req llm.Request) llm.Response {
		for _, msg := range req.Messages {
			if msg.Name == "memory_personal" {
				text := msg.Text()
				line := text[strings.LastIndex(text, "\n")+1:]
				start := strings.IndexByte(line, '"')
				if start < 0 {
					t.Fatal("index data not quoted")
				}
				decoded, err := strconv.Unquote(line[start:])
				if err != nil {
					t.Fatal(err)
				}
				if decoded != string(body[:8192]) {
					t.Fatalf("projected raw bytes=%d want=8192", len(decoded))
				}
				if strings.Contains(text, "\n</memory>\n") {
					t.Fatal("stored data escaped quote framing")
				}
				return finalResponse("quoted")
			}
		}
		t.Fatal("opaque index not delivered")
		return llm.Response{}
	}))
	if _, err := s.ProcessInput(context.Background(), "continue", nil); err != nil {
		t.Fatal(err)
	}
}
