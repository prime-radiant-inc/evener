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
			// A non-nil empty slice would clobber a curated modality list when
			// mergeCaps replays the live layer, so "nothing" must read as nil.
			got := GatewayInputModalities(tc.arch, tc.caps)
			if !slices.Equal(got, tc.want) || (tc.want == nil && got != nil) {
				t.Fatalf("GatewayInputModalities = %#v, want %#v", got, tc.want)
			}
		})
	}
}
