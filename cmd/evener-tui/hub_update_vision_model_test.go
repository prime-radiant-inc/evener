package tui

import (
	"encoding/json"
	"strings"
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

			plain := ansiPattern.ReplaceAllString(got.sessionVisionModelPicker.View(), "")
			lines := strings.Split(plain, "\n")
			var offLine, qualifiedLine string
			for _, line := range lines {
				if strings.Contains(line, "Off") {
					offLine = line
				}
				if strings.Contains(line, "off/model") {
					qualifiedLine = line
				}
			}
			if !strings.Contains(offLine, "(active)") {
				t.Fatalf("mixed-case off should mark canonical Off row active, got: %q", offLine)
			}
			if strings.Contains(qualifiedLine, "(active)") {
				t.Fatalf("provider-qualified off/model row must not be treated as off sentinel, got: %q", qualifiedLine)
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
