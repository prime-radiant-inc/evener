package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestImageDetail(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		part     string
		cap      string
		want     string
	}{
		{"chat drops an unset detail", registry.ProtocolOpenAIChat, "", "", ""},
		{"chat carries a row value", registry.ProtocolOpenAIChat, "", "low", "low"},
		{"chat maps the cap's original", registry.ProtocolOpenAIChat, "", "original", "high"},
		{"chat omit drops a part detail", registry.ProtocolOpenAIChat, "low", "omit", ""},
		{"chat carries a part value", registry.ProtocolOpenAIChat, "high", "low", "high"},
		{"chat maps the part's original", registry.ProtocolOpenAIChat, "original", "", "high"},
		{"chat drops an unknown part value", registry.ProtocolOpenAIChat, "medium", "", ""},
		{"responses defaults to high", registry.ProtocolOpenAIResponses, "", "", "high"},
		{"responses carries the row value", registry.ProtocolOpenAIResponses, "", "low", "low"},
		{"responses carries original verbatim", registry.ProtocolOpenAIResponses, "", "original", "original"},
		{"responses omit drops a part detail", registry.ProtocolOpenAIResponses, "low", "omit", ""},
		{"responses lets the part win", registry.ProtocolOpenAIResponses, "high", "low", "high"},
		{"another protocol carries only the part value", registry.ProtocolAnthropic, "low", "high", "low"},
		{"another protocol with no part value carries none", registry.ProtocolGoogle, "", "high", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageDetail(tc.protocol, tc.part, tc.cap); got != tc.want {
				t.Fatalf("imageDetail(%q, %q, %q) = %q, want %q", tc.protocol, tc.part, tc.cap, got, tc.want)
			}
		})
	}
}
