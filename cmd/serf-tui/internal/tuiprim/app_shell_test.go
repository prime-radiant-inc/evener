package tuiprim

import (
	"strings"
	"testing"
)

// TestAppShellNeverExceedsHeight verifies the bound holds even when the TopBar
// and Footer chrome alone meet or exceed Height: the frame must still not exceed
// Height, and the TopBar (the line inline mode would scroll off) must survive.
func TestAppShellNeverExceedsHeight(t *testing.T) {
	cases := []struct {
		name                  string
		topBar, overlay, foot string
		height                int
	}{
		{"multi-line chrome, tiny height", "T1\nT2\nT3", "o1\no2\no3\no4", "F1\nF2\nF3", 6},
		{"single-line chrome, height 4", "T", "o1\no2\no3\no4\no5\no6", "F", 4},
		{"footer taller than height, no content", "", "", "F1\nF2\nF3\nF4\nF5", 3},
		{"height 1", "T", "o1\no2", "F", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AppShell{TopBar: tc.topBar, Overlay: tc.overlay, Footer: tc.foot, Height: tc.height}.View()
			lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
			if len(lines) > tc.height {
				t.Fatalf("frame=%d lines exceeds Height=%d:\n%q", len(lines), tc.height, got)
			}
			if tc.topBar != "" && !strings.Contains(got, strings.SplitN(tc.topBar, "\n", 2)[0]) {
				t.Fatalf("TopBar first line dropped (would scroll header off inline):\n%q", got)
			}
		})
	}
}
