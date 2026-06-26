package transcript

import (
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/hubdiagnostics"
	"primeradiant.com/serf/cmd/serf-tui/internal/toolsummary"
)

// MessagesFromThread folds a full appwire thread into the display message list
// by replaying every item through a fresh reducer.
func MessagesFromThread(thread appwire.Thread) []ChatMessage {
	reducer := NewTranscriptReducer(nil, nil, nil)
	for _, turn := range thread.Turns {
		turnIndex := TurnIndexFromID(turn.ID)
		turnCompleted := appwire.IsTerminalTurnStatus(turn.Status)
		for _, item := range turn.Items {
			completed := turnCompleted && !appwire.IsActiveItemStatus(item.Status)
			reducer.ApplyThreadItem(item, turnIndex, completed)
		}
		if turn.Status == appwire.TurnStatusFailed && turn.Error != nil {
			reducer.messages = append(reducer.messages, ChatMessage{Kind: MsgSystem, Text: hubdiagnostics.FormatHubTurnError(turn.Error, "Session error")})
		}
	}
	return reducer.messages
}

func threadItemToolDone(item appwire.ThreadItem, completed bool) bool {
	return (completed && !appwire.IsActiveItemStatus(item.Status)) || appwire.IsTerminalItemStatus(item.Status) || item.Output != "" || item.Error != ""
}

func toolInfoFromThreadItem(item appwire.ThreadItem, done bool) *ToolCallInfo {
	desc, detail := toolsummary.SummarizeTool(item.ToolName, item.ArgumentsJSON)
	info := &ToolCallInfo{
		Name:        item.ToolName,
		Description: desc,
		Detail:      detail,
		RawArgs:     item.ArgumentsJSON,
		Raw:         string(item.Raw),
		Output:      item.Output,
		Error:       item.Error,
		Done:        done,
		Duration:    ItemDuration(item),
		Expanded:    detail != "" || (done && strings.Count(item.Output, "\n")+1 <= ToolCollapseThreshold),
		Hidden:      item.ToolName == "communicate",
	}
	if info.Name == "delegate" || info.Name == "delegate_send" {
		if run := subagentRunFromToolItem(item); run.JobID != "" || run.DelegateID != "" {
			merged := mergeSubagentRun(info.Subagent, run)
			info.Subagent = &merged
		}
	}
	return info
}

func mergeThreadItemIntoToolInfo(info *ToolCallInfo, item appwire.ThreadItem, done bool) {
	if info == nil {
		return
	}
	if item.ToolName != "" {
		info.Name = item.ToolName
		info.Hidden = item.ToolName == "communicate"
	}
	if item.ArgumentsJSON != "" || info.Description == "" {
		desc, detail := toolsummary.SummarizeTool(item.ToolName, item.ArgumentsJSON)
		info.Description = desc
		info.Detail = detail
		info.RawArgs = item.ArgumentsJSON
	}
	if item.Output != "" {
		info.Output = item.Output
	}
	if item.Error != "" {
		info.Error = item.Error
	}
	if len(item.Raw) > 0 {
		info.Raw = string(item.Raw)
	}
	if info.Name == "delegate" || info.Name == "delegate_send" {
		if run := subagentRunFromToolItem(item); run.JobID != "" || run.DelegateID != "" {
			merged := mergeSubagentRun(info.Subagent, run)
			info.Subagent = &merged
		}
	}
	if done {
		info.Done = true
		info.Duration = ItemDuration(item)
		if info.Detail != "" {
			info.Expanded = true
		} else {
			info.Expanded = strings.Count(info.Output, "\n")+1 <= ToolCollapseThreshold
		}
	}
}

// TurnIndexFromID parses the numeric turn index out of a "turn_<n>" id.
func TurnIndexFromID(raw string) int {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "turn_")
	n, _ := strconv.Atoi(raw)
	return n
}

// ItemDuration returns the wall-clock duration of a completed thread item, or
// zero when timing is unavailable or inconsistent.
func ItemDuration(item appwire.ThreadItem) time.Duration {
	if item.StartedAt == nil || item.CompletedAt == nil || *item.CompletedAt < *item.StartedAt {
		return 0
	}
	return time.Duration(*item.CompletedAt-*item.StartedAt) * time.Millisecond
}
