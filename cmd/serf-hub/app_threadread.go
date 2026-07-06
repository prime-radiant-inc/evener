package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent"
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
				Goal:         true,
				Rename:       true,
			},
		},
	}
	if includeTurns {
		thread.Turns = pastEntryTurns(entry)
		if jobsByID, err := agent.LoadSessionHistoricalJobRecords(entry.StateDir, entry.Meta.ID); err == nil {
			thread = reconcileDelegateThreadItems(thread, jobsByID)
		}
	}
	return thread
}

// windowedReadResponse bounds a thread's turns to the latest TurnLimit for a
// lazy initial load, setting OlderCursor when it truncates. TurnLimit <= 0
// returns the full transcript (legacy behavior).
func windowedReadResponse(thread appwire.Thread, turnLimit int) appwire.ThreadReadResponse {
	turns, cursor := appwire.WindowTurns(thread.Turns, turnLimit)
	thread.Turns = turns
	return appwire.ThreadReadResponse{Thread: thread, OlderCursor: cursor}
}

// pastTranscriptCache memoizes saved-transcript parsing by file identity. Past
// transcripts are immutable, so lazy paging back through one (a fresh
// thread/turns/list file read per page) reuses one parse instead of re-reading
// the whole transcript each page.
var pastTranscriptCache = apptranscript.NewTurnCache()

func pastEntryTurns(entry hubcore.PastEntry) []appwire.Turn {
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	toolNames := map[string]string{}
	turns := pastTranscriptCache.TurnsFromFile(transcriptPath, transcriptJSONLMaxLineBytes, func(raw json.RawMessage, turnID string, entryIndex int) []appwire.ThreadItem {
		var entryRec hubcore.ReplayEntry
		if err := json.Unmarshal(raw, &entryRec); err != nil {
			return nil
		}
		return appItemsFromReplayTurn(turnID, entryIndex, entryRec.Turn, toolNames)
	})
	// TurnsFromFile only has the per-round usage persisted in the transcript;
	// it doesn't know the session's model, so the cost estimate is stamped
	// here as a post-pass against entry.Meta.Model.
	for i := range turns {
		if turns[i].Usage != nil {
			turns[i].Cost = appwire.EstimateCost(entry.Meta.Model, turns[i].Usage)
		}
	}
	return turns
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
		case string(llm.ContentThinking):
			thinking := &llm.ThinkingData{}
			if part.Thinking != nil {
				thinking.Text = part.Thinking.Text
			}
			content = append(content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: thinking})
		case string(llm.ContentRedThinking):
			content = append(content, llm.ContentPart{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{Redacted: true}})
		case string(llm.ContentAudio):
			if part.Audio == nil {
				continue
			}
			content = append(content, llm.ContentPart{Kind: llm.ContentAudio, Audio: &llm.AudioData{
				URL:       part.Audio.URL,
				MediaType: part.Audio.MediaType,
			}})
		case string(llm.ContentDocument):
			if part.Document == nil {
				continue
			}
			content = append(content, llm.ContentPart{Kind: llm.ContentDocument, Document: &llm.DocumentData{
				URL:       part.Document.URL,
				MediaType: part.Document.MediaType,
				FileName:  part.Document.FileName,
			}})
		case string(llm.ContentWebSearch):
			if part.WebSearch == nil {
				continue
			}
			content = append(content, llm.ContentPart{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{
				Query: part.WebSearch.Query,
				Raw:   part.WebSearch.Raw,
			}})
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
					ToolState:  part.ToolResult.ToolState,
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

func reconcileDelegateThreadItemForTest(item appwire.ThreadItem, rec agent.HistoricalJobRecord) appwire.ThreadItem {
	return reconcileDelegateThreadItem(item, rec)
}

func reconcileDelegateThreadItems(thread appwire.Thread, jobsByID map[string]agent.HistoricalJobRecord) appwire.Thread {
	if len(jobsByID) == 0 || len(thread.Turns) == 0 {
		return thread
	}
	var turns []appwire.Turn
	clonedItems := map[int]bool{}
	for ti := range thread.Turns {
		for ii := range thread.Turns[ti].Items {
			item := thread.Turns[ti].Items[ii]
			if item.Type != "commandExecution" || item.ToolName != "delegate" {
				continue
			}
			jobID := delegateJobIDFromRaw(item.Raw)
			if jobID == "" {
				continue
			}
			rec, ok := jobsByID[jobID]
			if !ok {
				continue
			}
			reconciled := reconcileDelegateThreadItem(item, rec)
			if turns == nil {
				turns = append([]appwire.Turn(nil), thread.Turns...)
			}
			if !clonedItems[ti] {
				turns[ti].Items = append([]appwire.ThreadItem(nil), thread.Turns[ti].Items...)
				clonedItems[ti] = true
			}
			turns[ti].Items[ii] = reconciled
		}
	}
	if turns != nil {
		thread.Turns = turns
	}
	return thread
}

func reconcileDelegateThreadItem(item appwire.ThreadItem, rec agent.HistoricalJobRecord) appwire.ThreadItem {
	if item.Type != "commandExecution" || item.ToolName != "delegate" || rec.JobID == "" || rec.Type != "delegate" || !isTerminalHistoricalJobStatus(rec.Status) {
		return item
	}
	var raw map[string]any
	if len(item.Raw) != 0 {
		_ = json.Unmarshal(item.Raw, &raw)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	rawJobID, _ := raw["job_id"].(string)
	if rawJobID != "" && rawJobID != rec.JobID {
		return item
	}
	raw["job_id"] = rec.JobID
	if rec.DelegateID != "" {
		raw["delegate_id"] = rec.DelegateID
	}
	if rec.Task != "" {
		raw["task"] = rec.Task
	}
	if rec.TranscriptRef != "" {
		raw["transcript_ref"] = rec.TranscriptRef
	}
	if rec.OriginTurnID != "" {
		raw["origin_turn_id"] = rec.OriginTurnID
	}
	if rec.OriginToolCallID != "" {
		raw["origin_tool_call_id"] = rec.OriginToolCallID
	}
	if rec.OriginItemID != "" {
		raw["origin_item_id"] = rec.OriginItemID
	}
	if rec.Status != "" {
		raw["status"] = rec.Status
		item.Status = rec.Status
	}
	if rec.Reason != "" {
		raw["reason"] = rec.Reason
	}
	raw["output_bytes"] = rec.OutputBytes
	if b, err := json.Marshal(raw); err == nil {
		item.Raw = b
	}
	return item
}

func delegateJobIDFromRaw(raw json.RawMessage) string {
	var payload struct {
		JobID        string `json:"job_id"`
		StartedJobID string `json:"started_job_id"`
		CurrentJobID string `json:"current_job_id"`
		LatestJobID  string `json:"latest_job_id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	for _, value := range []string{payload.JobID, payload.StartedJobID, payload.CurrentJobID, payload.LatestJobID} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isTerminalHistoricalJobStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "stopped":
		return true
	default:
		return false
	}
}
