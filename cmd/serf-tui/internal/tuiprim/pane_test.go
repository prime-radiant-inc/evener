package tuiprim

import (
	"strings"
	"testing"
)

func TestActionBarJoinsKeys(t *testing.T) {
	tests := []struct {
		keys []string
		want string
	}{
		{[]string{"enter", "send"}, "enter  send"},
		{[]string{"esc", "cancel"}, "esc  cancel"},
		{[]string{}, ""},
		{[]string{"single"}, "single"},
		{[]string{"a", "b", "c"}, "a  b  c"},
	}
	for _, tc := range tests {
		got := ActionBar(tc.keys...)
		if got != tc.want {
			t.Errorf("ActionBar(%v) = %q, want %q", tc.keys, got, tc.want)
		}
	}
}

func TestActionBarForWidth(t *testing.T) {
	tests := []struct {
		name  string
		width int
		keys  []string
		want  string
	}{
		{
			name:  "zero width falls back to ActionBar",
			width: 0,
			keys:  []string{"enter", "send", "esc"},
			want:  "enter  send  esc",
		},
		{
			name:  "fits on one line",
			width: 100,
			keys:  []string{"enter", "send", "esc"},
			want:  "enter  send  esc",
		},
		{
			name:  "wraps to two lines",
			width: 15,
			keys:  []string{"enter send", "esc cancel"},
			want:  "enter send\nesc cancel",
		},
		{
			name:  "single key",
			width: 10,
			keys:  []string{"enter"},
			want:  "enter",
		},
		{
			name:  "empty keys",
			width: 10,
			keys:  []string{},
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ActionBarForWidth(tc.width, tc.keys...)
			if got != tc.want {
				t.Errorf("ActionBarForWidth(%d, %v) = %q, want %q", tc.width, tc.keys, got, tc.want)
			}
		})
	}
}

func TestPopupPaneWidth(t *testing.T) {
	tests := []struct {
		termWidth int
		want      int
	}{
		{0, 96},
		{30, 44},
		{44, 44},
		{60, 60},
		{96, 96},
		{120, 96},
	}
	for _, tc := range tests {
		got := PopupPaneWidth(tc.termWidth)
		if got != tc.want {
			t.Errorf("PopupPaneWidth(%d) = %d, want %d", tc.termWidth, got, tc.want)
		}
	}
}

func TestPopupPaneContentWidth(t *testing.T) {
	tests := []struct {
		termWidth int
		want      int
	}{
		{0, 90},   // PopupPaneWidth(0)=96, 96-6=90
		{30, 38},  // PopupPaneWidth(30)=44, 44-6=38
		{44, 38},  // PopupPaneWidth(44)=44, 44-6=38
		{60, 54},  // PopupPaneWidth(60)=60, 60-6=54
		{96, 90},  // PopupPaneWidth(96)=96, 96-6=90
		{120, 90}, // PopupPaneWidth(120)=96, 96-6=90
	}
	for _, tc := range tests {
		got := PopupPaneContentWidth(tc.termWidth)
		if got != tc.want {
			t.Errorf("PopupPaneContentWidth(%d) = %d, want %d", tc.termWidth, got, tc.want)
		}
	}
}

func TestActionBarForWidthWrapsByRenderedWidth(t *testing.T) {
	// Use keys that are short in plain text but verify wrapping logic works.
	keys := []string{"enter send message", "esc cancel operation", "tab complete"}
	got := ActionBarForWidth(25, keys...)
	// Each pairing exceeds width 25, so every key lands on its own line in order.
	want := "enter send message\nesc cancel operation\ntab complete"
	if got != want {
		t.Errorf("ActionBarForWidth(25, ...) = %q, want %q", got, want)
	}
	// No line should exceed the wrapping width.
	for i, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 25 {
			t.Errorf("line %d too long: %q (%d runes)", i, line, len([]rune(line)))
		}
	}
}
