package requestutil

import (
	"slices"
	"testing"
)

func TestGatewayInputModalities(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		arch GatewayArchitecture
		caps GatewayCapabilities
		want []string
	}{
		{"architecture wins over capabilities", GatewayArchitecture{InputModalities: []string{"text", "pdf"}}, GatewayCapabilities{Vision: &yes}, []string{"text", "pdf"}},
		{"architecture deduped", GatewayArchitecture{InputModalities: []string{"text", "text", "image"}}, GatewayCapabilities{}, []string{"text", "image"}},
		{"only empty entries fall through to vision", GatewayArchitecture{InputModalities: []string{"", ""}}, GatewayCapabilities{Vision: &yes}, []string{"text", "image"}},
		{"only empty entries advertise nothing", GatewayArchitecture{InputModalities: []string{""}}, GatewayCapabilities{}, nil},
		{"vision true advertises text and image", GatewayArchitecture{}, GatewayCapabilities{Vision: &yes}, []string{"text", "image"}},
		{"vision false advertises nothing", GatewayArchitecture{}, GatewayCapabilities{Vision: &no}, nil},
		{"no facts advertises nothing", GatewayArchitecture{}, GatewayCapabilities{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Nil, not an empty slice, is what "advertises nothing" must mean:
			// anything else would erase a curated list on the live replay.
			got := GatewayInputModalities(tc.arch, tc.caps)
			if !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("GatewayInputModalities = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDedupStrings(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"drops duplicates and empties in first-seen order", []string{"low", "", "low", "high"}, []string{"low", "high"}},
		{"nothing survives", []string{"", ""}, nil},
		{"nil stays nil", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DedupStrings(tc.in)
			if !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("DedupStrings(%#v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestGatewayEffortLadder(t *testing.T) {
	str := func(s string) *string { return &s }
	levels := func(m map[string]*string) *GatewayClientCompat {
		return &GatewayClientCompat{Pi: &GatewayPiCompat{ThinkingLevelMap: m}}
	}
	cases := []struct {
		name   string
		compat *GatewayClientCompat
		want   []string
	}{
		{
			name: "map levels come back in rank order",
			compat: levels(map[string]*string{
				"max": str("max"), "high": str("high"), "low": str("low"),
				"medium": str("medium"), "xhigh": str("xhigh"), "minimal": nil, "off": nil,
			}),
			want: []string{"low", "medium", "high", "xhigh", "max"},
		},
		{
			name: "an explicit off level leads the ladder",
			compat: levels(map[string]*string{
				"off": str("none"), "low": str("low"), "high": str("high"),
			}),
			want: []string{"none", "low", "high"},
		},
		{
			name: "an off level spelled as another tier is not an off",
			compat: levels(map[string]*string{
				"off": str("low"), "low": str("low"), "high": str("high"),
			}),
			want: []string{"low", "high"},
		},
		{
			// glm maps medium→high, minimal→low and xhigh→max: the menu offers
			// the wire values the gateway accepts, once each.
			name: "aliases collapse to their wire values",
			compat: levels(map[string]*string{
				"minimal": str("low"), "low": str("low"), "medium": str("high"),
				"high": str("high"), "xhigh": str("max"), "max": str("max"), "off": str("none"),
			}),
			want: []string{"none", "low", "high", "max"},
		},
		{
			name: "a level spelled outside the vocabulary is dropped",
			compat: levels(map[string]*string{
				"low": str("low"), "high": str("turbo"),
			}),
			want: []string{"low"},
		},
		{
			name:   "no compat block states no ladder",
			compat: nil,
			want:   nil,
		},
		{
			name:   "a compat block without pi states no ladder",
			compat: &GatewayClientCompat{},
			want:   nil,
		},
		{
			// Every level null is a model with no effort control; the
			// capabilities block, not an empty ladder, must state that.
			name:   "an all-null map states no ladder",
			compat: levels(map[string]*string{"low": nil, "high": nil}),
			want:   nil,
		},
		{
			name:   "an empty map states no ladder",
			compat: levels(map[string]*string{}),
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GatewayEffortLadder(tc.compat)
			if !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("GatewayEffortLadder = %#v, want %#v", got, tc.want)
			}
		})
	}
}
