package main

import (
	"encoding/json"
	"net/url"
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

func pastThreadForRead(cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.Thread, bool, error) {
	if cfg.Past == nil {
		return appwire.Thread{}, false, nil
	}
	threadID, ok := localPastThreadID(params)
	if !ok {
		return appwire.Thread{}, false, nil
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.Thread{}, false, nil
	}
	thread, err := pastEntryThread(cfg, entry, params.IncludeTurns)
	if err != nil {
		return thread, true, err
	}
	// One thread, one transcript: this path can afford the full-transcript
	// scans the per-entry list sweeps cannot (see stampDerivedSessionUsage).
	return stampDerivedFailureCount(entry, stampDerivedSessionUsage(entry, thread)), true, nil
}

func pastThreadReadResponse(cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, bool, error) {
	if !params.IncludeTurns || params.TurnLimit <= 0 {
		thread, ok, err := pastThreadForRead(cfg, params)
		if !ok {
			return appwire.ThreadReadResponse{}, false, err
		}
		if err != nil {
			return appwire.ThreadReadResponse{}, true, err
		}
		return windowedReadResponse(thread, params.TurnLimit), true, nil
	}
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		return appwire.ThreadReadResponse{}, false, nil
	}
	thread, err := pastEntryThread(cfg, entry, false)
	if err != nil {
		return appwire.ThreadReadResponse{}, true, err
	}
	var olderCursor string
	thread.Turns, olderCursor, err = pastEntryLatestTurns(entry, params.TurnLimit)
	if err != nil {
		return appwire.ThreadReadResponse{}, true, err
	}
	thread = stampDerivedFailureCount(entry, stampDerivedSessionUsage(entry, reconcileAndEnrichPastThread(entry, thread)))
	return appwire.ThreadReadResponse{Thread: thread, OlderCursor: olderCursor}, true, nil
}

func pastThreadTurnsList(cfg hubcore.WebConfig, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, bool, error) {
	readParams := appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: true}
	if params.Limit <= 0 {
		thread, ok, err := pastThreadForRead(cfg, readParams)
		if !ok {
			return appwire.ThreadTurnsListResponse{}, false, err
		}
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, true, err
		}
		return appwire.PageTurns(thread.Turns, params.Cursor, params.Limit), true, nil
	}
	entry, ok := pastEntryForRead(cfg, readParams)
	if !ok {
		return appwire.ThreadTurnsListResponse{}, false, nil
	}
	page, err := pastEntryPageTurns(entry, params.Cursor, params.Limit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, true, err
	}
	thread := reconcileAndEnrichPastThread(entry, appwire.Thread{ID: entry.Meta.ID, SessionID: entry.Meta.ID, CWD: entry.Meta.EnvInfo.WorkingDir, Turns: page.Data})
	page.Data = thread.Turns
	return page, true, nil
}

