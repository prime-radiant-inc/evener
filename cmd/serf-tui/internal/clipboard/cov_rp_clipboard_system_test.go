package clipboard

import (
	"testing"

	"primeradiant.com/serf/envvars"
)

func TestNewSystemClipboardSource(t *testing.T) {
	s := NewSystemClipboardSource()
	if s == nil {
		t.Fatal("NewSystemClipboardSource returned nil")
	}
	// The production source must satisfy the ClipboardSource contract.
	var _ ClipboardSource = s
}

func TestIsWaylandSession(t *testing.T) {
	t.Setenv(envvars.WaylandDisplay.Name, "")
	if isWaylandSession() {
		t.Fatal("isWaylandSession = true with WAYLAND_DISPLAY unset, want false")
	}
	t.Setenv(envvars.WaylandDisplay.Name, "wayland-0")
	if !isWaylandSession() {
		t.Fatal("isWaylandSession = false with WAYLAND_DISPLAY set, want true")
	}
}
