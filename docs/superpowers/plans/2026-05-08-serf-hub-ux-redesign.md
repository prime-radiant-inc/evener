# Serf Hub UX Redesign · Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read the design spec at `docs/superpowers/specs/2026-05-08-serf-hub-ux-redesign.md` for visual and IA detail; this plan provides the sequence, files, and acceptance criteria.

**Goal:** Rebuild `cmd/serf-hub` UI per the 2026-05-08 redesign spec. Add fork support and a few small data-model fields.

**Architecture:** 8 phases (A–H), each shippable. Phase A is data-model and fork-operation in shared packages; B is theming infra; C is the sidebar rewrite; D is the workspace rewrite; E is the spawn surface; F is search; G is settings; H is cleanup. Daemons are unchanged except for one bug-fix to `serf-hub`'s resume redirect.

**Tech Stack:** Go (`primeradiant.com/serf` module). htmx 2.0 + vanilla JS (`renderer.js`). `embed.FS` for assets and templates. `html/template` per-page sets. CSS custom properties for theming. Vendored htmx + marked, no other JS deps.

**Working from:** branch `serf-hub` in worktree `.worktrees/serf-hub`. Changes commit there. Tests must pass at every commit boundary.

---

## File map (where things land)

**Shared package changes:**
- `agent/snapshot.go` — add fork fields to `SessionMeta`.
- `agent/fork.go` (new) — `ForkSession()` operation: read prefix, write new transcript + meta.

**Hub server changes:**
- `cmd/serf-hub/web.go` — substantially rewritten: new routes, new tree builder, fork endpoint, search endpoint, settings routes.
- `cmd/serf-hub/tree.go` (new) — sidebar tree builder (Live + Projects with subagents/forks ordering).
- `cmd/serf-hub/settings.go` (new) — settings page handlers.
- `cmd/serf-hub/search.go` (new) — search endpoint.
- `cmd/serf-hub/config.go` — drop `SpawnTemplate` struct and `spawn_template` TOML.
- `cmd/serf-hub/spawn.go` — drop `findTemplate` and `Templates()`; `Spawn` accepts a direct `(provider, model, agent, working_dir, reasoning_effort)` tuple.

**Hub UI:**
- `cmd/serf-hub/templates/*.html` — substantially rewritten. New: `app.html` (single shell), `partials/sidebar.html`, `partials/workspace.html`, `partials/spawn.html`, `partials/settings/*.html`, `partials/search.html`, `partials/fork_dialog.html`. Old `landing.html`, `live.html`, `live_new.html`, `past.html`, `past_view.html` removed.
- `cmd/serf-hub/assets/style.css` — rewritten with semantic class names + CSS custom properties for light/dark themes.
- `cmd/serf-hub/assets/renderer.js` — rewritten for two-tier rendering, prose tools, edit affordance, fork dialog.
- `cmd/serf-hub/assets/init-app.js` (new) — single SPA-ish init reading `data-*` attributes.
- Old `init-live.js` and `init-past.js` removed.

---

## Phase A · Data model + fork operation

### Task A-1: Add fork fields to SessionMeta

**Files:**
- Modify: `agent/snapshot.go`
- Test: `agent/snapshot_test.go`

- [ ] **Step 1: Write a failing test that round-trips fork fields**

```go
// agent/snapshot_test.go
func TestSessionMeta_ForkFieldsRoundTrip(t *testing.T) {
    dir := t.TempDir()
    meta := SessionMeta{
        ID:                "01CHILD",
        ParentSessionID:   "01PARENT",
        DivergenceTurn:    7,
        ForkLabel:         "before TDD",
        UpdatedAt:         time.Now(),
    }
    if err := SaveSessionMeta(dir, meta); err != nil {
        t.Fatal(err)
    }
    got, err := LoadSessionMeta(filepath.Join(dir, "sessions", "01CHILD.meta.json"))
    if err != nil {
        t.Fatal(err)
    }
    if got.ParentSessionID != "01PARENT" {
        t.Errorf("ParentSessionID: %q", got.ParentSessionID)
    }
    if got.DivergenceTurn != 7 {
        t.Errorf("DivergenceTurn: %d", got.DivergenceTurn)
    }
    if got.ForkLabel != "before TDD" {
        t.Errorf("ForkLabel: %q", got.ForkLabel)
    }
}
```

- [ ] **Step 2: Run test → fails (struct fields missing)**

  `go test ./agent/ -run TestSessionMeta_ForkFieldsRoundTrip -count=1`

- [ ] **Step 3: Add fields to SessionMeta**

In `agent/snapshot.go`, on the `SessionMeta` struct (find the `OriginalTask` field as a sibling):

```go
// ParentSessionID, DivergenceTurn, and ForkLabel are non-empty on sessions
// that branched from another via the fork operation. ParentSessionID names
// the original session (the one whose transcript prefix this session shares);
// DivergenceTurn is the turn index immediately after the shared prefix
// (the first turn unique to this branch). ForkLabel, if set, is the
// user-supplied display name for the original branch.
ParentSessionID string `json:"parent_session_id,omitempty"`
DivergenceTurn  int    `json:"divergence_turn,omitempty"`
ForkLabel       string `json:"fork_label,omitempty"`
```

- [ ] **Step 4: Run test → passes**

  `go test ./agent/ -run TestSessionMeta_ForkFieldsRoundTrip -count=1`

- [ ] **Step 5: Commit**

  ```bash
  git add agent/snapshot.go agent/snapshot_test.go
  git commit -m "feat(agent): add ParentSessionID, DivergenceTurn, ForkLabel to SessionMeta"
  ```

### Task A-2: ForkSession operation in agent package

**Files:**
- Create: `agent/fork.go`
- Test: `agent/fork_test.go`

- [ ] **Step 1: Write the failing test**

```go
// agent/fork_test.go
package agent

import (
    "os"
    "path/filepath"
    "testing"
    "time"

    "primeradiant.com/serf/llm"
)

func TestForkSession_CopiesPrefixAndAppliesEdit(t *testing.T) {
    stateDir := t.TempDir()

    // Build a parent transcript with 4 user/assistant turn pairs.
    parentID := "01PARENT"
    tpath := filepath.Join(stateDir, "sessions", parentID+".transcript.jsonl")
    tw, err := NewTranscriptWriter(tpath, TranscriptHeader{
        SessionID: parentID, ProfileID: "openai", Model: "gpt-5",
    })
    if err != nil {
        t.Fatal(err)
    }
    if err := tw.Append(NewTurn(TurnUserInput, llm.User("first task"))); err != nil {
        t.Fatal(err)
    }
    if err := tw.Append(NewTurn(TurnAssistant, llm.Assistant("ack"))); err != nil {
        t.Fatal(err)
    }
    if err := tw.Append(NewTurn(TurnUserInput, llm.User("second task"))); err != nil {
        t.Fatal(err)
    }
    if err := tw.Append(NewTurn(TurnAssistant, llm.Assistant("done"))); err != nil {
        t.Fatal(err)
    }
    if err := tw.Close(); err != nil {
        t.Fatal(err)
    }
    if err := SaveSessionMeta(stateDir, SessionMeta{
        ID: parentID, UpdatedAt: time.Now(),
    }); err != nil {
        t.Fatal(err)
    }

    // Fork at turn 3 (the second user input), edit it.
    childID, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD")
    if err != nil {
        t.Fatalf("ForkSession: %v", err)
    }
    if childID == "" || childID == parentID {
        t.Fatalf("childID: %q", childID)
    }

    // Child meta has fork fields set.
    childMetaPath := filepath.Join(stateDir, "sessions", childID+".meta.json")
    childMeta, err := LoadSessionMeta(childMetaPath)
    if err != nil {
        t.Fatal(err)
    }
    if childMeta.ParentSessionID != parentID {
        t.Errorf("ParentSessionID: %q", childMeta.ParentSessionID)
    }
    if childMeta.DivergenceTurn != 3 {
        t.Errorf("DivergenceTurn: %d", childMeta.DivergenceTurn)
    }

    // Parent meta has ForkLabel set.
    parentMetaPath := filepath.Join(stateDir, "sessions", parentID+".meta.json")
    parentMeta, err := LoadSessionMeta(parentMetaPath)
    if err != nil {
        t.Fatal(err)
    }
    if parentMeta.ForkLabel != "before TDD" {
        t.Errorf("parent ForkLabel: %q", parentMeta.ForkLabel)
    }

    // Child transcript: header + 2 prefix entries + edited turn 3.
    childTPath := filepath.Join(stateDir, "sessions", childID+".transcript.jsonl")
    raw, err := os.ReadFile(childTPath)
    if err != nil {
        t.Fatal(err)
    }
    lines := splitLines(string(raw))
    if len(lines) < 4 {
        t.Fatalf("child transcript has %d lines, want >=4", len(lines))
    }
    // Last line should be the edited turn — assert it contains the edit text
    last := lines[len(lines)-1]
    if !contains(last, "second task, table-driven") {
        t.Errorf("last entry doesn't contain edit: %s", last)
    }
}

func splitLines(s string) []string {
    out := []string{}
    for _, line := range bytesSplit(s, '\n') {
        if line != "" {
            out = append(out, line)
        }
    }
    return out
}
func bytesSplit(s string, sep byte) []string {
    res := []string{}
    cur := ""
    for i := 0; i < len(s); i++ {
        if s[i] == sep {
            res = append(res, cur)
            cur = ""
        } else {
            cur += string(s[i])
        }
    }
    res = append(res, cur)
    return res
}
func contains(s, sub string) bool {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub {
            return true
        }
    }
    return false
}
```

