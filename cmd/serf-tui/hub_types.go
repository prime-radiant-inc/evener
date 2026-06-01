package main

import (
	"path/filepath"
	"strings"

	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/internal/appwire"
)

type hubTreeResponse struct {
	Live     []hubTreeNode
	Projects []hubTreeProject
}

type hubTreeProject struct {
	Key         string
	Name        string
	WorkingDir  string
	RollupState string
	Sessions    []hubTreeNode
}

type hubTreeNode struct {
	Ref         string
	SessionID   string
	SourceLabel string
	Title       string
	Project     string
	State       string
	Model       string
	Age         string
	RowID       string
	CreatedAt   int64
	UpdatedAt   int64
	Live        bool
	Children    []hubTreeNode
}

type hubSessionCapabilities struct {
	Send        bool
	Steer       bool
	Interrupt   bool
	Compact     bool
	Clear       bool
	Fork        bool
	Resume      bool
	Shutdown    bool
	ChangeModel bool
	// Queue advertises support for turn/queue (kata 111a). True when a turn
	// is in flight and the source can accept an enqueued user message for
	// processing after the active turn completes.
	Queue bool
}

type hubSessionDetail struct {
	Ref              string
	SessionID        string
	SourceLabel      string
	Title            string
	State            string
	Model            string
	Profile          string
	WorkingDir       string
	Project          string
	Branch           string
	TurnCount        int
	ContextPressure  float64
	ContextUsed      int
	ContextWindow    int
	ContextRemaining int
	ActiveTurnID     string
	RecentErrors     []string
	Diagnostics      *appwire.SerfDiagnostics
	Live             bool
	Capabilities     hubSessionCapabilities
	// Queue carries the authoritative queue snapshot from
	// thread.Serf.Queue (kata r80p). hubModel mirrors this into
	// sessionQueue when entering/refreshing a session so the composer
	// preview lines up with the daemon truth without local mirroring.
	Queue appwire.QueueState
}

type hubRefResponse struct {
	Ref string
}

type hubSpawnResponse struct {
	Ref string
}

type hubSpawnRequest struct {
	Prompt          string
	Harness         string
	Model           string
	WorkingDir      string
	LaunchOverrides *appwire.LaunchConfigLayer
}

type hubForkRequest struct {
	Turn          int
	EditedMessage string
	Label         string
}

type hubTranscriptViewState struct {
	Ref      string
	Title    string
	Source   string
	Messages []transcript.ChatMessage
}

type hubSessionPanel struct {
	Body string
}

func (p hubSessionPanel) View() string {
	return strings.TrimSpace(p.Body)
}

func (t hubTranscriptViewState) banner() string {
	source := strings.TrimSpace(t.Source)
	if source != "" {
		return "Viewing " + t.Title + " [" + source + "]. Press esc to return to chat."
	}
	return "Viewing " + t.Title + ". Press esc to return to chat."
}

func hubTreeFromThreads(threads []appwire.Thread) hubTreeResponse {
	var out hubTreeResponse
	projectIndexes := map[string]int{}
	for _, thread := range threads {
		node := hubNodeFromThread(thread)
		out.Live = append(out.Live, node)
		projectName := node.Project
		key := hubProjectKey(projectName)
		idx, ok := projectIndexes[key]
		if !ok {
			idx = len(out.Projects)
			projectIndexes[key] = idx
			out.Projects = append(out.Projects, hubTreeProject{Key: key, Name: projectName, WorkingDir: thread.CWD, RollupState: node.State})
		}
		out.Projects[idx].Sessions = append(out.Projects[idx].Sessions, node)
	}
	return out
}

