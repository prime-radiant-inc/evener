package hub

import (
	"context"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars"
)

var hubTranscriptRootForList = hubTranscriptRoot

func hubThreadTranscriptList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
	root, err := hubTranscriptRootForList(ctx, cfg, sources, params.Ref)
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
		ThreadID: envvars.FirstNonEmpty(root.ID, root.SessionID),
		Title:    "main session (live)",
		Kind:     "main",
		Status:   root.Status.Type,
		Source:   transcriptTargetSource(rootRef, root.Source),
	}}
	seen := map[string]struct{}{rootRef: {}}
	addTarget := func(thread appwire.Thread, turnsUsed int) {
		if thread.Evener.Kind != "subagent" || thread.Evener.ParentRef != rootRef {
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
		title := envvars.FirstNonEmpty(thread.Name, thread.Preview, thread.AgentNickname, "subagent "+envvars.FirstNonEmpty(thread.ID, thread.SessionID, ref))
		targets = append(targets, appwire.ThreadTranscriptTarget{
			Ref:       ref,
			ThreadID:  envvars.FirstNonEmpty(thread.ID, thread.SessionID),
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
				if thread.Evener.Ref == "" {
					threadID := envvars.FirstNonEmpty(thread.ID, thread.SessionID)
					if threadID != "" {
						thread.Evener.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
					}
				}
				addTarget(thread, len(thread.Turns))
			}
		}
	}
	if cfg.Past != nil {
		_, _ = cfg.Past.Rebuild()
		for _, entry := range cfg.Past.All() {
			thread, err := pastEntryThread(ctx, cfg, entry, false)
			if err != nil {
				return appwire.ThreadTranscriptListResponse{}, err
			}
			addTarget(thread, entry.Meta.TurnCount)
		}
	}

	return appwire.ThreadTranscriptListResponse{Data: targets}, nil
}

func hubTranscriptRoot(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, ref string) (appwire.Thread, error) {
	source, err := sourceForThreadWithDeletionFence(cfg, sources, ref, "")
	if err == nil {
		resp, readErr := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
		if readErr == nil {
			return resp.Thread, nil
		}
		err = readErr
	}
	entry, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{Ref: ref})
	if !ok {
		return appwire.Thread{}, err
	}
	thread, pastErr := pastEntryThread(ctx, cfg, entry, false)
	if pastErr != nil {
		return appwire.Thread{}, pastErr
	}
	return thread, nil
}

func threadRef(thread appwire.Thread) string {
	if strings.TrimSpace(thread.Evener.Ref) != "" {
		return thread.Evener.Ref
	}
	sourceID := strings.TrimSpace(thread.Source)
	threadID := envvars.FirstNonEmpty(thread.ID, thread.SessionID)
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