- [ ] **Step 2: Run test → fails (function doesn't exist)**

- [ ] **Step 3: Implement ForkSession**

```go
// agent/fork.go
package agent

import (
    "bufio"
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "time"

    "primeradiant.com/serf/llm"
)

// ForkSession creates a new session whose transcript shares the parent's
// first divergenceTurn-1 turns, with a user-edited message at turn
// divergenceTurn replacing whatever was at that turn in the parent. The
// parent transcript is left unchanged on disk; the parent's metadata is
// updated to set ForkLabel for sidebar display.
//
// Returns the new (child) session ID.
//
// divergenceTurn is 1-indexed and must point at a USER_INPUT turn in the
// parent transcript.
func ForkSession(stateDir, parentID string, divergenceTurn int, editedMessage, parentForkLabel string) (string, error) {
    if divergenceTurn < 1 {
        return "", fmt.Errorf("divergenceTurn must be >= 1")
    }
    parentTPath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
    parentMetaPath := filepath.Join(stateDir, sessionsSubdir, parentID+".meta.json")

    parentMeta, err := LoadSessionMeta(parentMetaPath)
    if err != nil {
        return "", fmt.Errorf("load parent meta: %w", err)
    }

    // Read parent transcript header + prefix turns.
    f, err := os.Open(parentTPath)
    if err != nil {
        return "", fmt.Errorf("open parent transcript: %w", err)
    }
    defer f.Close()
    scanner := bufio.NewScanner(f)
    scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

    var headerLine string
    var entryLines []string  // entry lines (TranscriptEntry, kind="entry") in order
    apiCallLines := map[int][]string{}  // entries up to and including divergenceTurn-1; api_call lines stripped

    userInputCount := 0
    for scanner.Scan() {
        line := scanner.Text()
        if headerLine == "" {
            headerLine = line
            continue
        }
        var head struct {
            Kind string `json:"kind"`
            Turn struct {
                Kind string `json:"kind"`
            } `json:"turn,omitempty"`
        }
        if err := json.Unmarshal([]byte(line), &head); err != nil {
            continue
        }
        if head.Kind == "api_call" {
            // Drop api_calls — they re-record on resume; not load-bearing for fork.
            continue
        }
        if head.Kind != "entry" {
            continue
        }
        if head.Turn.Kind == string(TurnUserInput) {
            userInputCount++
            if userInputCount == divergenceTurn {
                // Stop — we keep entries strictly before this one.
                break
            }
        }
        entryLines = append(entryLines, line)
    }
    if userInputCount < divergenceTurn {
        return "", fmt.Errorf("divergenceTurn %d exceeds user-input turns in parent (%d)", divergenceTurn, userInputCount)
    }
    _ = apiCallLines  // discard; only keep prefix entries

    // Mint child ID.
    childID := newSessionID(time.Now())

    // Build child header: copy parent header, override session_id and parent fields.
    var parentHeader TranscriptHeader
    if err := json.Unmarshal([]byte(headerLine), &parentHeader); err != nil {
        return "", fmt.Errorf("parse parent header: %w", err)
    }
    childHeader := parentHeader
    childHeader.SessionID = childID
    childHeader.CreatedAt = time.Now().UTC()
    childHeader.ParentSessionID = parentID
    childHeader.ParentDivergenceTurn = divergenceTurn

    // Write child transcript via TranscriptWriter so format stays canonical.
    childTPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
    tw, err := NewTranscriptWriter(childTPath, childHeader)
    if err != nil {
        return "", fmt.Errorf("create child transcript: %w", err)
    }
    // Replay prefix entries.
    for _, line := range entryLines {
        var entry TranscriptEntry
        if err := json.Unmarshal([]byte(line), &entry); err != nil {
            tw.Close()
            return "", fmt.Errorf("parse parent entry: %w", err)
        }
        if err := tw.Append(entry.Turn); err != nil {
            tw.Close()
            return "", fmt.Errorf("write child entry: %w", err)
        }
    }
    // Append the edited turn N as USER_INPUT.
    if err := tw.Append(NewTurn(TurnUserInput, llm.User(editedMessage))); err != nil {
        tw.Close()
        return "", fmt.Errorf("write edited turn: %w", err)
    }
    if err := tw.Close(); err != nil {
        return "", fmt.Errorf("close child transcript: %w", err)
    }

    // Write child meta. Inherit parent's title and other display fields,
    // but mark fork lineage.
    childMeta := parentMeta
    childMeta.ID = childID
    childMeta.UpdatedAt = time.Now().UTC()
    childMeta.TurnCount = divergenceTurn
    childMeta.ParentSessionID = parentID
    childMeta.DivergenceTurn = divergenceTurn
    childMeta.ForkLabel = "" // child carries the original title; parent gets the label
    if err := SaveSessionMeta(stateDir, childMeta); err != nil {
        return "", fmt.Errorf("save child meta: %w", err)
    }

    // Update parent meta with the user-supplied fork label.
    if parentForkLabel != "" {
        parentMeta.ForkLabel = parentForkLabel
        parentMeta.UpdatedAt = time.Now().UTC()
        if err := SaveSessionMeta(stateDir, parentMeta); err != nil {
            return "", fmt.Errorf("update parent meta: %w", err)
        }
    }

    return childID, nil
}

// LoadSessionMeta reads a session meta file from the given path.
func LoadSessionMeta(path string) (SessionMeta, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return SessionMeta{}, err
    }
    var m SessionMeta
    if err := json.Unmarshal(data, &m); err != nil {
        return SessionMeta{}, err
    }
    return m, nil
}

// newSessionID returns a fresh ULID-shaped session id at the given time.
// Defined in session.go for live sessions; reused here for forks.
var _ = errors.New
```

  Notes:
  - `newSessionID` exists in `agent/session.go` (or wherever live IDs are minted) — reuse it. If it's unexported and lives elsewhere, expose it or re-implement using the same library.
  - `LoadSessionMeta` may already exist; if so, drop the redefinition.
  - `ParentDivergenceTurn` field on TranscriptHeader: add it next to `ParentSessionID` if not present (a sibling addition to A-1).

- [ ] **Step 4: Run test → passes**

- [ ] **Step 5: Add edge-case tests**

  - Forking at turn 1 (the very first user input).
  - Forking when the parent has no transcript file (returns error).
  - Forking with `divergenceTurn` exceeding parent's turn count (returns error).

- [ ] **Step 6: Commit**

```bash
git add agent/fork.go agent/fork_test.go agent/snapshot.go
git commit -m "feat(agent): add ForkSession to create a branched session from a parent transcript"
```

### Task A-3: Tree builder for sidebar

**Files:**
- Create: `cmd/serf-hub/tree.go`
- Test: `cmd/serf-hub/tree_test.go`

The tree builder takes the current Live roster + Past index and produces a structured payload the sidebar template renders.

- [ ] **Step 1: Write the failing test**

```go
// cmd/serf-hub/tree_test.go
package main

import (
    "testing"
    "time"

    "primeradiant.com/serf/agent"
)

func TestBuildTree_GroupsByProjectWithSubagentsAndForks(t *testing.T) {
    now := time.Now()
    metas := []agent.SessionMeta{
        // Top-level live session.
        {ID: "01PARENT", UpdatedAt: now, OriginalTask: "fix replay bug",
            EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf-hub"}},
        // Subagent of parent.
        {ID: "01SUB1", UpdatedAt: now.Add(-time.Minute), OriginalTask: "verify",
            EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
            ParentSessionID: "01PARENT", IsSubagent: true},
        // Fork of parent (preserved original).
        {ID: "01FORK1", UpdatedAt: now.Add(-2 * time.Hour),
            EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
            ParentSessionID: "01PARENT", DivergenceTurn: 7,
            ForkLabel: "before TDD"},
        // Unrelated session in same project.
        {ID: "01OTHER", UpdatedAt: now.Add(-15 * time.Minute), OriginalTask: "htmx swap",
            EnvInfo: agent.EnvironmentInfo{WorkingDir: "/projects/serf-hub"}},
    }

    tree := BuildTree(metas, []LiveEntry{
        {SessionID: "01PARENT", Status: "processing"},
        {SessionID: "01SUB1", Status: "processing"},
    })

    if len(tree.Projects) != 1 {
        t.Fatalf("projects: %d", len(tree.Projects))
    }
    proj := tree.Projects[0]
    if proj.Name != "serf-hub" {
        t.Errorf("project name: %q", proj.Name)
    }

    // Order of sessions within the project: top-level by recency, with
    // their subagents and forks immediately following.
    if len(proj.Sessions) < 2 {
        t.Fatalf("project sessions: %d", len(proj.Sessions))
    }
    if proj.Sessions[0].ID != "01PARENT" {
        t.Errorf("first session: %q", proj.Sessions[0].ID)
    }
    // Children of the parent: subagent first (indented), fork after (dim, same indent).
    if len(proj.Sessions[0].Children) != 2 {
        t.Fatalf("parent children: %d", len(proj.Sessions[0].Children))
    }
    if proj.Sessions[0].Children[0].ID != "01SUB1" {
        t.Errorf("first child: %q", proj.Sessions[0].Children[0].ID)
    }
    if proj.Sessions[0].Children[0].Kind != "subagent" {
        t.Errorf("first child kind: %q", proj.Sessions[0].Children[0].Kind)
    }
    if proj.Sessions[0].Children[1].ID != "01FORK1" {
        t.Errorf("second child: %q", proj.Sessions[0].Children[1].ID)
    }
    if proj.Sessions[0].Children[1].Kind != "fork" {
        t.Errorf("second child kind: %q", proj.Sessions[0].Children[1].Kind)
    }
    // 01OTHER is the next top-level session.
    if proj.Sessions[1].ID != "01OTHER" {
        t.Errorf("second top-level session: %q", proj.Sessions[1].ID)
    }

    // Live section: only running sessions, sorted by attention (then recency).
    if len(tree.Live) != 2 {
        t.Fatalf("live: %d", len(tree.Live))
    }
}
```

- [ ] **Step 2: Run → fails**

- [ ] **Step 3: Implement tree builder**

```go
// cmd/serf-hub/tree.go
package main

import (
    "path/filepath"
    "sort"

    "primeradiant.com/serf/agent"
)

type Tree struct {
    Live     []TreeNode
    Projects []TreeProject
}

type TreeProject struct {
    Name     string
    Sessions []TreeNode
}

// TreeNode represents a row in the sidebar. Kind is one of:
// "session" (top-level), "subagent" (purple dot, indented),
// "fork" (⎇ glyph, same indent as session, dim).
type TreeNode struct {
    ID       string
    Title    string
    Project  string
    State    string  // "awaiting" | "processing" | "warning" | "idle" | "ended"
    Kind     string  // "session" | "subagent" | "fork"
    Age      string  // pre-formatted "2m", "3h", "1d"
    Children []TreeNode
}

// BuildTree builds the sidebar render payload from session metas + live roster.
func BuildTree(metas []agent.SessionMeta, live []LiveEntry) Tree {
    liveMap := map[string]LiveEntry{}
    for _, le := range live {
        liveMap[le.SessionID] = le
    }

    // Index by ID.
    byID := map[string]agent.SessionMeta{}
    for _, m := range metas {
        byID[m.ID] = m
    }

    // Group by project.
    byProject := map[string][]agent.SessionMeta{}
    for _, m := range metas {
        proj := projectName(m.EnvInfo.WorkingDir)
        byProject[proj] = append(byProject[proj], m)
    }

    var tree Tree
    var projectNames []string
    for n := range byProject {
        projectNames = append(projectNames, n)
    }
    sort.Strings(projectNames)

    for _, proj := range projectNames {
        ms := byProject[proj]
        // Top-level sessions: those without ParentSessionID, OR with a parent
        // that's NOT in the same fork-set. (Subagents have ParentSessionID
        // AND IsSubagent=true.)
        topLevel := []agent.SessionMeta{}
        children := map[string][]agent.SessionMeta{}
        for _, m := range ms {
            if m.ParentSessionID == "" {
                topLevel = append(topLevel, m)
            } else {
                children[m.ParentSessionID] = append(children[m.ParentSessionID], m)
            }
        }

        // Sort top-level by recency (most recent first).
        sort.Slice(topLevel, func(i, j int) bool {
            return topLevel[i].UpdatedAt.After(topLevel[j].UpdatedAt)
        })

        var nodes []TreeNode
        for _, m := range topLevel {
            node := metaToNode(m, "session", liveMap)
            // Children: subagents first (sorted by recency), then forks (sorted by recency).
            var subs, forks []agent.SessionMeta
            for _, c := range children[m.ID] {
                if c.IsSubagent {
                    subs = append(subs, c)
                } else {
                    forks = append(forks, c)
                }
            }
            sort.Slice(subs, func(i, j int) bool { return subs[i].UpdatedAt.After(subs[j].UpdatedAt) })
            sort.Slice(forks, func(i, j int) bool { return forks[i].UpdatedAt.After(forks[j].UpdatedAt) })
            for _, s := range subs {
                node.Children = append(node.Children, metaToNode(s, "subagent", liveMap))
            }
            for _, f := range forks {
                node.Children = append(node.Children, metaToNode(f, "fork", liveMap))
            }
            nodes = append(nodes, node)
        }

        tree.Projects = append(tree.Projects, TreeProject{Name: proj, Sessions: nodes})
    }

    // Live: every running session, flat, sorted by attention then recency.
    for _, m := range metas {
        le, running := liveMap[m.ID]
        if !running {
            continue
        }
        node := metaToNode(m, "session", liveMap)
        node.State = le.Status  // ensure latest from roster
        tree.Live = append(tree.Live, node)
    }
    sort.SliceStable(tree.Live, func(i, j int) bool {
        return attentionRank(tree.Live[i].State) < attentionRank(tree.Live[j].State)
    })

    return tree
}

func metaToNode(m agent.SessionMeta, kind string, live map[string]LiveEntry) TreeNode {
    title := m.OriginalTask
    if title == "" {
        title = m.ID
    }
    if kind == "fork" && m.ForkLabel != "" {
        title = title + " · " + m.ForkLabel
    }
    state := "ended"
    if le, ok := live[m.ID]; ok {
        state = le.Status
    }
    return TreeNode{
        ID:      m.ID,
        Title:   title,
        Project: projectName(m.EnvInfo.WorkingDir),
        State:   state,
        Kind:    kind,
        Age:     ageString(m.UpdatedAt),
    }
}

func projectName(workingDir string) string {
    if workingDir == "" {
        return "(no project)"
    }
    return filepath.Base(workingDir)
}

func attentionRank(state string) int {
    switch state {
    case "awaiting":
        return 0
    case "processing":
        return 1
    case "warning":
        return 2
    case "idle":
        return 3
    default:
        return 4
    }
}

// ageString returns a compact age like "2m", "3h", "5d".
func ageString(t time.Time) string {
    d := time.Since(t)
    switch {
    case d < time.Minute:
        return "now"
    case d < time.Hour:
        return fmt.Sprintf("%dm", int(d.Minutes()))
    case d < 24*time.Hour:
        return fmt.Sprintf("%dh", int(d.Hours()))
    default:
        return fmt.Sprintf("%dd", int(d.Hours()/24))
    }
}
```

  Notes:
  - `LiveEntry.Status` field needs adding (today's `LiveEntry` has SessionID, PID, Address, Model, WorkingDir, SpawnedBy). Add `Status string` (sourced from prober's most recent /status sample). Update prober to record it; update `Roster.Refresh` to populate it.
  - `agent.SessionMeta.IsSubagent` field may not exist yet. If not: add `IsSubagent bool \`json:"is_subagent,omitempty"\`` to `SessionMeta` struct in `agent/snapshot.go`, and ensure the agent's spawn-subagent code sets it on the child meta.

- [ ] **Step 4: Run → passes (after stub-fixing IsSubagent + LiveEntry.Status if needed)**

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/tree.go cmd/serf-hub/tree_test.go cmd/serf-hub/roster.go agent/snapshot.go
git commit -m "feat(serf-hub): tree builder for sidebar (live + projects with subagents/forks)"
```

### Task A-4: Drop ?from= from resume redirect

**Files:** `cmd/serf-hub/web.go`

The kata #7 implementation added `?from=<id>` to the resume redirect to surface "forked from" UI. The redesign makes resume invisible (same identity throughout), so this is now redundant. Remove it.

- [ ] **Step 1: Remove the `?from=` query and the `ForkedFrom` template wiring**

In `cmd/serf-hub/web.go`, find:
```go
http.Redirect(w, r, "/live/"+sessID+"?from="+id, http.StatusFound)
```
Replace with:
```go
http.Redirect(w, r, "/live/"+sessID, http.StatusFound)
```

Remove `ForkedFrom: r.URL.Query().Get("from")` from the live drive page handler's data map (the field will be unused after the template rewrite in Phase D, but cleaning it now keeps the test surface honest).

- [ ] **Step 2: Update tests**

  Find `TestWeb_DrivePage_ShowsForkedFrom` in `cmd/serf-hub/web_test.go` and remove it (the feature it tests is being removed).

- [ ] **Step 3: Run → all hub tests pass**

- [ ] **Step 4: Commit**

```bash
git commit -m "refactor(serf-hub): drop ?from= cruft from resume redirect — resume is invisible per redesign"
```

---

## Phase B · Theme infrastructure

### Task B-1: CSS custom properties for light + dark

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (full rewrite incoming in Phase D; for now, define tokens at top)
- Modify: `cmd/serf-hub/templates/base.html`

- [ ] **Step 1: Define color tokens in style.css**

Add at the top of `style.css` (preserving existing styles below for now):

```css
/* Color tokens — both palettes ship; theme is selected by [data-theme] on :root.
   The default :root rules use prefers-color-scheme; data-theme overrides. */

:root {
  --bg: #0a0a0e;
  --bg-raised: #16161e;
  --text: #ececf0;
  --text-muted: #7a7a86;
  --text-dim: #5a5a64;
  --rule: #1a1a20;
  --accent: #7aa2f7;
  --state-awaiting: #f7768e;
  --state-processing: #7aa2f7;
  --state-warning: #e0af68;
  --state-idle: #9ece6a;
  --state-ended: #3a3a44;
  --state-subagent: #bb9af7;
}

@media (prefers-color-scheme: light) {
  :root {
    --bg: #fafafa;
    --bg-raised: #f0f0f1;
    --text: #16161e;
    --text-muted: #6a6a76;
    --text-dim: #a0a0a8;
    --rule: #e0e0e3;
    --accent: #3b6fc9;
    --state-awaiting: #c43755;
    --state-processing: #3b6fc9;
    --state-warning: #a06f1e;
    --state-idle: #3f7a1e;
    --state-ended: #c0c0c8;
    --state-subagent: #7449c7;
  }
}

:root[data-theme="light"] {
  --bg: #fafafa;
  --bg-raised: #f0f0f1;
  --text: #16161e;
  /* … (same as light mediaquery) … */
}

:root[data-theme="dark"] {
  --bg: #0a0a0e;
  --bg-raised: #16161e;
  /* … (same as default) … */
}
```

- [ ] **Step 2: Add early theme-init script to base.html**

In `cmd/serf-hub/templates/base.html`, before any other script, in `<head>` (after the `<meta>` tags):

```html
<script>
  // Theme init — runs before render to avoid flash. Reads localStorage; falls
  // back to prefers-color-scheme. data-theme on <html> drives CSS tokens.
  (function () {
    var pref = localStorage.getItem("serf-hub.theme");
    if (pref === "light" || pref === "dark") {
      document.documentElement.setAttribute("data-theme", pref);
    }
    // No data-theme → CSS uses @media prefers-color-scheme.
  })();
</script>
```

- [ ] **Step 3: Run hub, manually verify**

  Run `go test ./cmd/serf-hub/ -count=1` to make sure nothing broke.

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(serf-hub): introduce light/dark theme tokens via CSS custom properties"
```

### Task B-2: Replace hardcoded colors with tokens (existing styles)

**Files:** `cmd/serf-hub/assets/style.css`

- [ ] **Step 1: Replace literals with var() calls throughout existing style.css**

  Existing `style.css` uses hex literals like `#0c0c10`, `#15151b`, etc. Replace each with the matching token:
  - `#0c0c10` → `var(--bg)`
  - `#15151b` → `var(--bg-raised)`
  - `#e5e5e9` → `var(--text)`
  - `#9a9aa6` → `var(--text-muted)`
  - `#2a2a33` → `var(--rule)`
  - `#7aa2f7` → `var(--accent)`
  - `#f7768e` → `var(--state-awaiting)`
  - `#9ece6a` → `var(--state-idle)`
  - `#e0af68` → `var(--state-warning)`
  - `#bb9af7` → `var(--state-subagent)`

- [ ] **Step 2: Verify in browser** that both themes render correctly. Toggle via:
  ```js
  // In browser devtools console:
  document.documentElement.setAttribute("data-theme", "light");
  document.documentElement.setAttribute("data-theme", "dark");
  ```

- [ ] **Step 3: Commit**

```bash
git commit -m "style(serf-hub): replace hardcoded colors with CSS custom property tokens"
```

### Task B-3: Theme picker placeholder

A real Settings page comes in Phase G. For now, expose a programmatic theme toggle for development.

- [ ] **Step 1: Add window.serfHub.setTheme() helper to a new asset**

```js
// cmd/serf-hub/assets/theme.js
(function () {
  window.serfHub = window.serfHub || {};
  window.serfHub.setTheme = function (theme) {
    if (theme === "light" || theme === "dark") {
      document.documentElement.setAttribute("data-theme", theme);
      localStorage.setItem("serf-hub.theme", theme);
    } else {
      document.documentElement.removeAttribute("data-theme");
      localStorage.removeItem("serf-hub.theme");
    }
  };
})();
```

- [ ] **Step 2: Include in base.html**

  Add `<script src="/assets/theme.js"></script>` after the existing scripts.

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(serf-hub): add window.serfHub.setTheme(theme) helper for dev/settings use"
```

---

## Phase C · Sidebar redesign

The sidebar is replaced with the combined Live + Projects shape from the spec. Read spec sections "Two-pane layout" and "Subagents and forks in the tree" before starting.

### Task C-1: Sidebar partial template + route

**Files:**
- Create: `cmd/serf-hub/templates/partials/sidebar.html`
- Modify: `cmd/serf-hub/web.go` (add `/sidebar` partial route)

- [ ] **Step 1: Write the partial template**

  Use semantic class names: `.sidebar`, `.sidebar-header`, `.sidebar-section`, `.sidebar-section-header`, `.live-row`, `.project-section`, `.project-header`, `.project-row`, `.session-row`, `.subagent-row`, `.fork-row`, `.status-dot`, `.row-title`, `.row-meta`, `.row-age`. Render the `Tree` struct from Task A-3.

```html
{{define "sidebar"}}
<nav class="sidebar">
  <div class="sidebar-header">
    <a class="sidebar-action" href="/new">＋ new</a>
    <a class="sidebar-action" href="#" data-search-trigger>🔍 search</a>
  </div>

  {{if .Live}}
  <section class="sidebar-section">
    <header class="sidebar-section-header">
      <span>Live</span>
      <span class="row-meta">{{len .Live}}</span>
    </header>
    {{range .Live}}
    <a class="live-row session-row" data-state="{{.State}}" href="/s/{{.ID}}">
      <span class="status-dot" data-state="{{.State}}"></span>
      <span class="row-title">{{.Title}}</span>
      <span class="row-meta">{{.Project}}</span>
      <span class="row-age">{{.Age}}</span>
    </a>
    {{end}}
  </section>
  {{end}}

  {{range .Projects}}
  <section class="sidebar-section project-section">
    <header class="project-header">{{.Name}}</header>
    {{range .Sessions}}
    <a class="session-row" data-state="{{.State}}" href="/s/{{.ID}}">
      <span class="status-dot" data-state="{{.State}}"></span>
      <span class="row-title">{{.Title}}</span>
      <span class="row-age">{{.Age}}</span>
    </a>
    {{range .Children}}
      {{if eq .Kind "subagent"}}
      <a class="subagent-row" data-state="{{.State}}" href="/s/{{.ID}}">
        <span class="status-dot subagent" data-state="{{.State}}"></span>
        <span class="row-title">{{.Title}}</span>
        <span class="row-age">{{.Age}}</span>
      </a>
      {{else if eq .Kind "fork"}}
      <a class="fork-row" data-state="{{.State}}" href="/s/{{.ID}}">
        <span class="fork-glyph" data-state="{{.State}}">⎇</span>
        <span class="row-title">{{.Title}}</span>
        <span class="row-age">{{.Age}}</span>
      </a>
      {{end}}
    {{end}}
    {{end}}
  </section>
  {{end}}
</nav>
{{end}}
```

- [ ] **Step 2: Add semantic CSS for the sidebar**

  Append to `style.css`:

```css
.sidebar { width: 260px; padding: 20px 0; border-right: 1px solid var(--rule); font-size: 12px; overflow-y: auto; }
.sidebar-header { padding: 0 20px 14px; display: flex; gap: 18px; }
.sidebar-action { color: var(--text-muted); text-decoration: none; }
.sidebar-section { margin-bottom: 4px; }
.sidebar-section-header,
.project-header { padding: 14px 20px 4px; color: var(--text-dim); font-size: 10px; letter-spacing: 0.12em; text-transform: uppercase; display: flex; align-items: baseline; }
.session-row,
.subagent-row,
.fork-row,
.live-row { display: flex; align-items: baseline; padding: 5px 20px; gap: 9px; color: var(--text); text-decoration: none; }
.session-row .row-title { flex: 1; font-weight: 500; }
.subagent-row { padding-left: 48px; }
.subagent-row .row-title { flex: 1; }
.fork-row { padding-left: 20px; color: var(--text-muted); }
.fork-row .row-title { flex: 1; }
.fork-glyph { font-family: ui-monospace, monospace; color: var(--state-ended); }
.fork-row[data-state="processing"] .fork-glyph { color: var(--state-processing); }
.fork-row[data-state="awaiting"] .fork-glyph { color: var(--state-awaiting); }
.fork-row[data-state="warning"] .fork-glyph { color: var(--state-warning); }
.fork-row[data-state="idle"] .fork-glyph { color: var(--state-idle); }
.status-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--state-ended); }
.status-dot[data-state="processing"] { background: var(--state-processing); }
.status-dot[data-state="awaiting"] { background: var(--state-awaiting); }
.status-dot[data-state="warning"] { background: var(--state-warning); }
.status-dot[data-state="idle"] { background: var(--state-idle); }
.status-dot.subagent { background: var(--state-subagent); }
.row-meta { color: var(--text-muted); font-size: 11px; }
.row-age { color: var(--text-muted); font-size: 11px; margin-left: auto; }
```

- [ ] **Step 3: Wire `/sidebar` partial endpoint**

  In `cmd/serf-hub/web.go`, add a handler:

```go
mux.HandleFunc("/sidebar", s.handleSidebarPartial)
```

```go
func (s *WebServer) handleSidebarPartial(w http.ResponseWriter, r *http.Request) {
    var metas []agent.SessionMeta
    if s.cfg.Past != nil {
        metas = s.cfg.Past.AllMetas()  // returns the in-memory snapshot
    }
    var live []LiveEntry
    if s.cfg.Roster != nil {
        live = s.cfg.Roster.List()
    }
    tree := BuildTree(metas, live)
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    if err := s.sidebarTmpl.ExecuteTemplate(w, "sidebar", tree); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
    }
}
```

  Add a corresponding `sidebarTmpl` field on `WebServer`, parsed from `templates/partials/sidebar.html`. May need to add a `PastIndex.AllMetas()` method (returns the slice).

- [ ] **Step 4: Test rendering**

```go
// Add to web_test.go
func TestWeb_SidebarPartial_RendersTree(t *testing.T) {
    // Build past index with parent + subagent + fork; build roster with parent live.
    // GET /sidebar; assert response body has the project name, parent title,
    // subagent title (with subagent-row class), and fork title (with fork-row class).
}
```

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(serf-hub): sidebar partial — live + projects tree with subagents and forks"
```

