package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
}

type hubSessionDetail struct {
	Ref             string
	SessionID       string
	SourceLabel     string
	Title           string
	State           string
	Model           string
	Profile         string
	WorkingDir      string
	Project         string
	Branch          string
	TurnCount       int
	ContextPressure float64
	ActiveTurnID    string
	RecentErrors    []string
	Diagnostics     *appwire.SerfDiagnostics
	Live            bool
	Capabilities    hubSessionCapabilities
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
	Messages []chatMessage
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
		Live:        thread.Status.Type != appwire.ThreadStatusClosed && thread.Status.Type != appwire.ThreadStatusEnded,
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
	}
	if !node.Live {
		capabilities.Send = false
		capabilities.Steer = false
		capabilities.Interrupt = false
		capabilities.Compact = false
		capabilities.Clear = false
		capabilities.Shutdown = false
		capabilities.ChangeModel = false
		capabilities.Resume = true
	}
	return hubSessionDetail{
		Ref:             node.Ref,
		SessionID:       thread.SessionID,
		SourceLabel:     node.SourceLabel,
		Title:           node.Title,
		State:           node.State,
		Model:           thread.ModelProvider,
		Profile:         thread.Serf.Profile,
		WorkingDir:      thread.CWD,
		Project:         node.Project,
		Branch:          gitBranchFromThread(thread),
		TurnCount:       len(thread.Turns),
		ActiveTurnID:    activeTurnIDFromThread(thread),
		ContextPressure: thread.Serf.ContextPressure,
		RecentErrors:    recentTurnErrors(thread),
		Diagnostics:     thread.Serf.Diagnostics,
		Live:            node.Live,
		Capabilities:    capabilities,
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
		if turn.Status == appwire.TurnStatusRunning {
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

func messagesFromThread(thread appwire.Thread) []chatMessage {
	reducer := newHubTranscriptReducer(nil, nil, nil)
	for _, turn := range thread.Turns {
		turnIndex := turnIndexFromID(turn.ID)
		for _, item := range turn.Items {
			reducer.applyThreadItem(item, turnIndex, false)
		}
		if turn.Status == appwire.TurnStatusFailed && turn.Error != nil {
			reducer.messages = append(reducer.messages, chatMessage{Kind: msgSystem, Text: formatHubTurnError(turn.Error, "Session error")})
		}
	}
	return reducer.messages
}

func threadItemToolDone(item appwire.ThreadItem, completed bool) bool {
	return completed || item.Status == "completed" || item.Output != "" || item.Error != ""
}

func toolInfoFromThreadItem(item appwire.ThreadItem, done bool) *toolCallInfo {
	desc, detail := summarizeTool(item.ToolName, item.ArgumentsJSON)
	return &toolCallInfo{
		Name:        item.ToolName,
		Description: desc,
		Detail:      detail,
		Output:      item.Output,
		Error:       item.Error,
		Done:        done,
		Duration:    itemDuration(item),
		Expanded:    detail != "" || (done && strings.Count(item.Output, "\n")+1 <= toolCollapseThreshold),
		Hidden:      item.ToolName == "communicate",
	}
}

func mergeThreadItemIntoToolInfo(info *toolCallInfo, item appwire.ThreadItem, done bool) {
	if info == nil {
		return
	}
	if item.ToolName != "" {
		info.Name = item.ToolName
		info.Hidden = item.ToolName == "communicate"
	}
	if item.ArgumentsJSON != "" || info.Description == "" {
		desc, detail := summarizeTool(item.ToolName, item.ArgumentsJSON)
		info.Description = desc
		info.Detail = detail
	}
	if item.Output != "" {
		info.Output = item.Output
	}
	if item.Error != "" {
		info.Error = item.Error
	}
	if done {
		info.Done = true
		info.Duration = itemDuration(item)
		if info.Detail != "" {
			info.Expanded = true
		} else {
			info.Expanded = strings.Count(info.Output, "\n")+1 <= toolCollapseThreshold
		}
	}
}

func turnIndexFromID(raw string) int {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "turn_")
	n, _ := strconv.Atoi(raw)
	return n
}

func itemDuration(item appwire.ThreadItem) time.Duration {
	if item.StartedAt == nil || item.CompletedAt == nil || *item.CompletedAt < *item.StartedAt {
		return 0
	}
	return time.Duration(*item.CompletedAt-*item.StartedAt) * time.Millisecond
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