func hubNodeFromThread(thread appwire.Thread) hubTreeNode {
	ref := thread.Serf.Ref
	if ref == "" {
		ref = appwire.Ref{SourceID: thread.Source, ThreadID: thread.ID}.String()
	}
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	if title == "" {
		title = thread.SessionID
	}
	project := projectNameFromCWD(thread.CWD)
	return hubTreeNode{
		Ref:         ref,
		SessionID:   thread.SessionID,
		SourceLabel: sourceLabelFromRefText(ref),
		Title:       title,
		Project:     project,
		State:       thread.Status.Type,
		Model:       hubThreadModelLabel(thread),
		RowID:       "project:" + hubProjectKey(project) + ":" + ref,
		CreatedAt:   thread.CreatedAt,
		UpdatedAt:   thread.UpdatedAt,
		Live:        thread.Status.Type != appwire.ThreadStatusClosed && thread.Status.Type != appwire.ThreadStatusNotLoaded,
	}
}

func hubThreadModelLabel(thread appwire.Thread) string {
	if model := strings.TrimSpace(thread.ModelProvider); model != "" {
		return model
	}
	if provider := strings.TrimSpace(thread.Serf.Profile); provider != "" {
		return "provider: " + provider
	}
	return ""
}

func hubDetailFromThread(thread appwire.Thread) hubSessionDetail {
	node := hubNodeFromThread(thread)
	caps := thread.Serf.Capabilities
	capabilities := hubSessionCapabilities{
		Send:        caps.Send,
		Steer:       caps.Steer,
		Interrupt:   caps.Interrupt,
		Compact:     caps.Compact,
		Clear:       caps.Clear,
		Fork:        caps.ForkFromTurn,
		Shutdown:    caps.Shutdown,
		ChangeModel: caps.ChangeModel,
		Queue:       caps.Queue,
	}
	if !node.Live {
		capabilities.Send = false
		capabilities.Steer = false
		capabilities.Interrupt = false
		capabilities.Compact = false
		capabilities.Clear = false
		capabilities.Shutdown = false
		capabilities.ChangeModel = false
		capabilities.Queue = false
		capabilities.Resume = true
	}
	return hubSessionDetail{
		Ref:              node.Ref,
		SessionID:        thread.SessionID,
		SourceLabel:      node.SourceLabel,
		Title:            node.Title,
		State:            node.State,
		Model:            thread.ModelProvider,
		Profile:          thread.Serf.Profile,
		WorkingDir:       thread.CWD,
		Project:          node.Project,
		Branch:           gitBranchFromThread(thread),
		TurnCount:        len(thread.Turns),
		ActiveTurnID:     activeTurnIDFromThread(thread),
		ContextPressure:  thread.Serf.ContextPressure,
		ContextUsed:      thread.Serf.ContextUsed,
		ContextWindow:    thread.Serf.ContextWindow,
		ContextRemaining: thread.Serf.ContextRemaining,
		RecentErrors:     recentTurnErrors(thread),
		Diagnostics:      thread.Serf.Diagnostics,
		Live:             node.Live,
		Capabilities:     capabilities,
		Queue:            thread.Serf.Queue,
	}
}

func gitBranchFromThread(thread appwire.Thread) string {
	if thread.GitInfo == nil {
		return ""
	}
	return thread.GitInfo.Branch
}

func recentTurnErrors(thread appwire.Thread) []string {
	var out []string
	for _, turn := range thread.Turns {
		if turn.Error == nil {
			continue
		}
		label := turn.ID
		if label == "" {
			label = "turn"
		}
		if turn.Error.Message != "" {
			out = append(out, label+": "+turn.Error.Message)
		}
	}
	if len(out) > 3 {
		return out[len(out)-3:]
	}
	return out
}

func activeTurnIDFromThread(thread appwire.Thread) string {
	for _, turn := range thread.Turns {
		if turn.Status == appwire.TurnStatusInProgress {
			return turn.ID
		}
	}
	return ""
}

func sourceLabelFromRefText(refText string) string {
	ref, err := appwire.ParseRef(refText)
	if err != nil {
		return "serf"
	}
	return sourceLabelFromRef(ref)
}

func sourceLabelFromRef(ref appwire.Ref) string {
	if ref.SourceID == "" || ref.SourceID == "local" {
		return "serf"
	}
	return ref.SourceID
}

func projectNameFromCWD(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "(no project)"
	}
	name := filepath.Base(strings.TrimRight(cwd, string(filepath.Separator)))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return cwd
	}
	return name
}