### Task C-2: htmx-driven sidebar refresh

- [ ] **Step 1: Add `hx-get`/`hx-trigger` on the sidebar to auto-refresh every 5s and on app focus**

  In the new `app.html` shell (which we're building toward), wrap the sidebar in:

```html
<aside id="sidebar"
       hx-get="/sidebar"
       hx-trigger="load, every 5s, sidebar:refresh from:body"
       hx-swap="innerHTML">
  loading…
</aside>
```

  When live state changes (a daemon spawns/exits), trigger `htmx.trigger(document.body, 'sidebar:refresh')` from JS.

- [ ] **Step 2: Test refresh interaction in browser**

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(serf-hub): sidebar auto-refreshes every 5s and on demand"
```

### Task C-3: Roll-up dot on collapsed projects

When a project section is collapsed (user toggled), show a roll-up dot in the project header reflecting the most-attention-needing live child.

- [ ] **Step 1: Compute the roll-up state in `BuildTree`**

  Add `RollupState string` to `TreeProject`. Compute as the highest-attention state across the project's running sessions (or empty if no live).

- [ ] **Step 2: Render in template**

  In `project-header`, add `<span class="status-dot rollup" data-state="{{.RollupState}}"></span>{{if .RollupState}}<span class="status-dot" data-state="{{.RollupState}}"></span>{{end}}`.

  Also add a count: `<span class="row-meta">{{len .Sessions}}</span>` right-aligned.

- [ ] **Step 3: Add tests for rollup state computation**

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(serf-hub): roll-up dot on collapsed project headers"
```

### Task C-4: Click handlers — open session in workspace

Clicking a row sets the workspace to that session. Use htmx swap on the workspace pane.

- [ ] **Step 1: Add `hx-get`/`hx-target`/`hx-push-url` to row anchors**

```html
<a class="session-row"
   hx-get="/s/{{.ID}}"
   hx-target="#workspace"
   hx-swap="innerHTML"
   hx-push-url="true">…</a>
```

- [ ] **Step 2: Add `/s/<id>` endpoint that returns the workspace partial**

  (Stubbed for now — returns a placeholder. Filled in Phase D.)

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(serf-hub): wire sidebar row clicks to workspace pane via htmx"
```

### Task C-5: App shell template (replaces landing.html)

- [ ] **Step 1: Create `templates/app.html`**

```html
{{define "app"}}
<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>serf hub</title>
  <link rel="icon" href="/assets/favicon.svg">
  <link rel="stylesheet" href="/assets/style.css">
  <script>/* theme init from B-1 */</script>
</head>
<body class="app">
  <aside id="sidebar" hx-get="/sidebar" hx-trigger="load, every 5s" hx-swap="innerHTML">loading…</aside>
  <main id="workspace" hx-get="/workspace/empty" hx-trigger="load" hx-swap="innerHTML">…</main>
  <script src="/assets/htmx.min.js"></script>
  <script src="/assets/marked.min.js"></script>
  <script src="/assets/theme.js"></script>
  <script src="/assets/init-app.js"></script>
</body>
</html>
{{end}}
```

  CSS for `.app`: `display: flex; min-height: 100vh;`.

- [ ] **Step 2: Wire `/` to render `app.html`**

  Replace `s.handleIndex` to render `app.html`.

- [ ] **Step 3: Add `/workspace/empty` returning a "no session selected" partial**

  Use the spawn surface as the empty workspace (placeholder for now; built in Phase E).

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(serf-hub): single-page app shell with htmx-driven sidebar + workspace panes"
```

---

## Phase D · Workspace redesign

Read spec section "Workspace pane" before starting. The conversation body has two reading tiers; the bottom strip is compact; tool calls are prose; diffs hairline-indented; subagent references one-line.

This phase is the largest. Each task is a contained piece of the workspace.

### Task D-1: Workspace partial template skeleton

**Files:**
- Create: `cmd/serf-hub/templates/partials/workspace.html`
- Modify: `cmd/serf-hub/web.go` (add `/s/<id>` workspace handler)

- [ ] **Step 1: Write the partial template**

```html
{{define "workspace"}}
<header class="workspace-header">
  <div class="workspace-title">
    <span class="title">{{.Title}}</span>
    <a class="details-link" href="#" data-details>details</a>
  </div>
  <div class="workspace-meta">
    <span class="branch">{{.Branch}}</span>
    <span class="rule-dot">·</span>
    <span class="status-pill" data-state="{{.State}}">
      <span class="status-dot" data-state="{{.State}}"></span>
      {{.StateLabel}}
    </span>
    <span class="rule-dot">·</span>
    <span class="turn-count">{{.TurnCount}} turns</span>
  </div>
</header>

<div class="conversation"
     id="conversation"
     data-session-id="{{.ID}}"
     data-replay-url="{{.ReplayURL}}"
     data-events-url="{{.EventsURL}}"
     data-mode="{{.Mode}}"></div>

<form class="workspace-input" data-input-form>
  <textarea class="message-input" placeholder="message the agent…"></textarea>
  <div class="input-strip">
    <span class="model">{{.Model}}</span>
    <span class="rule-dot">·</span>
    <span class="context">
      <span class="context-bar"><span class="context-fill" style="width:{{.ContextPercent}}%"></span></span>
      <span class="context-numbers">{{.ContextNumbers}}</span>
    </span>
    <span class="rule-dot">·</span>
    <span class="cost">{{.Cost}}</span>
    <button class="send-btn" type="submit">send <kbd>⌘↵</kbd></button>
  </div>
</form>
{{end}}
```

- [ ] **Step 2: Add CSS for workspace skeleton**

```css
.workspace-header { padding: 14px 28px 12px; border-bottom: 1px solid var(--rule); }
.workspace-title { display: flex; align-items: baseline; gap: 14px; }
.workspace-title .title { font-weight: 500; font-size: 15px; }
.details-link { margin-left: auto; color: var(--text-muted); font-size: 12px; }
.workspace-meta { display: flex; align-items: baseline; gap: 12px; margin-top: 2px; color: var(--text-muted); font-size: 12px; }
.rule-dot { color: var(--text-dim); }
.conversation { flex: 1; padding: 32px 64px; overflow-y: auto; font-size: 15px; line-height: 1.7; }
.workspace-input { padding: 14px 28px 16px; border-top: 1px solid var(--rule); }
.message-input { width: 100%; min-height: 60px; background: transparent; border: none; resize: vertical; color: var(--text); font: inherit; outline: none; }
.input-strip { display: flex; align-items: center; gap: 14px; padding-top: 12px; margin-top: 10px; border-top: 1px solid var(--rule); font-size: 11.5px; color: var(--text-muted); }
.context-bar { display: inline-block; width: 80px; height: 2px; background: var(--rule); vertical-align: middle; }
.context-fill { display: block; height: 100%; background: var(--accent); }
.send-btn { margin-left: auto; padding: 4px 12px; background: transparent; border: 1px solid var(--rule); color: var(--text); font-size: 11.5px; cursor: pointer; }
```

  `main#workspace` needs `display: flex; flex-direction: column; flex: 1; min-width: 0;`.

- [ ] **Step 3: Add `/s/<id>` handler returning workspace partial**

```go
mux.HandleFunc("/s/", s.handleSession)

func (s *WebServer) handleSession(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/s/")
    if id == "" { http.NotFound(w, r); return }
    // Lookup session: live first, then past.
    var data WorkspaceData
    if le, ok := s.cfg.Roster.Find(id); ok {
        data = workspaceDataFromLive(le)
    } else if pe, ok := s.cfg.Past.Find(id); ok {
        data = workspaceDataFromPast(pe)
    } else {
        http.NotFound(w, r); return
    }
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    s.workspaceTmpl.ExecuteTemplate(w, "workspace", data)
}
```

- [ ] **Step 4: Tests**

  Add `TestWeb_Workspace_LiveSession_RendersHeader` and `TestWeb_Workspace_PastSession_RendersReadable`.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(serf-hub): workspace partial — title, conversation, input strip"
```

### Task D-2: Two-tier conversation rendering in renderer.js

**Files:** `cmd/serf-hub/assets/renderer.js`

The current renderer.js handles SSE events and produces DOM. Refactor for the new visual model: messages in `.message-tier`, tool calls in `.annotation-tier` (margin-indented).

- [ ] **Step 1: Add CSS for tiers**

```css
.user-message { display: flex; justify-content: flex-end; margin-bottom: 28px; }
.user-message .pill { max-width: 62%; padding: 8px 14px; background: var(--bg-raised); border-radius: 14px; font-size: 14.5px; color: var(--text); line-height: 1.55; }
.assistant-message { margin-bottom: 24px; max-width: 680px; font-size: 15px; line-height: 1.7; color: var(--text); }
.assistant-message code { background: var(--bg-raised); padding: 1px 6px; border-radius: 3px; font-family: ui-monospace, monospace; font-size: 14px; }
.tool-call { margin: 0 0 6px 28px; font-size: 12.5px; color: var(--text-muted); }
.tool-call-cluster { margin-bottom: 24px; }
.tool-call .verb { color: var(--text-muted); font-family: ui-monospace, monospace; }
.tool-call .target { color: var(--text-muted); font-family: ui-monospace, monospace; }
.tool-call.read .target,
.tool-call.grep .target,
.tool-call.list_dir .target { color: #9a9aa6; }  /* slightly lighter for cheap reads */
.tool-call .sep { color: var(--text-dim); }
.tool-call .result-good { color: var(--state-idle); }
.tool-call .result-bad { color: var(--state-awaiting); }
.diff-body { margin: 0 0 24px 44px; padding-left: 14px; border-left: 1px solid var(--rule); font-family: ui-monospace, monospace; font-size: 12px; line-height: 1.65; color: var(--text-muted); }
.diff-body .add { color: var(--state-idle); }
.diff-body .del { color: var(--state-awaiting); }
.subagent-reference { margin: 0 0 26px 28px; font-size: 12.5px; color: var(--text-muted); }
.subagent-reference .verb { color: var(--state-subagent); font-family: ui-monospace, monospace; }
```

- [ ] **Step 2: Refactor renderer.js to produce these classes**

  Replace existing `appendUserMessage`, `appendAssistantDelta`, `beginToolCall`, etc. with versions that emit the new classes. Drop the role-label divs entirely.

  Key changes from current renderer:
  - `appendUserMessage` produces `<div class="user-message"><div class="pill">…</div></div>`.
  - `beginAssistantMessage` produces `<div class="assistant-message"></div>` (no role div).
  - `beginToolCall` (cheap reads): produces `<div class="tool-call <verb>"><span class="verb">…</span> <span class="target">…</span> <span class="sep">·</span> …</div>`. Group consecutive cheap-tool calls into `.tool-call-cluster`.
  - For mutating tools, append a `<div class="diff-body">…</div>` if the call has output.
  - Subagent reference (TOOL_CALL_END for tool=`spawn_agent` or similar): emit `<div class="subagent-reference">…</div>`.

- [ ] **Step 3: Test in browser** — drive a real session, verify the new markup.

- [ ] **Step 4: Commit**

```bash
git commit -m "refactor(renderer): two-tier conversation rendering with semantic classes"
```

### Task D-3: User message edit affordance

**Files:** `cmd/serf-hub/assets/renderer.js`, `style.css`

- [ ] **Step 1: Wrap user pills with hover-affordances**

  Each user pill gets a sibling `<div class="user-message-actions">` showing on `:hover` of `.user-message` with `copy` and `✎ edit` actions. CSS:

```css
.user-message { position: relative; }
.user-message-actions { position: absolute; right: 0; top: -22px; display: none; gap: 14px; font-size: 11px; color: var(--text-muted); }
.user-message:hover .user-message-actions { display: flex; }
.user-message-actions .edit { color: var(--text); cursor: pointer; }
```

- [ ] **Step 2: Click handlers — `copy` and `edit`**

  In renderer.js: `bindUserMessageActions` adds click listeners. `copy` puts the pill text on the clipboard. `edit` switches the pill to a `contenteditable=true` textarea-like, focuses, and on `⌘↵` triggers the fork dialog (Task D-7).

- [ ] **Step 3: Test in browser**.

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(workspace): hover-reveal copy and edit affordances on user messages"
```

### Task D-4: Tool call prose + diff rendering

(See D-2; this task hardens the variants.)

- [ ] **Step 1: Map tool names to classes**

```js
const cheapTools = new Set(["read_file", "grep_files", "list_dir"]);
const mutatingTools = new Set(["edit_file", "write_file", "shell", "apply_patch"]);
```

- [ ] **Step 2: Cheap tools → `.tool-call.read` etc., one line per call. Cluster consecutive into a `.tool-call-cluster` block with one bottom margin.**

- [ ] **Step 3: Mutating tools → `.tool-call.<verb>` line + `.diff-body` (if applicable, parsed from output). For `shell`, render the output preview block instead of a diff.**

- [ ] **Step 4: Tests in browser.**

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(renderer): polished tool-call rendering — cheap one-liners, mutating with diff"
```

### Task D-5: Subagent reference + click-through

- [ ] **Step 1: When the parent's transcript shows `TOOL_CALL_END` for `spawn_agent` (or analogous), render as `.subagent-reference`. Include child session_id as `data-subagent-id`. On click, navigate `/s/<child>`.**

- [ ] **Step 2: Tests.**

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(renderer): subagent references — inline one-line, click navigates to child"
```

### Task D-6: Fork dialog

**Files:** `cmd/serf-hub/templates/partials/fork_dialog.html`, `renderer.js`, `web.go`

- [ ] **Step 1: Add fork dialog HTML** (rendered inline in the conversation body in the assistant-response slot)

```html
<div class="fork-dialog" data-fork-dialog>
  <div class="fork-dialog-title">Editing this message will fork the conversation.</div>
  <div class="fork-dialog-body">The current branch continues with your edited message; the original is preserved as a sibling fork. Label the original:</div>
  <input class="fork-dialog-label" data-fork-label-input>
  <div class="fork-dialog-actions">
    <button class="fork-cancel">cancel</button>
    <button class="fork-confirm">fork ⌘↵</button>
  </div>
</div>
```

- [ ] **Step 2: Add `POST /s/<parent>/fork` endpoint** in `web.go`:

```go
mux.HandleFunc("/s/", s.handleSession)  // already

// In handleSession:
if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/fork") {
    s.handleFork(w, r)
    return
}
```

```go
func (s *WebServer) handleFork(w http.ResponseWriter, r *http.Request) {
    parentID := /* extract from URL */
    var body struct {
        Turn          int    `json:"turn"`
        EditedMessage string `json:"edited_message"`
        Label         string `json:"label"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest); return
    }
    childID, err := agent.ForkSession(s.cfg.StateDir, parentID, body.Turn, body.EditedMessage, body.Label)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError); return
    }
    // Refresh past index so the new session shows up immediately.
    s.cfg.Past.Rebuild()
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"child_session_id": childID})
}
```

  `WebConfig` may need a `StateDir string` field. Add it.

- [ ] **Step 3: Wire renderer.js to call POST /s/<id>/fork on confirm, then navigate to child.**

- [ ] **Step 4: Test end-to-end** — fork a real session in browser, verify both branches appear in the sidebar.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(workspace,hub): fork dialog → POST /s/<id>/fork → navigate to new branch"
```

