package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/rendezvous"
)

func newHubSourceRegistry(cfg WebConfig) *appsource.Registry {
	registry := appsource.NewRegistry()
	registry.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		if cfg.Roster != nil {
			live := cfg.Roster.List()
			entries := make([]appsource.LocalDaemonEntry, 0, len(live))
			for _, item := range live {
				if strings.EqualFold(item.Status, "CLOSED") {
					continue
				}
				entries = append(entries, appsource.LocalDaemonEntry{
					Entry:     item.Entry,
					SessionID: item.SessionID,
					Status:    item.Status,
				})
			}
			return entries
		}
		if cfg.RunDir == "" {
			return nil
		}
		raw, _ := rendezvous.List(cfg.RunDir)
		entries := make([]appsource.LocalDaemonEntry, 0, len(raw))
		for _, entry := range raw {
			entries = append(entries, appsource.LocalDaemonEntry{Entry: entry})
		}
		return entries
	}, http.DefaultClient))
	for _, source := range cfg.CodexSources {
		registry.Add(appsource.NewCodexSource(source, http.DefaultClient))
	}
	return registry
}

var hubRelayIdleExitHook func(threadID string)
var hubRelayAfterIdleDeleteHook func(threadID string)

type hubRelayHandle struct {
	ready chan struct{}
	err   error
}

type threadReadRelayPolicy interface {
	RelayOnThreadRead() bool
}

func relayOnThreadRead(source appsource.Source) bool {
	if policy, ok := source.(threadReadRelayPolicy); ok {
		return policy.RelayOnThreadRead()
	}
	return true
}

