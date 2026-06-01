package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"

	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
)

func hubThreadTranscriptList(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
	root, err := hubTranscriptRoot(ctx, cfg, sources, params.Ref)
	if err != nil {
		return appwire.ThreadTranscriptListResponse{}, err
	}
	rootRef := threadRef(root)
	if rootRef == "" {
		rootRef = strings.TrimSpace(params.Ref)
	}
	if rootRef == "" {
		return appwire.ThreadTranscriptListResponse{}, appwire.InvalidParams("thread ref is required")
	}

	targets := []appwire.ThreadTranscriptTarget{{
		Ref:      rootRef,
		ThreadID: strutil.FirstNonEmpty(root.ID, root.SessionID),
		Title:    "main session (live)",
		Kind:     "main",
		Status:   root.Status.Type,
		Source:   transcriptTargetSource(rootRef, root.Source),
	}}
	seen := map[string]struct{}{rootRef: {}}
	addTarget := func(thread appwire.Thread, turnsUsed int) {
		if thread.Serf.Kind != "subagent" || thread.Serf.ParentRef != rootRef {
			return
		}
		ref := threadRef(thread)
		if ref == "" {
			return
		}
		if _, ok := seen[ref]; ok {
			return
		}
		seen[ref] = struct{}{}
		title := strutil.FirstNonEmpty(thread.Name, thread.Preview, thread.AgentNickname, "subagent "+strutil.FirstNonEmpty(thread.ID, thread.SessionID, ref))
		targets = append(targets, appwire.ThreadTranscriptTarget{
			Ref:       ref,
			ThreadID:  strutil.FirstNonEmpty(thread.ID, thread.SessionID),
			Title:     title,
			Kind:      "subagent",
			Status:    thread.Status.Type,
			Source:    transcriptTargetSource(ref, thread.Source),
			TurnsUsed: turnsUsed,
		})
	}

	if sources != nil {
		for _, source := range sources.All() {
			resp, err := source.ListThreads(ctx, appwire.ThreadListParams{IncludeSubagents: true})
			if err != nil {
				continue
			}
			for _, thread := range resp.Data {
				if thread.Source == "" {
					thread.Source = source.ID()
				}
				if thread.Serf.Ref == "" {
					threadID := strutil.FirstNonEmpty(thread.ID, thread.SessionID)
					if threadID != "" {
						thread.Serf.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
					}
				}
				addTarget(thread, len(thread.Turns))
			}
		}
	}
	if cfg.Past != nil {
		_ = cfg.Past.Rebuild()
		for _, entry := range cfg.Past.All() {
			addTarget(pastEntryThread(entry, false), entry.Meta.TurnCount)
		}
	}

	return appwire.ThreadTranscriptListResponse{Data: targets}, nil
}

func hubTranscriptRoot(ctx context.Context, cfg WebConfig, sources *appsource.Registry, ref string) (appwire.Thread, error) {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, ref, "")
	if err == nil {
		resp, readErr := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
		if readErr == nil {
			return resp.Thread, nil
		}
		err = readErr
	}
	if thread, ok := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false}); ok {
		return thread, nil
	}
	return appwire.Thread{}, err
}

func threadRef(thread appwire.Thread) string {
	if strings.TrimSpace(thread.Serf.Ref) != "" {
		return thread.Serf.Ref
	}
	sourceID := strings.TrimSpace(thread.Source)
	threadID := strutil.FirstNonEmpty(thread.ID, thread.SessionID)
	if sourceID == "" || threadID == "" {
		return ""
	}
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
}

func transcriptTargetSource(refText, fallback string) string {
	if ref, err := appwire.ParseRef(refText); err == nil && ref.SourceID != "" {
		return ref.SourceID
	}
	return fallback
}

// transcriptTailSummary returns the kind ("entry" | "api_call" | "") of the
// final non-empty line of the transcript at path, and whether that api_call
// recorded a non-empty Error field. Returns ("", false) on read failures so the
// caller leaves the thread status unchanged when it cannot inspect the tail.
func transcriptTailSummary(path string) (kind string, hasError bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptJSONLMaxLineBytes)
	var lastLine []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lastLine = append(lastLine[:0], line...)
	}
	if scanner.Err() != nil || len(lastLine) == 0 {
		return "", false
	}
	var head struct {
		Kind  string `json:"kind"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(lastLine, &head); err != nil {
		return "", false
	}
	return head.Kind, strings.TrimSpace(head.Error) != ""
}