### Task D-7: Bottom strip — context bar + send

(Mostly addressed in D-1; this task wires the live values.)

- [ ] **Step 1: Render context, cost, model from `/s/<id>/state` JSON via htmx every 2s.**

```html
<div class="input-strip"
     hx-get="/s/{{.ID}}/state"
     hx-trigger="every 2s"
     hx-swap="innerHTML">…</div>
```

- [ ] **Step 2: Add `/s/<id>/state` handler returning a partial with model/context/cost.**

- [ ] **Step 3: Tests.**

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(workspace): live model/context/cost in input strip via htmx"
```

### Task D-8: Send + transparent resume

When the user sends a message in a closed session, the hub spawns/resumes the daemon, then forwards the message.

- [ ] **Step 1: Frontend — on form submit, POST to `/s/<id>/send` with `{text}`.**

- [ ] **Step 2: Backend `/s/<id>/send` handler:**

  - If session is live (in roster), forward to daemon `/input`.
  - If not, call `Spawner.Resume(ctx, id)`, wait for rendezvous, then forward.
  - Status dot transitions automatically via the SSE stream.

- [ ] **Step 3: Tests with both live and closed sessions.**

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(workspace): transparent resume — type and send wakes a closed session"
```

---

## Phase E · Spawn surface

Read spec section "Spawn surface".

