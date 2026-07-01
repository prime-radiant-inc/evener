package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestS3Cov_FindBuckets_AllProjects(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// Construct the <home>/serf/projects/<hash> layout stateHomeFor recognizes.
	projects := filepath.Join(home, "serf", "projects")
	current := filepath.Join(projects, "aaaa")
	sibling := filepath.Join(projects, "bbbb")
	for _, d := range []string{current, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	buckets, scope := findBuckets(current, scopeAllProjects)
	if scope != scopeAllProjects {
		t.Fatalf("scope = %q, want all_projects", scope)
	}
	if len(buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %v", buckets)
	}
}

func TestS3Cov_FormatSessionFindings(t *testing.T) {
	t.Parallel()

	t.Run("no matches", func(t *testing.T) {
		t.Parallel()
		got := formatSessionFindings(findSessionsEnvelope{ScopeApplied: scopeCurrentProject})
		if !strings.Contains(got, "No matching sessions") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("full record with parent, project, snippets, scan-truncated", func(t *testing.T) {
		t.Parallel()
		scanned := 3
		truncated := true
		env := findSessionsEnvelope{
			ScopeApplied:  scopeAllProjects,
			Scanned:       &scanned,
			ScanTruncated: &truncated,
			Matches: []sessionRecord{{
				TranscriptRef: "local:abc",
				Kind:          kindSubagent,
				Title:         "A task",
				UpdatedAt:     time.Unix(1000, 0).UTC(),
				ApproxTurns:   7,
				ParentRef:     "local:parent",
				Project:       "proj",
				IsCurrent:     true,
				Snippets:      []snippet{{Seq: 4, Role: "assistant", Snippet: "hello needle"}},
			}},
		}
		got := formatSessionFindings(env)
		for _, want := range []string{
			"1. local:abc — A task",
			"project proj",
			"· current",
			"parent: local:parent",
			"seq 4 (assistant): hello needle",
			"1 match ",
			"scanned 3",
			"scan truncated",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("findings missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("plural footer", func(t *testing.T) {
		t.Parallel()
		env := findSessionsEnvelope{
			ScopeApplied: scopeCurrentProject,
			Matches: []sessionRecord{
				{TranscriptRef: "a", Kind: kindRoot, UpdatedAt: time.Unix(0, 0).UTC()},
				{TranscriptRef: "b", Kind: kindRoot, UpdatedAt: time.Unix(0, 0).UTC()},
			},
		}
		if got := formatSessionFindings(env); !strings.Contains(got, "2 matches") {
			t.Fatalf("expected plural footer, got %q", got)
		}
	})
}

func TestS3Cov_FindBuckets(t *testing.T) {
	t.Parallel()

	t.Run("non-all scope is current project", func(t *testing.T) {
		t.Parallel()
		buckets, scope := findBuckets("/state/dir", scopeCurrentProject)
		if scope != scopeCurrentProject || len(buckets) != 1 || buckets[0] != "/state/dir" {
			t.Fatalf("got buckets=%v scope=%q", buckets, scope)
		}
	})

	t.Run("all-projects under flat dir degrades to current", func(t *testing.T) {
		t.Parallel()
		// A dir with no recognizable state-home layout → stateHomeFor == "" →
		// degrade to current project.
		buckets, scope := findBuckets(t.TempDir(), scopeAllProjects)
		if scope != scopeCurrentProject || len(buckets) != 1 {
			t.Fatalf("got buckets=%v scope=%q", buckets, scope)
		}
	})
}

func TestS3Cov_ClampFindLimit(t *testing.T) {
	t.Parallel()
	if got := clampFindLimit(nil); got != findLimitDefault {
		t.Fatalf("nil => %d, want %d", got, findLimitDefault)
	}
	zero := 0
	if got := clampFindLimit(&zero); got != findLimitDefault {
		t.Fatalf("zero => %d, want default", got)
	}
	over := findLimitMax + 100
	if got := clampFindLimit(&over); got != findLimitMax {
		t.Fatalf("over => %d, want max", got)
	}
	mid := 7
	if got := clampFindLimit(&mid); got != 7 {
		t.Fatalf("mid => %d, want 7", got)
	}
}

func TestS3Cov_SessionKind(t *testing.T) {
	t.Parallel()
	if got := sessionKind(schema.SessionMeta{IsSubagent: true}); got != kindSubagent {
		t.Fatalf("subagent => %q", got)
	}
	if got := sessionKind(schema.SessionMeta{ParentSessionID: "p"}); got != kindFork {
		t.Fatalf("fork(parent) => %q", got)
	}
	if got := sessionKind(schema.SessionMeta{DivergenceTurn: 3}); got != kindFork {
		t.Fatalf("fork(divergence) => %q", got)
	}
	if got := sessionKind(schema.SessionMeta{}); got != kindRoot {
		t.Fatalf("root => %q", got)
	}
}

func TestS3Cov_ProjectName(t *testing.T) {
	t.Parallel()
	m := schema.SessionMeta{}
	m.EnvInfo.GitOriginURL = "git@github.com:owner/serf.git"
	if got := projectName(m); got != "serf" {
		t.Fatalf("origin => %q", got)
	}
	m2 := schema.SessionMeta{}
	m2.EnvInfo.WorkingDir = "/home/jesse/git/thing"
	if got := projectName(m2); got != "thing" {
		t.Fatalf("workdir => %q", got)
	}
	if got := projectName(schema.SessionMeta{}); got != "" {
		t.Fatalf("empty => %q", got)
	}
}

func TestS3Cov_TurnRoleLabel(t *testing.T) {
	t.Parallel()
	cases := map[schema.TurnKind]string{
		schema.TurnUserInput:   "user",
		schema.TurnAssistant:   "assistant",
		schema.TurnToolResults: "tool_result",
		schema.TurnTool:        "tool_result",
		schema.TurnSteering:    "steering",
		schema.TurnSummary:     "summary",
		schema.TurnCheckpoint:  "checkpoint",
		schema.TurnSystem:      "system",
		schema.TurnKind("XyZ"): "xyz",
	}
	for kind, want := range cases {
		if got := turnRoleLabel(kind); got != want {
			t.Errorf("turnRoleLabel(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestS3Cov_RepoBasename(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"git@github.com:owner/repo.git":     "repo",
		"https://github.com/owner/repo.git": "repo",
		"https://github.com/owner/repo/":    "repo",
		"plainname":                         "plainname",
	}
	for in, want := range cases {
		if got := repoBasename(in); got != want {
			t.Errorf("repoBasename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestS3Cov_RawEntryText(t *testing.T) {
	t.Parallel()
	msg := llm.Assistant("assistant says")
	msg.Content = append(msg.Content,
		llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thinks"}},
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: "grep", Arguments: json.RawMessage(`{"pattern":"needle"}`)}},
		llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Content: "result body"}},
	)
	turn := schema.NewTurn(schema.TurnAssistant, msg)
	got := rawEntryText(turn)
	for _, want := range []string{"assistant says", "thinks", "grep", "needle", "result body"} {
		if !strings.Contains(got, want) {
			t.Errorf("rawEntryText missing %q in %q", want, got)
		}
	}
}

func TestS3Cov_MakeSnippet(t *testing.T) {
	t.Parallel()

	t.Run("centers on match with ellipses", func(t *testing.T) {
		t.Parallel()
		text := strings.Repeat("a ", 100) + "NEEDLE " + strings.Repeat("b ", 100)
		got := makeSnippet(text, "needle", 40)
		if !strings.Contains(strings.ToLower(got), "needle") {
			t.Fatalf("snippet missing needle: %q", got)
		}
		if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
			t.Fatalf("expected clipping ellipses on both ends: %q", got)
		}
	})

	t.Run("query not found returns leading width", func(t *testing.T) {
		t.Parallel()
		got := makeSnippet("hello world here", "absent", 5)
		if !strings.HasPrefix(got, "hello") {
			t.Fatalf("got %q, want leading %q", got, "hello")
		}
	})

	t.Run("match near start no leading ellipsis", func(t *testing.T) {
		t.Parallel()
		got := makeSnippet("NEEDLE at the very start of text", "needle", 40)
		if strings.HasPrefix(got, "…") {
			t.Fatalf("did not expect leading ellipsis: %q", got)
		}
	})
}
