package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestProject_NotesUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventNotesUpdated,
		Data: events.NotesUpdatedData{HumanNote: "h", AgentNote: "a"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerNotesUpdated {
		t.Fatalf("want one evener/notes/updated notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.NotesUpdatedParams)
	if !ok || params.HumanNote != "h" || params.AgentNote != "a" {
		t.Fatalf("params = %+v", out[0].Params)
	}
}

func TestProject_NotesUpdatedClear(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventNotesUpdated,
		Data: events.NotesUpdatedData{},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerNotesUpdated {
		t.Fatalf("want one evener/notes/updated clear notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.NotesUpdatedParams)
	if !ok || params.ThreadID != "th1" || params.Ref != "local:th1" || params.HumanNote != "" || params.AgentNote != "" {
		t.Fatalf("params = %+v, want empty notes", out[0].Params)
	}
}

func TestProject_UrlsUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventUrlsUpdated,
		Data: events.UrlsUpdatedData{URLs: []events.SessionURLData{
			{ID: "u1", URL: "https://x.test/y", Label: "why", AddedBy: "agent", AddedAt: 42},
		}},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerUrlsUpdated {
		t.Fatalf("want one evener/urls/updated notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.UrlsUpdatedParams)
	if !ok || params.ThreadID != "th1" || params.Ref != "local:th1" || len(params.URLs) != 1 {
		t.Fatalf("params = %+v, want one url entry", out[0].Params)
	}
	got := params.URLs[0]
	if got.ID != "u1" || got.URL != "https://x.test/y" || got.Label != "why" || got.AddedBy != "agent" || got.AddedAt != 42 {
		t.Fatalf("url entry = %+v, want mapped fields", got)
	}
}

func TestProject_UrlsUpdatedEmpty(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventUrlsUpdated,
		Data: events.UrlsUpdatedData{URLs: []events.SessionURLData{}},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerUrlsUpdated {
		t.Fatalf("want one evener/urls/updated empty notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.UrlsUpdatedParams)
	if !ok || params.ThreadID != "th1" || params.Ref != "local:th1" || len(params.URLs) != 0 {
		t.Fatalf("params = %+v, want empty url list", out[0].Params)
	}
}
