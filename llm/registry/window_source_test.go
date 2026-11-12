package registry

import "testing"

// Every provenance tag a window can carry maps into the documented source
// set. A new layer constant must be added to the table, then to the mapping.
func TestContextWindowSourceMapsEveryLayerIntoTheDocumentedSet(t *testing.T) {
	documented := map[string]bool{"live": true, "overlay": true, "config": true, "snapshot": true}
	for _, layer := range []string{LayerSnapshot, LayerCache, LayerOverlay, LayerLive, LayerConfig} {
		for _, suffix := range []string{"", "/row", "/provider", "/glob:x*"} {
			if got := ContextWindowSource(layer + suffix); !documented[got] {
				t.Errorf("provenance %q maps to %q, outside the documented set", layer+suffix, got)
			}
		}
	}
	if got := ContextWindowSource(LayerCache + "/row"); got != "snapshot" {
		t.Errorf("the refreshed catalog is catalog data: got %q", got)
	}
	if got := ContextWindowSource(""); got != "" {
		t.Errorf("no provenance maps to no source, got %q", got)
	}
}