func pastEntryForRead(cfg hubcore.WebConfig, params appwire.ThreadReadParams) (hubcore.PastEntry, bool) {
	if cfg.Past == nil {
		return hubcore.PastEntry{}, false
	}
	threadID, ok := localPastThreadID(params)
	if !ok {
		return hubcore.PastEntry{}, false
	}
	return cfg.Past.Find(threadID)
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

func mergePastThreadForRead(cfg hubcore.WebConfig, params appwire.ThreadReadParams, live appwire.Thread) (appwire.Thread, error) {
	if !liveThreadCanMergeLocalPast(live) {
		return live, nil
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
	past, ok, err := pastThreadForRead(cfg, params)
	if err != nil {
		return appwire.Thread{}, err
	}
	if !ok {
		return live, nil
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
	return live, nil
}

func pastEntryThread(cfg hubcore.WebConfig, entry hubcore.PastEntry, includeTurns bool) (appwire.Thread, error) {
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
	status := appwire.ThreadStatusNotLoaded
	if cfg.Roster != nil && cfg.Roster.IsSubagentActive(entry.Meta.ID) {
		status = appwire.ThreadStatusActive
	}
	// cumulativeUsage is the persisted full-session token total; the cost stamp
	// derives its "~$X.XX" from it at the session model's price (empty when
	// there is no usage or the model is uncataloged), mirroring the per-turn
	// cost stamp in pastEntryTurns and the live producer in server's appThread.
	//
	// A meta without it leaves both absent HERE, because this function also
	// runs once per entry on the list and transcript-target sweeps. The
	// single-thread read paths recover the figure from the transcript instead —
	// see stampDerivedSessionUsage.
	cumulativeUsage := serfUsageFromCumulative(entry.Meta.CumulativeUsage)
	thread := appwire.Thread{
		ID:            entry.Meta.ID,
		SessionID:     entry.Meta.ID,
		Preview:       title,
		Name:          title,
		ModelProvider: entry.Meta.Model,
		CreatedAt:     hubcore.UnixSeconds(createdAt),
		UpdatedAt:     hubcore.UnixSeconds(updatedAt),
		Status:        appwire.ThreadStatus{Type: status},
		Path:          filepath.Base(cwd),
		CWD:           cwd,
		Source:        "local",
		Serf: appwire.SerfThread{
			Ref:       ref,
			ParentRef: parentRef,
			Kind:      kind,
			Profile:   entry.Meta.ProfileID,
			// A past/exited thread advertises exactly the actions the hub can
			// carry out for it once qp94's auto-resume runs: the resume-and-retry
			// session mutations (compact, clear, change model, shutdown) plus the
			// always-available ones (send, fork, goal, rename). Steer, Interrupt,
			// and Queue stay false — they gate on an active turn a cold exited
			// session has none of, so the hub deliberately does not resume for
			// them (kata xr4x trues this up to qp94's wiring).
			Capabilities: appwire.ThreadCapabilities{
				Send:         true,
				ForkFromTurn: true,
				Compact:      true,
				Clear:        true,
				ChangeModel:  true,
				Shutdown:     true,
				Goal:         true,
				Rename:       true,
			},
			WorkMillis: entry.Meta.WorkMillis,
			Usage:      cumulativeUsage,
			Cost:       appwire.EstimateCost(entry.Meta.Model, cumulativeUsage),
			// ActiveTurnStartedAt stays 0 because the parent status payload does not
			// expose the in-process child's turn start time.
		},
	}
	if includeTurns {
		var err error
		thread.Turns, err = pastEntryTurns(entry)
		if err != nil {
			return appwire.Thread{}, err
		}
		thread = reconcileAndEnrichPastThread(entry, thread)
	}
	annotateThreadProjects([]appwire.Thread{thread})
	return thread, nil
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

// stampDerivedSessionUsage fills in a session token total the meta does not
// carry, by summing the session's own span of its FULL transcript.
//
// The gap is the common case, not an edge: agent/fork.go's writeForkChild builds
// the child SessionMeta field by field and stamps no CumulativeUsage at all, so
// every fork child arrives with the field unset, and serf.usage and serf.cost
// both empty. The client can then only sum the turns it happens to hold, which
// it must honestly label "tokens (loaded turns)". The transcript records
// per-turn usage regardless, so summing it recovers the full-session figure —
// and because it reads the whole file rather than the loaded window, the total
// does not shrink with thread/read's turnLimit.
//
// A fork child's transcript OPENS with a verbatim copy of the parent's prefix,
// whose tokens the PARENT spent. DivergenceTurn marks where the child's own
// history begins, and only that span is counted: charging the parent's spend to
// the child would be fabrication.
//
// A present total is left alone: it is the daemon's own running count, and
// re-deriving would invite a second, disagreeing figure.
//
// Applied only on the single-thread read paths. pastEntryThread also runs once
// per entry on the thread-list and transcript-target sweeps, where a scan per
// session would cost a read of every transcript in the state dir.
//
// A read error (a legacy format_version 1 transcript, a missing file) leaves the
// total absent. "Unknown" is the honest report, the client already renders an
// absent total as nothing rather than "↑0 ↓0", and a missing token figure is no
// reason to fail the whole thread projection.
func stampDerivedSessionUsage(entry hubcore.PastEntry, thread appwire.Thread) appwire.Thread {
	if thread.Serf.Usage != nil {
		return thread
	}
	total := derivedSessionUsage(entry)
	if total == nil {
		return thread
	}
	thread.Serf.Usage = total
	thread.Serf.Cost = appwire.EstimateCost(entry.Meta.Model, total)
	return thread
}

// derivedSessionUsage is stampDerivedSessionUsage's sum, for the legacy web
// surface that assembles its own WorkspaceData rather than an appwire.Thread.
// Returns nil for an absent total, on the same terms.
func derivedSessionUsage(entry hubcore.PastEntry) *appwire.SerfUsage {
	total, err := pastTranscriptCache.UsageTotalFromFile(pastTranscriptPath(entry), transcriptJSONLMaxLineBytes, entry.Meta.DivergenceTurn)
	if err != nil {
		return nil
	}
	return total
}

// stampDerivedFailureCount reports how many of the session's tool calls failed,
// by scanning its own span of the FULL transcript.
//
// It is derived server-side because the client cannot derive it honestly. A
// windowed thread/read hands the client a suffix of the session — measured at
// about 47% of a long real session's document at load (kata hw2n) — and a count
// over that suffix is a partial figure wearing a full-session label. For
// failures that is worse than saying nothing: the harm the count exists to fix
// is a reader concluding a run was clean because they had not yet scrolled to
// the failure, and a "0 failed" computed from the loaded window states exactly
// that conclusion in the session's own chrome.
//
// A fork child's transcript OPENS with a verbatim copy of the parent's prefix,
// whose failures the PARENT made. DivergenceTurn marks where the child's own
// history begins, and only that span is counted — the same attribution rule
// stampDerivedSessionUsage applies to tokens.
//
// Applied only on the single-thread read paths, for the reason
// stampDerivedSessionUsage states: pastEntryThread also runs once per entry on
// the thread-list and transcript-target sweeps, where a scan per session costs a
// read of every transcript in the state dir.
//
// A read error (a legacy format_version 1 transcript, a missing file) leaves the
// count ABSENT. Unknown is the honest report, the client renders an absent count
// as nothing, and a session nobody can read must not be reported as clean.
func stampDerivedFailureCount(entry hubcore.PastEntry, thread appwire.Thread) appwire.Thread {
	count, err := pastTranscriptCache.FailedToolCallsFromFile(pastTranscriptPath(entry), transcriptJSONLMaxLineBytes, entry.Meta.DivergenceTurn)
	if err != nil {
		return thread
	}
	thread.Serf.FailedToolCalls = &count
	return thread
}

// pastTranscriptPath is where a saved session's transcript lives.
func pastTranscriptPath(entry hubcore.PastEntry) string {
	return filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
}

func pastEntryTurns(entry hubcore.PastEntry) ([]appwire.Turn, error) {
	transcriptPath := pastTranscriptPath(entry)
	toolNames := map[string]string{}
	turns, err := pastTranscriptCache.TurnsFromFile(transcriptPath, transcriptJSONLMaxLineBytes, func(raw json.RawMessage, turnID string, entryIndex int) []appwire.ThreadItem {
		var entryRec hubcore.ReplayEntry
		if err := json.Unmarshal(raw, &entryRec); err != nil {
			return nil
		}
		return appItemsFromReplayTurn(entry.Meta.ID, turnID, entryIndex, entryRec.Turn, toolNames)
	})
	if err != nil {
		return nil, err
	}
	// TurnsFromFile only has the per-round usage persisted in the transcript;
	// it doesn't know the session's model, so the cost estimate is stamped
	// here as a post-pass against entry.Meta.Model.
	for i := range turns {
		if turns[i].Usage != nil {
			turns[i].Cost = appwire.EstimateCost(entry.Meta.Model, turns[i].Usage)
		}
	}
	return turns, nil
}

func projectBoundedPastTranscriptTurn(raw json.RawMessage, turnID string, entryIndex int, toolNames map[string]string) []appwire.ThreadItem {
	var entryRec hubcore.ReplayEntry
	if err := json.Unmarshal(raw, &entryRec); err != nil {
		return nil
	}
	return appItemsFromReplayTurn("", turnID, entryIndex, entryRec.Turn, toolNames)
}

func pastEntryLatestTurns(entry hubcore.PastEntry, limit int) ([]appwire.Turn, string, error) {
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	turns, cursor, err := pastTranscriptCache.LatestFromFile(path, transcriptJSONLMaxLineBytes, limit, projectBoundedPastTranscriptTurn)
	if err != nil {
		return nil, "", err
	}
	stampPastEmbeddedImageURLs(entry.Meta.ID, turns)
	stampPastTurnCosts(entry.Meta.Model, turns)
	return turns, cursor, nil
}

func pastEntryPageTurns(entry hubcore.PastEntry, cursor string, limit int) (appwire.ThreadTurnsListResponse, error) {
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	page, err := pastTranscriptCache.PageFromFile(path, transcriptJSONLMaxLineBytes, cursor, limit, projectBoundedPastTranscriptTurn)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	stampPastEmbeddedImageURLs(entry.Meta.ID, page.Turns)
	stampPastTurnCosts(entry.Meta.Model, page.Turns)
	return appwire.ThreadTurnsListResponse{Data: page.Turns, NextCursor: page.NextCursor}, nil
}

func stampPastTurnCosts(model string, turns []appwire.Turn) {
	for i := range turns {
		if turns[i].Usage != nil {
			turns[i].Cost = appwire.EstimateCost(model, turns[i].Usage)
		}
	}
}

func stampPastEmbeddedImageURLs(sessionID string, turns []appwire.Turn) {
	for ti := range turns {
		for ii := range turns[ti].Items {
			for oi := range turns[ti].Items[ii].OutputImages {
				image := &turns[ti].Items[ii].OutputImages[oi]
				if image.URL == "/s//images/"+image.SHA {
					image.URL = "/s/" + url.PathEscape(sessionID) + "/images/" + image.SHA
				}
			}
		}
	}
}

func reconcileAndEnrichPastThread(entry hubcore.PastEntry, thread appwire.Thread) appwire.Thread {
	if jobsByID, err := agent.LoadSessionHistoricalJobRecords(entry.StateDir, entry.Meta.ID); err == nil {
		thread = reconcileDelegateThreadItems(thread, jobsByID)
	}
	return enrichThreadFileBackedOutputImages(thread)
}

func appItemsFromReplayTurn(sessionID, turnID string, turnIndex int, turn hubcore.ReplayTurn, toolNames map[string]string) []appwire.ThreadItem {
	agentTurn, imageNames := replayTurnToAgentTurn(turn)
	return apptranscript.ProjectTurn(turnID, turnIndex, agentTurn, toolNames, func(image llm.ImageData) appwire.InputItem {
		return projectReplayInputImage(image, imageNames)
	}, func(result *llm.ToolResultData) []appwire.OutputImage {
		return projectReplayOutputImages(sessionID, result)
	})
}

func projectReplayInputImage(image llm.ImageData, imageNames map[string]string) appwire.InputItem {
	item := apptranscript.DefaultImageProjector(image)
	if len(image.Data) == 0 {
		return item
	}
	sha := imageSha(image.Data)
	item.Name = imageNames[sha]
	item.Metadata = map[string]string{"sha": sha, "size": strconv.Itoa(len(image.Data))}
	return item
}

func projectReplayOutputImages(sessionID string, result *llm.ToolResultData) []appwire.OutputImage {
	if result == nil || len(result.ImageData) == 0 {
		return nil
	}
	sha := imageSha(result.ImageData)
	mediaType := result.ImageMediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	return []appwire.OutputImage{{
		Source: "tool-result", Name: result.Name, MediaType: mediaType,
		Size: int64(len(result.ImageData)), SHA: sha,
		URL: "/s/" + url.PathEscape(sessionID) + "/images/" + sha,
	}}
}

func enrichThreadFileBackedOutputImages(thread appwire.Thread) appwire.Thread {
	sessionID := strings.TrimSpace(thread.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(thread.ID)
	}
	cwd := strings.TrimSpace(thread.CWD)
	if sessionID == "" || cwd == "" || len(thread.Turns) == 0 {
		return thread
	}
	argsByCallID := map[string]string{}
	for ti := range thread.Turns {
		for ii := range thread.Turns[ti].Items {
			item := thread.Turns[ti].Items[ii]
			if item.Type != "commandExecution" {
				continue
			}
			if item.CallID != "" && item.ArgumentsJSON != "" {
				argsByCallID[item.CallID] = item.ArgumentsJSON
			}
			if item.Status != appwire.TurnStatusCompleted {
				continue
			}
			argsJSON := item.ArgumentsJSON
			if argsJSON == "" && item.CallID != "" {
				argsJSON = argsByCallID[item.CallID]
			}
			fileBacked := outputImagesForToolCall(sessionID, cwd, item.ToolName, argsJSON, item.Output)
			if len(fileBacked) == 0 {
				continue
			}
			item.OutputImages = appendOutputImagesUnique(item.OutputImages, fileBacked)
			thread.Turns[ti].Items[ii] = item
		}
	}
	return thread
}

func appendOutputImagesUnique(existing, extra []appwire.OutputImage) []appwire.OutputImage {
	if len(extra) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(extra))
	for _, img := range existing {
		key := outputImageDescriptorKey(img)
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
	}
	out := existing
	for _, img := range extra {
		key := outputImageDescriptorKey(img)
		if key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, img)
	}
	return out
}

func outputImageDescriptorKey(img appwire.OutputImage) string {
	if img.URL != "" {
		return img.URL
	}
	if img.SHA != "" {
		return "sha:" + img.SHA
	}
	if img.Path != "" {
		return "path:" + img.Path
	}
	return ""
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
					ToolCallID:     part.ToolResult.ToolCallID,
					Name:           part.ToolResult.Name,
					Content:        part.ToolResult.Content,
					IsError:        part.ToolResult.IsError,
					ToolState:      part.ToolResult.ToolState,
					ImageData:      part.ToolResult.ImageData,
					ImageMediaType: part.ToolResult.ImageMediaType,
				},
			})
		}
	}
	return schema.Turn{
		Kind:           schema.TurnKind(turn.Kind),
		Timestamp:      turn.Timestamp,
		SteeringSource: turn.SteeringSource,
		Error:          replayTurnFailureInfo(turn.Error),
		Message: llm.Message{
			Role:    llm.Role(turn.Message.Role),
			Content: content,
		},
	}, imageNames
}

// replayTurnFailureInfo restores a failed turn's persisted diagnostic. Without
// it a reloaded failure would reach the client with its message alone, losing
// the source/title/hint/cause the live error event carried (kata mcgh).
func replayTurnFailureInfo(replayed *hubcore.ReplayTurnError) *schema.TurnFailureInfo {
	if replayed == nil {
		return nil
	}
	info := &schema.TurnFailureInfo{
		Message: replayed.Message,
		Source:  replayed.Source,
		Title:   replayed.Title,
		Hint:    replayed.Hint,
	}
	if replayed.Cause != nil {
		info.Cause = &schema.TurnFailureCause{
			Kind:     replayed.Cause.Kind,
			Provider: replayed.Cause.Provider,
			Model:    replayed.Cause.Model,
			Status:   replayed.Cause.Status,
		}
	}
	return info
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
	case "completed", "failed", "cancelled", "stopped", "exhausted":
		return true
	default:
		return false
	}
}