### Task E-1: Spawn template + chips

**Files:**
- Create: `cmd/serf-hub/templates/partials/spawn.html`

- [ ] **Step 1: Build the template per spec.** Big task input, four chips (model, working_dir, branch, mode), advanced section, recent tasks list.

- [ ] **Step 2: Add `/new` route returning the spawn partial as the workspace innerHTML.**

- [ ] **Step 3: Tests.**

- [ ] **Step 4: Commit**

### Task E-2: Model chip — list configured models

- [ ] **Step 1: Backend — add `/api/models` returning configured models grouped by provider, derived from Config.**

- [ ] **Step 2: Frontend — clicking the model chip opens a dropdown.**

- [ ] **Step 3: Tests + commit.**

### Task E-3: Branch chip — worktree autosuggest

- [ ] **Step 1: Backend — add `/api/worktrees?dir=<path>` returning worktrees of the project (shell out to `git worktree list --porcelain`).**

- [ ] **Step 2: Frontend — branch chip dropdown with worktrees.**

- [ ] **Step 3: Commit.**

### Task E-4: Sticky defaults via localStorage

- [ ] **Step 1: On spawn-form open, read `serf-hub.spawn-defaults.<project-sha>` and pre-fill chips.**

- [ ] **Step 2: On spawn submit, write the current values back.**

