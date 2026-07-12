package tuiprim

import (
	"strings"
	"testing"
)

func FuzzPrimitives(f *testing.F) {
	seeds := []struct {
		left, right string
		width       int
	}{
		{"left", "right", 60},
		{"a", "b", 8},
		{"long left", "r", 7},
		{"l", "very long right", 8},
		{"", "", 0},
	}
	for _, seed := range seeds {
		f.Add(seed.left, seed.right, seed.width)
	}
	f.Fuzz(func(t *testing.T, left, right string, width int) {
		if width < -256 || width > 256 {
			return
		}
		_ = SectionDivider(width, left, right)
		got := DotLeader(left, right, width)
		if width <= 0 && got != left+" "+right {
			t.Fatalf("non-positive width changed fallback: %q", got)
		}
		_ = Overlay(OverlayOpts{Title: left, Body: right, Footer: left, Width: width})
	})
}

func FuzzPanesAndShell(f *testing.F) {
	f.Add("body", 20, 0)
	f.Add("body", 44, 3)
	f.Add("body", 100, -1)
	f.Fuzz(func(t *testing.T, body string, width, height int) {
		if width < -256 || width > 256 || height < -256 || height > 256 {
			return
		}
		_ = RenderStyledPane(body, width)
		_ = RenderPopupPane(body, width)
		got := AppShell{TopBar: "top", Body: body, Footer: "foot", Height: height}.View()
		if height > 0 && len(strings.Split(strings.TrimSuffix(got, "\n"), "\n")) > height {
			t.Fatalf("shell exceeded height %d: %q", height, got)
		}
	})
}

func TestPrimitiveBoundaryStates(t *testing.T) {
	if got := RenderStyledPane("body", 0); got != "" {
		t.Fatalf("zero-width styled pane = %q", got)
	}
	_ = RenderPopupPane("body", 0)
	_ = RenderPopupPane("body", 44)
	_ = Overlay(OverlayOpts{Width: 1})
	_ = SectionDivider(1, "long", "right")

	for _, shell := range []AppShell{
		{TopBar: "top"},
		{Footer: "foot"},
		{},
		{TopBar: "top", Footer: "foot", Height: 0},
	} {
		_ = shell.View()
	}
}
