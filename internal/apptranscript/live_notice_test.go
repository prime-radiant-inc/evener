package apptranscript

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestRoundTimingsAnnouncementCarriesTheTimingsOnRaw(t *testing.T) {
	announcement := RoundTimingsAnnouncement(events.RoundTimings{Round: 1})
	if announcement.EventKind != appwire.ThreadItemEventKindRoundTimings || announcement.Text == "" {
		t.Fatalf("announcement = %+v", announcement)
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal(announcement.Raw, &payload); err != nil {
		t.Fatalf("raw is not JSON: %v", err)
	}
	if payload["roundTimings"]["round"] != float64(1) {
		t.Fatalf("raw round = %v, want 1", payload["roundTimings"]["round"])
	}
}

func TestPluginLoadedAnnouncementWithoutAName(t *testing.T) {
	announcement := PluginLoadedAnnouncement(events.PluginLoadedData{})
	if !strings.Contains(announcement.Description, "Loaded plugin (") {
		t.Fatalf("no name should say 'Loaded plugin (', got %q", announcement.Description)
	}
	item, ok := SystemMessage(announcement, "id", "turn")
	if !ok || item.Text != "" || item.Raw == nil {
		t.Fatalf("plugin_loaded item = %+v, %v; want its summary in the description with no text", item, ok)
	}
}

func TestContextCompactionAnnouncementDropsRawWhenMarshalFails(t *testing.T) {
	oldMarshal := marshalContextCompaction
	defer func() { marshalContextCompaction = oldMarshal }()
	marshalContextCompaction = func(any) ([]byte, error) { return nil, errors.New("injected marshal failure") }
	if raw := ContextCompactionAnnouncement(events.ContextCompactionData{Layer: "layer"}).Raw; raw != nil {
		t.Fatalf("marshal failure returned raw payload: %s", raw)
	}
}

func TestSystemMessageDropsAnAnnouncementWithNothingToShow(t *testing.T) {
	if _, ok := SystemMessage(NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindHookCompleted, Description: "desc"}, "id", "turn"); ok {
		t.Fatal("empty text with a non-plugin kind should show nothing")
	}
	if _, ok := SystemMessage(NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindPluginLoaded, Description: "  ", Text: "  "}, "id", "turn"); ok {
		t.Fatal("empty description and text should show nothing")
	}
}