- [ ] **Step 3: Commit.**

---

## Phase F · Search palette

Read spec section "Search palette".

### Task F-1: ⌘K binding + modal HTML

- [ ] **Step 1: Add a hidden `<dialog>` element to `app.html` with the search input.**
- [ ] **Step 2: Bind ⌘K (and click on `🔍 search`) to open the dialog.**
- [ ] **Step 3: Commit.**

### Task F-2: Search endpoint

- [ ] **Step 1: Add `/api/search?q=…` returning JSON with `live`, `past`, `inSession` arrays.**
- [ ] **Step 2: Live = roster filter by title contains q. Past = PastIndex.Search(q). InSession = current session's transcript text search (read latest transcript snapshot from disk, filter turns).**
- [ ] **Step 3: Tests + commit.**

### Task F-3: Modal UI render + keyboard nav

- [ ] **Step 1: Render results in three sections within the dialog.**
- [ ] **Step 2: Up/Down navigation, Enter opens, ⇧Enter jumps to turn (in-session results only — encode the turn number in the URL).**
- [ ] **Step 3: Commit.**

---

## Phase G · Settings

Read spec section "Settings".

### Task G-1: Settings shell

- [ ] **Step 1: Create `templates/partials/settings/shell.html` with left-rail nav.**
- [ ] **Step 2: `/settings` route renders shell + first pane (General).**
- [ ] **Step 3: Each section is `/settings/<name>` returning a workspace partial.**
- [ ] **Step 4: Commit.**

