package appsource

import (
	"encoding/json"

	"primeradiant.com/serf/appwire"
)

func mapCodexThreadStatus(status codexThreadStatus) appwire.ThreadStatus {
	switch status.Type {
	case "active":
		return appwire.ThreadStatus{Type: appwire.ThreadStatusActive, ActiveFlags: status.ActiveFlags}
	case "idle":
		return appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}
	case "systemError":
		return appwire.ThreadStatus{Type: appwire.ThreadStatusSystemError}
	case "notLoaded", "":
		return appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded}
	default:
		return appwire.ThreadStatus{Type: status.Type, ActiveFlags: status.ActiveFlags}
	}
}

func mapCodexTurn(turn codexTurn) appwire.Turn {
	out := appwire.Turn{
		ID:          turn.ID,
		ItemsView:   firstNonEmpty(turn.ItemsView, "full"),
		Status:      mapCodexTurnStatus(turn.Status),
		StartedAt:   turn.StartedAt,
		CompletedAt: turn.CompletedAt,
		DurationMS:  turn.DurationMS,
	}
	if turn.Error != nil {
		out.Error = &appwire.TurnError{
			Message:           firstNonEmpty(turn.Error.Message, turn.Error.AdditionalDetails),
			AdditionalDetails: turn.Error.AdditionalDetails,
			Source:            "codex",
		}
		if len(turn.Error.CodexErrorInfo) > 0 {
			out.Error.CodexErrorInfo = json.RawMessage(append([]byte(nil), turn.Error.CodexErrorInfo...))
		}
	}
	for _, item := range turn.Items {
		out.Items = append(out.Items, mapCodexItem(turn.ID, item))
	}
	return out
}

func mapCodexTurnStatus(status string) string {
	switch status {
	case "inProgress":
		return appwire.TurnStatusInProgress
	case "interrupted":
		return appwire.TurnStatusInterrupted
	case "":
		return appwire.TurnStatusCompleted
	default:
		return status
	}
}

func mapCodexItem(turnID string, raw json.RawMessage) appwire.ThreadItem {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return appwire.ThreadItem{Type: "commandExecution", TurnID: turnID, Raw: raw, Error: err.Error()}
	}
	itemType := rawString(obj["type"])
	item := appwire.ThreadItem{Type: itemType, ID: rawString(obj["id"]), TurnID: turnID, Raw: raw}
	switch itemType {
	case "userMessage":
		item.Type = "userMessage"
		item.Text = codexInputText(obj["content"])
		item.Images = codexInputImages(obj["content"])
	case "agentMessage":
		item.Type = "agentMessage"
		item.Text = rawString(obj["text"])
	case "commandExecution":
		item.Type = "commandExecution"
		item.ToolName = "shell"
		item.CallID = item.ID
		item.ArgumentsJSON = jsonString(map[string]any{"command": rawString(obj["command"]), "cwd": rawString(obj["cwd"])})
		item.Output = rawString(obj["aggregatedOutput"])
		item.Status = rawString(obj["status"])
		if item.Status == "failed" {
			item.Error = "command failed"
		}
	case "mcpToolCall":
		item.Type = "commandExecution"
		item.ToolName = rawString(obj["tool"])
		item.CallID = item.ID
		item.ArgumentsJSON = string(obj["arguments"])
		item.Status = rawString(obj["status"])
		item.Output = string(obj["result"])
		item.Error = string(obj["error"])
	case "dynamicToolCall":
		item.Type = "commandExecution"
		item.ToolName = rawString(obj["tool"])
		item.CallID = item.ID
		item.ArgumentsJSON = string(obj["arguments"])
		item.Status = rawString(obj["status"])
		item.Output = string(obj["contentItems"])
	case "reasoning":
		item.Type = "reasoning"
		item.Text = rawString(obj["text"])
	default:
		item.Type = "commandExecution"
		item.ToolName = itemType
	}
	return item
}
