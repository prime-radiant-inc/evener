package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
)

// registerNotesTools registers the shared-notes agent tools into reg,
// mirroring registerGoalTools: notes_agent_set, urls_add, urls_remove,
// notes_read. There is deliberately no notes_human_set tool — the human
// whiteboard is owned by the hub channel (ownership by channel absence).
func registerNotesTools(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefNotesAgentSet(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			note, _ := args["note"].(string)
			stored, changed := deps.notesGuard.setAgentNote(note)
			deps.notesGuard.save()
			if changed {
				human, agent := deps.notesGuard.Snapshot()
				deps.emit(events.EventNotesUpdated, notesUpdatedData(human, agent))
			}
			return tool.StateResult{
				Output: "Agent note recorded.",
				State:  map[string]any{"agentNote": stored},
			}, nil
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefUrlsAdd(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			rawURL, _ := args["url"].(string)
			label, _ := args["label"].(string)
			entry, err := deps.notesGuard.addURL(rawURL, label)
			if err != nil {
				return nil, err
			}
			deps.notesGuard.save()
			deps.emit(events.EventUrlsUpdated, urlsUpdatedData(deps.notesGuard.SnapshotURLs()))
			return tool.StateResult{
				Output: "URL added: " + entry.URL,
				State:  entry,
			}, nil
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefUrlsRemove(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			id, _ := args["id"].(string)
			if !deps.notesGuard.removeURL(id) {
				return nil, fmt.Errorf("urls/remove: no URL entry with id %q", id)
			}
			deps.notesGuard.save()
			deps.emit(events.EventUrlsUpdated, urlsUpdatedData(deps.notesGuard.SnapshotURLs()))
			return tool.StateResult{
				Output: "URL removed.",
				State:  map[string]any{"id": id},
			}, nil
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefNotesRead(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			human, agent, urls := deps.notesGuard.SnapshotAll()
			lines := []string{}
			if human != "" {
				lines = append(lines, "Human: "+human)
			}
			if agent != "" {
				lines = append(lines, "Agent: "+agent)
			}
			for _, u := range urls {
				lines = append(lines, formatNotesLinkLine(u))
			}
			out := strings.Join(lines, "\n")
			if out == "" {
				out = "No shared notes."
			}
			return tool.StateResult{
				Output: out,
				State: map[string]any{
					"humanNote": human,
					"agentNote": agent,
					"urls":      urls,
				},
			}, nil
		},
	})
}

// notesUpdatedData converts a notes snapshot into the public event payload
// shared by every notes mutation boundary.
func notesUpdatedData(human, agent string) events.NotesUpdatedData {
	return events.NotesUpdatedData{HumanNote: human, AgentNote: agent}
}

// urlsUpdatedData converts a URL list snapshot into the public event payload
// shared by every URL-list mutation boundary.
func urlsUpdatedData(urls []schema.SessionURL) events.UrlsUpdatedData {
	entries := make([]events.SessionURLData, 0, len(urls))
	for _, u := range urls {
		entries = append(entries, events.SessionURLData{
			ID:      u.ID,
			URL:     u.URL,
			Label:   u.Label,
			AddedBy: u.AddedBy,
			AddedAt: u.AddedAt,
		})
	}
	if entries == nil {
		entries = []events.SessionURLData{}
	}
	return events.UrlsUpdatedData{URLs: entries}
}