### Task G-2: General / Theme / Notifications panes (editable)

- [ ] **Step 1: General pane — startup defaults, hub address (read from Config).**
- [ ] **Step 2: Theme pane — light/dark/system radio. Write to localStorage.**
- [ ] **Step 3: Notifications pane — four toggles. Write to localStorage.**
- [ ] **Step 4: Commit.**

### Task G-3: Providers, Agents, Plugins (read-only inspection)

- [ ] **Step 1: Backend handlers reading from `~/.config/serf/` (or wherever truth lives) and producing the inspection panes.**
- [ ] **Step 2: Each pane shows a list with "open in editor" links per row.**
- [ ] **Step 3: Commit.**

### Task G-4: Skills + MCP servers panes

- [ ] **Step 1: MCP servers — live status from a small daemon prober (each daemon's /status reports its MCP server health). Backend computes a union.**
- [ ] **Step 2: Skills — read from plugin directories.**
- [ ] **Step 3: Commit.**

### Task G-5: Hub + Storage panes

- [ ] **Step 1: Hub pane — bind address, spawn timeout, results-per-page from Config.**
- [ ] **Step 2: Storage pane — paths and sizes (state-dir, run-dir, hub.toml).**
- [ ] **Step 3: Commit.**

### Task G-6: Notification frontend integration

- [ ] **Step 1: On every status change, update title count + favicon based on opt-ins.**
- [ ] **Step 2: For OS notification opt-in, request permission once on first opt-in. Fire on idle→awaiting and processing→errored transitions.**
- [ ] **Step 3: For sound opt-in, play a short tone (vendored audio asset).**
- [ ] **Step 4: Commit.**

---

## Phase H · Cleanup + verification

### Task H-1: Remove [[spawn_template]] and template-using code

- [ ] **Step 1: Remove `SpawnTemplate` struct from `cmd/serf-hub/config.go`.**
- [ ] **Step 2: Remove `findTemplate`, `Templates()`, `SpawnTemplate` references from `spawn.go` and `web.go`.**
- [ ] **Step 3: Update `Spawner` interface to take `(provider, model, agent, working_dir, reasoning_effort)` directly.**
- [ ] **Step 4: Update tests.**
- [ ] **Step 5: Commit.**

### Task H-2: Remove old templates and routes

- [ ] **Step 1: Delete `templates/landing.html`, `live.html`, `live_new.html`, `past.html`, `past_view.html`.**
- [ ] **Step 2: Delete `templates/partials/live_roster.html`, `past_results.html`, `status_bar.html`.**
- [ ] **Step 3: Delete `assets/init-live.js`, `init-past.js` (replaced by `init-app.js`).**
- [ ] **Step 4: Remove old route handlers from `web.go` (`handleIndex` for landing, `handleLiveRoster`, `handlePast`, `handlePastID`, `handleLiveProxy`, `handleLiveNew`, `handleStatusBar`).**
- [ ] **Step 5: Tests pass.**
- [ ] **Step 6: Commit.**

### Task H-3: README and kata bookkeeping

- [ ] **Step 1: Update `README.md` `## Serf Hub` section to reflect the redesign — describe the project tree sidebar, theming, fork model.**
- [ ] **Step 2: Close kata #18 with a comment: "out of scope per redesign — spawn templates removed entirely".**
- [ ] **Step 3: Open new kata items for any deferred work discovered during build.**
- [ ] **Step 4: Commit README.**

### Task H-4: End-to-end browser verification

- [ ] **Step 1: Use `superpowers-chrome:browsing` to drive the new hub end-to-end:**
  - Spawn a session via the new spawn surface.
  - Edit a prior message, fork.
  - Verify both branches appear in the sidebar with the right glyphs.
  - Click into the original (closed fork), type a follow-up, verify it wakes.
  - Test theme toggle.
  - Test ⌘K palette.

- [ ] **Step 2: Save a GIF of the demo flow at `docs/serf-hub-demo-redesign.gif`.**

- [ ] **Step 3: Final commit.**

```bash
git commit -m "verify(serf-hub): end-to-end browser demo of redesigned UX"
```

---

## Open questions to settle before implementation

1. ✅ **Daemon `--resume` preserving session_id** — already preserved at the agent level. No daemon changes needed.
2. **Settings → Agents pane scope** — names + locations only (recommend), vs full system-prompt + tool-allowlist preview. Decision: names + locations + "open in editor".
3. **Fork metadata location** — `agent.SessionMeta` (decision: yes — already in this plan's A-1).
4. **Fork dialog timing** — drawn as: edit → ⌘↵ → dialog → confirm. Decision: confirmed.
5. **Implementation order** — phases are sequential. Some phases (B theming, F search) can move earlier or later; the plan's order is recommended for incremental shipability.
