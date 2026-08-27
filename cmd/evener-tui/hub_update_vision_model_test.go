package tui

import (
	"strings"
	"testing"

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
