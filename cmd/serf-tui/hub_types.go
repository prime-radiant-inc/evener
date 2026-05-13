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
	Ref       string
	SessionID string
	Title     string
	Project   string
	State     string
	Model     string
	Age       string
	RowID     string
	Live      bool
	Children  []hubTreeNode
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
	Ref          string
	SessionID    string
	Title        string
	State        string
	Model        string
	Profile      string
	WorkingDir   string
	Project      string
	Branch       string
	TurnCount    int
	Live         bool
	Capabilities hubSessionCapabilities
}

type hubRefResponse struct {
	Ref string
}

type hubSpawnResponse struct {
	Ref string
}

type hubSpawnRequest struct {
	Task       string
	Model      string
	WorkingDir string
}

type hubForkRequest struct {
	Turn          int
	EditedMessage string
	Label         string
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
		Ref:       ref,
		SessionID: thread.SessionID,
		Title:     title,
		Project:   project,
		State:     thread.Status.Type,
		Model:     thread.ModelProvider,
		RowID:     "project:" + hubProjectKey(project) + ":" + ref,
		Live:      thread.Status.Type != appwire.ThreadStatusClosed && thread.Status.Type != appwire.ThreadStatusEnded,
	}
}

func hubDetailFromThread(thread appwire.Thread) hubSessionDetail {
	node := hubNodeFromThread(thread)
	caps := thread.Serf.Capabilities
	return hubSessionDetail{
		Ref:        node.Ref,
		SessionID:  thread.SessionID,
		Title:      node.Title,
		State:      node.State,
		Model:      thread.ModelProvider,
		Profile:    thread.Serf.Profile,
		WorkingDir: thread.CWD,
		Project:    node.Project,
		TurnCount:  len(thread.Turns),
		Live:       node.Live,
		Capabilities: hubSessionCapabilities{
			Send:        caps.Send,
			Steer:       caps.Steer,
			Interrupt:   caps.Interrupt,
			Compact:     caps.Compact,
			Clear:       caps.Clear,
			Fork:        caps.ForkFromTurn,
			Shutdown:    caps.Shutdown,
			ChangeModel: caps.ChangeModel,
		},
	}
}

func messagesFromThread(thread appwire.Thread) []chatMessage {
	var messages []chatMessage
	for _, turn := range thread.Turns {
		turnIndex := turnIndexFromID(turn.ID)
		for _, item := range turn.Items {
			switch item.Type {
			case "user_message":
				if strings.TrimSpace(item.Text) != "" {
					messages = append(messages, chatMessage{Kind: msgUser, Text: item.Text, TurnIndex: turnIndex})
				}
			case "agent_message":
				if strings.TrimSpace(item.Text) != "" {
					messages = append(messages, chatMessage{Kind: msgAssistant, Text: item.Text})
				}
			case "tool_call":
				desc, detail := summarizeTool(item.ToolName, item.ArgumentsJSON)
				messages = append(messages, chatMessage{Kind: msgTool, Tool: &toolCallInfo{
					Name:        item.ToolName,
					Description: desc,
					Detail:      detail,
					Output:      item.Output,
					Error:       item.Error,
					Done:        item.Status == "completed",
					Expanded:    detail != "" || strings.Count(item.Output, "\n")+1 <= toolCollapseThreshold,
					Hidden:      item.ToolName == "communicate",
					Duration:    itemDuration(item),
				}})
			}
		}
	}
	return messages
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
