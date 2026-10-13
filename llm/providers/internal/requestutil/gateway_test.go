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
