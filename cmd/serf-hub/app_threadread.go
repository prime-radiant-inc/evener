package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/apptranscript"
	"primeradiant.com/serf/llm"
)

func pastThreadForRead(cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.Thread, bool) {
	if cfg.Past == nil {
		return appwire.Thread{}, false
	}
	threadID, ok := localPastThreadID(params)
	if !ok {
		return appwire.Thread{}, false
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.Thread{}, false
	}
	return pastEntryThread(entry, params.IncludeTurns), true
}

func localPastThreadID(params appwire.ThreadReadParams) (string, bool) {
	if params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return "", false
		}
		if ref.SourceID != "local" {
			return "", false
		}
		return ref.ThreadID, true
	}
	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" {
		return "", false
	}
	return threadID, true
}

func liveThreadCanMergeLocalPast(live appwire.Thread) bool {
	if live.Serf.Ref != "" {
		ref, err := appwire.ParseRef(live.Serf.Ref)
		return err == nil && ref.SourceID == "local"
	}
	if live.Source != "" {
		return live.Source == "local"
	}
	return true
}

func mergePastThreadForRead(cfg hubcore.WebConfig, params appwire.ThreadReadParams, live appwire.Thread) appwire.Thread {
	if !liveThreadCanMergeLocalPast(live) {
		return live
	}
	if params.ThreadID == "" && params.Ref == "" {
		switch {
		case live.Serf.Ref != "":
			params.Ref = live.Serf.Ref
		case live.ID != "":
			params.Ref = appwire.Ref{SourceID: "local", ThreadID: live.ID}.String()
		case live.SessionID != "":
			params.Ref = appwire.Ref{SourceID: "local", ThreadID: live.SessionID}.String()
		}
	}
	past, ok := pastThreadForRead(cfg, params)
	if !ok {
		return live
	}
	if live.ID == "" {
		live.ID = past.ID
	}
	if live.SessionID == "" {
		live.SessionID = past.SessionID
	}
	if live.Preview == "" || live.Preview == live.ID || live.Preview == live.SessionID {
		live.Preview = past.Preview
	}
	if live.Name == "" {
		live.Name = past.Name
	}
	if live.ModelProvider == "" {
		live.ModelProvider = past.ModelProvider
	}
	if live.CreatedAt == 0 {
		live.CreatedAt = past.CreatedAt
	}
	if live.UpdatedAt == 0 {
		live.UpdatedAt = past.UpdatedAt
	}
	if live.Path == "" {
		live.Path = past.Path
	}
	if live.CWD == "" {
		live.CWD = past.CWD
	}
	if live.Source == "" {
		live.Source = past.Source
	}
	if live.Serf.Ref == "" {
		live.Serf.Ref = past.Serf.Ref
	}
	if live.Serf.Profile == "" {
		live.Serf.Profile = past.Serf.Profile
	}
	if params.IncludeTurns && len(live.Turns) == 0 {
		live.Turns = past.Turns
	}
	return live
}

func pastEntryThread(entry hubcore.PastEntry, includeTurns bool) appwire.Thread {
	title := schema.SessionDisplayName(entry.Meta)
	if title == "" {
		title = entry.Meta.ID
	}
	cwd := entry.Meta.EnvInfo.WorkingDir
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
	parentRef := ""
	if entry.Meta.ParentSessionID != "" {
		parentRef = appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ParentSessionID}.String()
	}
	kind := "session"
	if entry.Meta.IsSubagent {
		kind = "subagent"
	} else if entry.Meta.ParentSessionID != "" {
		kind = "fork"
	}
	createdAt := hubcore.OrderCreatedAt(entry.Meta.CreatedAt, entry.Meta.UpdatedAt)
	updatedAt := hubcore.OrderUpdatedAt(entry.Meta.UpdatedAt, entry.Meta.CreatedAt)
	thread := appwire.Thread{
		ID:            entry.Meta.ID,
		SessionID:     entry.Meta.ID,
		Preview:       title,
		Name:          title,
		ModelProvider: entry.Meta.Model,
		CreatedAt:     hubcore.UnixSeconds(createdAt),
		UpdatedAt:     hubcore.UnixSeconds(updatedAt),
		Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded},
		Path:          filepath.Base(cwd),
		CWD:           cwd,
		Source:        "local",
		Serf: appwire.SerfThread{
			Ref:       ref,
			ParentRef: parentRef,
			Kind:      kind,
			Profile:   entry.Meta.ProfileID,
			Capabilities: appwire.ThreadCapabilities{
				Send:         true,
				ForkFromTurn: true,
			},
		},
	}
	if includeTurns {
		thread.Turns = pastEntryTurns(entry)
	}
	return thread
}

func pastEntryTurns(entry hubcore.PastEntry) []appwire.Turn {
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	toolNames := map[string]string{}
	return apptranscript.TurnsFromFile(transcriptPath, transcriptJSONLMaxLineBytes, func(raw json.RawMessage, turnID string, entryIndex int) []appwire.ThreadItem {
		var entryRec hubcore.ReplayEntry
		if err := json.Unmarshal(raw, &entryRec); err != nil {
			return nil
		}
		return appItemsFromReplayTurn(turnID, entryIndex, entryRec.Turn, toolNames)
	})
}

func appItemsFromReplayTurn(turnID string, turnIndex int, turn hubcore.ReplayTurn, toolNames map[string]string) []appwire.ThreadItem {
	agentTurn, imageNames := replayTurnToAgentTurn(turn)
	return apptranscript.ProjectTurn(turnID, turnIndex, agentTurn, toolNames, func(image llm.ImageData) appwire.InputItem {
		item := apptranscript.DefaultImageProjector(image)
		if len(image.Data) == 0 {
			return item
		}
		sha := imageSha(image.Data)
		item.Name = imageNames[sha]
		item.Metadata = map[string]string{
			"sha":  sha,
			"size": strconv.Itoa(len(image.Data)),
		}
		return item
	})
}

func replayTurnToAgentTurn(turn hubcore.ReplayTurn) (schema.Turn, map[string]string) {
	imageNames := map[string]string{}
	content := make([]llm.ContentPart, 0, len(turn.Message.Content))
	for _, part := range turn.Message.Content {
		switch part.Kind {
		case string(llm.ContentText):
			content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: part.Text})
		case string(llm.ContentImage):
			if part.Image == nil {
				continue
			}
			image := llm.ImageData{
				Data:      part.Image.Data,
				MediaType: part.Image.MediaType,
			}
			if len(part.Image.Data) > 0 && part.Image.Name != "" {
				imageNames[imageSha(part.Image.Data)] = part.Image.Name
			}
			content = append(content, llm.ContentPart{Kind: llm.ContentImage, Image: &image})
		case string(llm.ContentToolCall):
			if part.ToolCall == nil {
				continue
			}
			content = append(content, llm.ContentPart{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        part.ToolCall.ID,
					Name:      part.ToolCall.Name,
					Arguments: part.ToolCall.Arguments,
				},
			})
		case string(llm.ContentToolResult):
			if part.ToolResult == nil {
				continue
			}
			content = append(content, llm.ContentPart{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: part.ToolResult.ToolCallID,
					Name:       part.ToolResult.Name,
					Content:    part.ToolResult.Content,
					IsError:    part.ToolResult.IsError,
				},
			})
		}
	}
	return schema.Turn{
		Kind: schema.TurnKind(turn.Kind),
		Message: llm.Message{
			Role:    llm.Role(turn.Message.Role),
			Content: content,
		},
	}, imageNames
}
