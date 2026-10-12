package tui

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
)

func TestHubVisionModelPickerMarksMixedCaseOffActive(t *testing.T) {
	for _, visionModel := range []string{"OfF", "OFF"} {
		t.Run(visionModel, func(t *testing.T) {
			m := newSessionHubModel(nil)
			m.detail.VisionModel = visionModel

			updated, _ := m.Update(hubVisionModelsMsg{models: []tuipick.ModelPickerItem{
				{ID: "", Display: "Current model"},
				{ID: "off", Display: "Off"},
				{ID: "off/model", Display: "off/model"},
			}})
			got := updated.(hubModel)
			if got.sessionVisionModelPicker == nil {
				t.Fatal("vision model picker was not opened")
			}

			// "off" is the wire sentinel the canonical Off row carries; the
			// provider-qualified off/model row must never be read as it.
			if active := got.sessionVisionModelPicker.Active(); active != "off" {
				t.Fatalf("mixed-case off should mark the canonical Off row active, got active %q", active)
			}
		})
	}
}

func TestHubVisionModelNotificationUpdatesOnlyCurrentSession(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Ref = "local:current"
	m.detail.VisionModel = "off"

	encode := func(t *testing.T, params appwire.ThreadVisionModelChangedParams) []byte {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal vision notification: %v", err)
		}
		return raw
	}
	m.applyHubNotification(appwire.Notification{
		Method: appwire.NotifyThreadVisionModelChanged,
		Params: encode(t, appwire.ThreadVisionModelChangedParams{Ref: "local:other", VisionModel: "openai/gpt-4o"}),
	})
	if got := m.detail.VisionModel; got != "off" {
		t.Fatalf("other session notification changed VisionModel to %q", got)
	}
	m.applyHubNotification(appwire.Notification{
		Method: appwire.NotifyThreadVisionModelChanged,
		Params: encode(t, appwire.ThreadVisionModelChangedParams{Ref: "local:current", VisionModel: "openai/gpt-4o"}),
	})
	if got := m.detail.VisionModel; got != "openai/gpt-4o" {
		t.Fatalf("current session notification VisionModel = %q, want openai/gpt-4o", got)
	}
}