func newHubAppServer(cfg WebConfig, sources *appsource.Registry) *appserver.Server {
	server := appserver.NewServer(appserver.ServerConfig{
		ServerName: "serf-hub",
		Version:    Version,
		SourceID:   "local",
		Features: appwire.FeatureSet{
			ThreadList:        true,
			ThreadTurnsList:   false,
			TurnStart:         true,
			TurnSteer:         true,
			ThreadClear:       true,
			ThreadShutdown:    true,
			ForkFromTurn:      true,
			Tasks:             true,
			ModelList:         true,
			DirectoryComplete: true,
		},
	})
	var relayMu sync.Mutex
	relayedThreads := map[string]struct{}{}
	startRelay := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
		threadID := thread.ID
		if threadID == "" {
			return nil
		}
		appserver.Subscribe(ctx, threadID)

		subscribeParams := params
		if subscribeParams.Ref == "" {
			subscribeParams.Ref = thread.Serf.Ref
		}
		if subscribeParams.Ref == "" {
			subscribeParams.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
		}

		relayKey := source.ID() + ":" + threadID
		relayMu.Lock()
		if _, ok := relayedThreads[relayKey]; ok {
			relayMu.Unlock()
			return nil
		}
		relayedThreads[relayKey] = struct{}{}
		relayMu.Unlock()

		notifications, err := source.SubscribeThread(context.WithoutCancel(ctx), subscribeParams)
		if err != nil {
			relayMu.Lock()
			delete(relayedThreads, relayKey)
			relayMu.Unlock()
			return err
		}
		go func() {
			defer func() {
				relayMu.Lock()
				delete(relayedThreads, relayKey)
				relayMu.Unlock()
			}()
			for notification := range notifications {
				server.Broadcast(threadID, notification.Method, notification.Params)
			}
		}()
		return nil
	}
	startTurn := func(ctx context.Context, source appsource.Source, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		readParams := appwire.ThreadReadParams{Ref: params.Ref, IncludeTurns: false}
		threadResp, err := source.ReadThread(ctx, readParams)
		if err != nil {
			return appwire.TurnStartResponse{}, err
		}
		if !threadActionAvailable(threadResp.Thread.Serf.Capabilities, "send") {
			return appwire.TurnStartResponse{}, appwire.Unavailable("send is not available for this session")
		}
		if err := startRelay(ctx, source, readParams, threadResp.Thread); err != nil {
			return appwire.TurnStartResponse{}, err
		}
		return source.StartTurn(ctx, params)
	}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return hubThreadList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		source, err := sourceForThread(sources, params.Ref, params.ThreadID)
		if err != nil {
			if thread, ok := pastThreadForRead(cfg, params); ok {
				return appwire.ThreadReadResponse{Thread: thread}, nil
			}
			return appwire.ThreadReadResponse{}, err
		}
		resp, err := source.ReadThread(ctx, params)
		if err != nil {
			if thread, ok := pastThreadForRead(cfg, params); ok {
				return appwire.ThreadReadResponse{Thread: thread}, nil
			}
			return appwire.ThreadReadResponse{}, err
		}
		resp.Thread = mergePastThreadForRead(cfg, params, resp.Thread)
		if params.Subscribe || relayOnThreadRead(source) {
			if err := startRelay(ctx, source, params, resp.Thread); err != nil {
				return appwire.ThreadReadResponse{}, err
			}
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		return hubThreadStart(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		return hubThreadResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadFork, func(ctx context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
		return hubThreadFork(ctx, cfg, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			if _, resumeErr := hubThreadResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref}); resumeErr != nil {
				return appwire.TurnStartResponse{}, resumeErr
			}
			source, err = sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnStartResponse{}, err
			}
		}
		resp, err := startTurn(ctx, source, params)
		if err == nil {
			return resp, nil
		}
		if _, ok := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: params.Ref}); !ok {
			return appwire.TurnStartResponse{}, err
		}
		if _, resumeErr := hubThreadResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref}); resumeErr != nil {
			return appwire.TurnStartResponse{}, resumeErr
		}
		source, sourceErr := sourceForThread(sources, params.Ref, "")
		if sourceErr != nil {
			return appwire.TurnStartResponse{}, sourceErr
		}
		return startTurn(ctx, source, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnSteer, func(ctx context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "steer"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.SteerTurn(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnInterrupt, func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "interrupt"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.InterruptTurn(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadClear, func(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.ThreadClearResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "clear"); err != nil {
			return appwire.ThreadClearResponse{}, err
		}
		return source.ClearThread(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadCompactStart, func(ctx context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "compact"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.CompactThread(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadShutdown, func(ctx context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "shutdown"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.ShutdownThread(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(ctx context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "model"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.SetThreadModel(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodModelList, func(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
		source, ok := sources.Source("local")
		if ok {
			resp, err := source.ListModels(ctx, params)
			if err == nil && len(resp.Data) > 0 {
				return resp, nil
			}
		}
		out := make([]appwire.ModelDescriptor, 0, len(cfg.Models))
		for _, model := range cfg.Models {
			out = append(out, appwire.ModelDescriptor{Provider: model.Provider, Model: model.Model})
		}
		return appwire.ModelListResponse{Data: out}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfTasksList, func(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.TaskListResponse{}, err
		}
		return source.ListTasks(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfDirsComplete, func(_ context.Context, params appwire.DirsCompleteParams) (appwire.DirsCompleteResponse, error) {
		return completeDirs(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: launchHarnessDescriptors(cfg)}, nil
	})
	return server
}

func ensureThreadActionAvailable(ctx context.Context, source appsource.Source, ref, action string) error {
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
	if err != nil {
		return err
	}
	if threadActionAvailable(resp.Thread.Serf.Capabilities, action) {
		return nil
	}
	return appwire.Unavailable(action + " is not available for this session")
}

func threadActionAvailable(caps appwire.ThreadCapabilities, action string) bool {
	switch action {
	case "send":
		return caps.Send
	case "steer":
		return caps.Steer
	case "interrupt":
		return caps.Interrupt
	case "compact":
		return caps.Compact
	case "clear":
		return caps.Clear
	case "fork":
		return caps.ForkFromTurn
	case "shutdown":
		return caps.Shutdown
	case "model":
		return caps.ChangeModel
	default:
		return false
	}
}

func hubThreadList(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	var threads []appwire.Thread
	liveIDs := map[string]struct{}{}
	for _, source := range sources.All() {
		if !sourceAllowedForList(source.ID(), params) {
			continue
		}
		resp, err := source.ListThreads(ctx, params)
		if err != nil {
			return appwire.ThreadListResponse{}, err
		}
		for _, thread := range resp.Data {
			if thread.ID != "" {
				liveIDs[thread.ID] = struct{}{}
			}
			if thread.SessionID != "" {
				liveIDs[thread.SessionID] = struct{}{}
			}
			thread = mergePastMetadataForList(cfg, thread)
			if appThreadMatches(thread, params) {
				threads = append(threads, thread)
			}
		}
	}
	if cfg.Past != nil {
		limit := params.Limit
		if limit <= 0 {
			limit = 100
		}
		for _, entry := range cfg.Past.Search(params.SearchTerm, limit, 0) {
			if _, ok := liveIDs[entry.ID]; ok {
				continue
			}
			thread := pastEntryThread(entry, false)
			if appThreadMatches(thread, params) {
				threads = append(threads, thread)
			}
		}
	}
	sort.SliceStable(threads, func(i, j int) bool {
		return appwireThreadLess(threads[i], threads[j])
	})
	if params.Limit > 0 && len(threads) > params.Limit {
		threads = threads[:params.Limit]
	}
	return appwire.ThreadListResponse{Data: threads}, nil
}

func sourceAllowedForList(sourceID string, params appwire.ThreadListParams) bool {
	if len(params.SourceIDs) == 0 {
		return true
	}
	for _, want := range params.SourceIDs {
		if want == sourceID {
			return true
		}
	}
	return false
}

func mergePastMetadataForList(cfg WebConfig, live appwire.Thread) appwire.Thread {
	if cfg.Past == nil {
		return live
	}
	var entry PastEntry
	var ok bool
	for _, id := range []string{live.ID, live.SessionID} {
		if id == "" {
			continue
		}
		entry, ok = cfg.Past.Find(id)
		if ok {
			break
		}
	}
	if !ok {
		return live
	}
	past := pastEntryThread(entry, false)
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
	if past.CreatedAt != 0 {
		live.CreatedAt = past.CreatedAt
	}
	if past.UpdatedAt != 0 {
		live.UpdatedAt = past.UpdatedAt
	}
	if live.Path == "" || live.Path == "." {
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
	return live
}

func appThreadMatches(thread appwire.Thread, params appwire.ThreadListParams) bool {
	if len(params.Statuses) > 0 {
		status := strings.ToLower(thread.Status.Type)
		found := false
		for _, want := range params.Statuses {
			if strings.EqualFold(want, status) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(params.SourceIDs) > 0 {
		found := false
		for _, sourceID := range params.SourceIDs {
			if sourceID == thread.Source {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	q := strings.ToLower(strings.TrimSpace(params.SearchTerm))
	if q == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		thread.ID,
		thread.SessionID,
		thread.Name,
		thread.Preview,
		thread.CWD,
		thread.Path,
		thread.ModelProvider,
	}, " "))
	return strings.Contains(haystack, q)
}

func pastThreadForRead(cfg WebConfig, params appwire.ThreadReadParams) (appwire.Thread, bool) {
	if cfg.Past == nil {
		return appwire.Thread{}, false
	}
	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" && params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.Thread{}, false
		}
		threadID = ref.ThreadID
	}
	if threadID == "" {
		return appwire.Thread{}, false
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.Thread{}, false
	}
	return pastEntryThread(entry, params.IncludeTurns), true
}

func mergePastThreadForRead(cfg WebConfig, params appwire.ThreadReadParams, live appwire.Thread) appwire.Thread {
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

func pastEntryThread(entry PastEntry, includeTurns bool) appwire.Thread {
	title := entry.Meta.OriginalTask
	if title == "" {
		title = entry.Meta.ID
	}
	cwd := entry.Meta.EnvInfo.WorkingDir
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
	createdAt := orderCreatedAt(entry.Meta.CreatedAt, entry.Meta.UpdatedAt)
	updatedAt := orderUpdatedAt(entry.Meta.UpdatedAt, entry.Meta.CreatedAt)
	thread := appwire.Thread{
		ID:            entry.Meta.ID,
		SessionID:     entry.Meta.ID,
		Preview:       title,
		Name:          title,
		ModelProvider: entry.Meta.Model,
		CreatedAt:     unixSeconds(createdAt),
		UpdatedAt:     unixSeconds(updatedAt),
		Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusEnded},
		Path:          filepath.Base(cwd),
		CWD:           cwd,
		Source:        "local",
		Serf: appwire.SerfThread{
			Ref:     ref,
			Profile: entry.Meta.ProfileID,
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

func pastEntryTurns(entry PastEntry) []appwire.Turn {
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	toolNames := map[string]string{}
	var turns []appwire.Turn
	entryIndex := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &head); err != nil || head.Kind != "entry" {
			if head.Kind == "api_call" {
				var call agent.TranscriptAPICall
				if err := json.Unmarshal(raw, &call); err == nil && strings.TrimSpace(call.Error) != "" {
					info := diagnostic.Classify(call.Error)
					entryIndex++
					turns = append(turns, appwire.Turn{
						ID:        fmt.Sprintf("turn_%d", entryIndex),
						ItemsView: "full",
						Status:    appwire.TurnStatusFailed,
						Error: &appwire.TurnError{
							Message: call.Error,
							Source:  string(info.Source),
							Title:   info.Title,
							Hint:    info.Hint,
						},
					})
				}
			}
			continue
		}
		var entryRec replayEntry
		if err := json.Unmarshal(raw, &entryRec); err != nil {
			continue
		}
		entryIndex++
		turnID := fmt.Sprintf("turn_%d", entryIndex)
		items := appItemsFromReplayTurn(turnID, entryIndex, entryRec.Turn, toolNames)
		if len(items) == 0 {
			continue
		}
		turns = append(turns, appwire.Turn{ID: turnID, Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted})
	}
	return turns
}

func appItemsFromReplayTurn(turnID string, turnIndex int, turn replayTurn, toolNames map[string]string) []appwire.ThreadItem {
	switch turn.Kind {
	case "USER_INPUT":
		item := appwire.ThreadItem{
			Type:   "user_message",
			ID:     fmt.Sprintf("item_user_%d", turnIndex),
			TurnID: turnID,
			Text:   joinText(turn.Message.Content),
			Status: "completed",
		}
		for _, part := range turn.Message.Content {
			if part.Kind != "image" || part.Image == nil || len(part.Image.Data) == 0 {
				continue
			}
			item.Images = append(item.Images, appwire.InputItem{
				Type:      "input_image",
				MediaType: part.Image.MediaType,
				Name:      part.Image.Name,
				Metadata: map[string]string{
					"sha":  imageSha(part.Image.Data),
					"size": strconv.Itoa(len(part.Image.Data)),
				},
			})
		}
		return []appwire.ThreadItem{item}
	case "STEERING":
		return []appwire.ThreadItem{{
			Type:   "steering",
			ID:     fmt.Sprintf("item_steering_%d", turnIndex),
			TurnID: turnID,
			Text:   joinText(turn.Message.Content),
			Status: "completed",
		}}
	case "ASSISTANT":
		var items []appwire.ThreadItem
		for i, part := range turn.Message.Content {
			switch part.Kind {
			case "text":
				if part.Text != "" {
					items = append(items, appwire.ThreadItem{
						Type:   "agent_message",
						ID:     fmt.Sprintf("item_assistant_%d_%d", turnIndex, i),
						TurnID: turnID,
						Text:   part.Text,
						Status: "completed",
					})
				}
			case "tool_call":
				if part.ToolCall != nil {
					toolNames[part.ToolCall.ID] = part.ToolCall.Name
					items = append(items, appwire.ThreadItem{
						Type:          "tool_call",
						ID:            fmt.Sprintf("item_tool_%d_%d", turnIndex, i),
						TurnID:        turnID,
						ToolName:      part.ToolCall.Name,
						CallID:        part.ToolCall.ID,
						ArgumentsJSON: string(part.ToolCall.Arguments),
						Status:        appwire.TurnStatusRunning,
					})
				}
			}
		}
		return items
	case "TOOL", "TOOL_RESULTS":
		var items []appwire.ThreadItem
		for i, part := range turn.Message.Content {
			if part.Kind != "tool_result" || part.ToolResult == nil {
				continue
			}
			name := part.ToolResult.Name
			if name == "" {
				name = toolNames[part.ToolResult.ToolCallID]
			}
			item := appwire.ThreadItem{
				Type:     "tool_call",
				ID:       fmt.Sprintf("item_tool_result_%d_%d", turnIndex, i),
				TurnID:   turnID,
				ToolName: name,
				CallID:   part.ToolResult.ToolCallID,
				Status:   "completed",
			}
			if part.ToolResult.IsError {
				item.Error = stringifyToolContent(part.ToolResult.Content)
			} else {
				item.Output = stringifyToolContent(part.ToolResult.Content)
			}
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

func sourceForThread(sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	if ref != "" {
		return sources.SourceForRef(ref)
	}
	source, ok := sources.Source("local")
	if !ok {
		return nil, fmt.Errorf("source not found: local")
	}
	if threadID == "" {
		return source, nil
	}
	return source, nil
}

func hubThreadStart(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	sourceID := launchSourceID(params)
	if sourceID != "" && sourceID != "local" {
		source, ok := sources.Source(sourceID)
		if !ok {
			if cfg.CodexLauncher == nil {
				return appwire.ThreadStartResponse{}, fmt.Errorf("source not found: %s", sourceID)
			}
			launched, err := cfg.CodexLauncher.EnsureSource(ctx, sourceID, sources)
			if err != nil {
				return appwire.ThreadStartResponse{}, err
			}
			source = launched
		}
		return source.StartThread(ctx, params)
	}
	if cfg.Spawner == nil {
		return appwire.ThreadStartResponse{}, appwire.Unavailable("spawner not configured")
	}
	model := params.Model
	if params.ModelProvider != "" && params.Model != "" && !strings.HasPrefix(params.Model, params.ModelProvider+"/") {
		model = params.ModelProvider + "/" + params.Model
	}
	if model == "" {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams("model is required")
	}
	modelRef, err := cmdutil.ParseModelRef(model)
	if err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	workingDir := params.CWD
	if workingDir != "" {
		resolved, err := canonicalizeDir(workingDir)
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams("cwd: " + err.Error())
		}
		workingDir = resolved
	}
	entry, err := cfg.Spawner.Spawn(ctx, SpawnRequest{
		Model:           modelRef.Qualified(),
		WorkingDir:      workingDir,
		Agent:           params.Profile,
		ReasoningEffort: params.ReasoningEffort,
	})
	if err != nil {
		return appwire.ThreadStartResponse{}, err
	}
	if cfg.Roster != nil {
		cfg.Roster.Refresh()
		if entry.ThreadID == "" || entry.SessionID == "" {
			for _, live := range cfg.Roster.List() {
				if live.PID == entry.PID {
					if entry.ThreadID == "" {
						entry.ThreadID = live.SessionID
					}
					if entry.SessionID == "" {
						entry.SessionID = live.SessionID
					}
					break
				}
			}
		}
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.ThreadID}.String()
	source, err := sourceForThread(sources, ref, "")
	if err != nil {
		if entry.ThreadID == "" {
			return appwire.ThreadStartResponse{}, err
		}
		return appwire.ThreadStartResponse{Thread: appwire.Thread{
			ID:            entry.ThreadID,
			SessionID:     entry.SessionID,
			Preview:       entry.SessionID,
			ModelProvider: modelRef.Provider,
			CWD:           workingDir,
			Source:        "local",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:          appwire.SerfThread{Ref: ref},
		}}, nil
	}
	threadResp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		threadResp.Thread = appwire.Thread{ID: entry.ThreadID, SessionID: entry.SessionID, Source: "local", Serf: appwire.SerfThread{Ref: ref}}
	}
	turn := appwire.Turn{}
	if params.Prompt != "" || len(params.Items) > 0 {
		turnResp, err := source.StartTurn(ctx, appwire.TurnStartParams{Ref: ref, Prompt: params.Prompt, Items: params.Items})
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		turn = turnResp.Turn
	}
	return appwire.ThreadStartResponse{Thread: threadResp.Thread, Turn: turn}, nil
}

func launchSourceID(params appwire.ThreadStartParams) string {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" {
		if harness == "serf" {
			return "local"
		}
		return harness
	}
	return ""
}

func launchHarnessDescriptors(cfg WebConfig) []appwire.HarnessDescriptor {
	out := []appwire.HarnessDescriptor{{ID: "serf", Label: "serf", Kind: "serf"}}
	seen := map[string]bool{"serf": true}
	for _, source := range cfg.CodexSources {
		id := strings.TrimSpace(source.ID)
		if id == "" {
			id = "codex"
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, appwire.HarnessDescriptor{ID: id, Label: id, Kind: "codex"})
	}
	for _, launch := range cfg.CodexLaunches {
		id := strings.TrimSpace(launch.ID)
		if id == "" {
			id = "codex"
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, appwire.HarnessDescriptor{ID: id, Label: id, Kind: "codex"})
	}
	return out
}

func hubThreadResume(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	if params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		if ref.SourceID != "local" {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.ThreadResumeResponse{}, err
			}
			return source.ResumeThread(ctx, params)
		}
	}
	if cfg.Spawner == nil {
		return appwire.ThreadResumeResponse{}, appwire.Unavailable("spawner not configured")
	}
	sessionID := strings.TrimSpace(params.Session)
	if sessionID == "" && params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		sessionID = ref.ThreadID
	}
	if sessionID == "" {
		return appwire.ThreadResumeResponse{}, appwire.InvalidParams("sessionId or ref is required")
	}
	entry, err := cfg.Spawner.Resume(ctx, resumeRequestForConfig(cfg, sessionID))
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	if cfg.Roster != nil {
		cfg.Roster.Refresh()
	}
	threadID := entry.ThreadID
	if threadID == "" {
		threadID = entry.SessionID
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
	source, err := sourceForThread(sources, ref, "")
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	threadResp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	return appwire.ThreadResumeResponse{Thread: threadResp.Thread}, nil
}

func resumeRequestForConfig(cfg WebConfig, id string) ResumeRequest {
	req := ResumeRequest{SessionID: id}
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(id); ok {
			req.WorkingDir = pe.Meta.EnvInfo.WorkingDir
			req.StateDir = pe.StateDir
		}
	}
	return req
}

func hubThreadFork(_ context.Context, cfg WebConfig, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	turn, err := parseSourceTurnID(params.SourceTurnID)
	if err != nil {
		return appwire.ThreadForkResponse{}, appwire.InvalidParams(err.Error())
	}
	if strings.TrimSpace(params.EditedInput) == "" {
		return appwire.ThreadForkResponse{}, appwire.InvalidParams("editedInput is required")
	}
	stateDir := cfg.StateDir
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(ref.ThreadID); ok {
			stateDir = pe.StateDir
		}
	}
	if stateDir == "" {
		return appwire.ThreadForkResponse{}, appwire.Unavailable("state dir not resolvable for parent thread")
	}
	childID, err := agent.ForkSession(stateDir, ref.ThreadID, turn, params.EditedInput, params.Label)
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	if cfg.Past != nil {
		_ = cfg.Past.Rebuild()
	}
	childRef := appwire.Ref{SourceID: ref.SourceID, ThreadID: childID}.String()
	return appwire.ThreadForkResponse{Thread: appwire.Thread{
		ID:        childID,
		SessionID: childID,
		Source:    ref.SourceID,
		Serf:      appwire.SerfThread{Ref: childRef},
	}}, nil
}

func parseSourceTurnID(raw string) (int, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "turn_"))
	if raw == "" {
		return 0, fmt.Errorf("sourceTurnId is required")
	}
	turn, err := strconv.Atoi(raw)
	if err != nil || turn < 1 {
		return 0, fmt.Errorf("sourceTurnId must be a positive turn number")
	}
	return turn, nil
}

func completeDirs(params appwire.DirsCompleteParams) (appwire.DirsCompleteResponse, error) {
	prefix := params.Prefix
	if prefix == "" {
		prefix = os.Getenv("HOME")
	}
	if strings.HasPrefix(prefix, "~/") || prefix == "~" {
		prefix = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(prefix, "~"))
	}
	cleaned, err := sanitizeDirPrefix(prefix)
	if err != nil {
		return appwire.DirsCompleteResponse{}, nil
	}
	prefix = cleaned

	var listDir, filter string
	if strings.HasSuffix(prefix, string(filepath.Separator)) || prefix == "" {
		listDir = prefix
		if listDir == "" {
			listDir = string(filepath.Separator)
		}
	} else {
		listDir = filepath.Dir(prefix)
		filter = filepath.Base(prefix)
	}

	entries, err := os.ReadDir(listDir)
	if err != nil {
		return appwire.DirsCompleteResponse{}, nil
	}
	limit := params.Limit
	if limit <= 0 || limit > 30 {
		limit = 30
	}
	results := make([]string, 0, limit)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") && filter == "" {
			continue
		}
		if filter != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(filter)) {
			continue
		}
		results = append(results, filepath.Join(listDir, name))
		if len(results) >= limit {
			break
		}
	}
	sort.Strings(results)
	return appwire.DirsCompleteResponse{Data: results}, nil
}
