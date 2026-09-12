package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestConfiguredAdapterTransportShortestHeaderTimeout(t *testing.T) {
	for _, tt := range []struct {
		name                          string
		request, idle, existing, want time.Duration
	}{
		{"request shortest", time.Second, 2 * time.Second, 3 * time.Second, time.Second},
		{"idle shortest", 3 * time.Second, time.Second, 2 * time.Second, time.Second},
		{"existing shortest", 3 * time.Second, 2 * time.Second, time.Second, time.Second},
		{"request disabled idle shortest", 0, time.Second, 2 * time.Second, time.Second},
		{"request disabled existing shortest", 0, 2 * time.Second, time.Second, time.Second},
		{"idle disabled existing shortest", 2 * time.Second, 0, time.Second, time.Second},
		{"idle disabled request shortest", time.Second, 0, 2 * time.Second, time.Second},
		{"existing disabled idle shortest", 2 * time.Second, time.Second, 0, time.Second},
		{"existing disabled request shortest", time.Second, 2 * time.Second, 0, time.Second},
		{"request only", time.Second, 0, 0, time.Second},
		{"idle only", 0, time.Second, 0, time.Second},
		{"existing only", 0, 0, time.Second, time.Second},
		{"all disabled", 0, 0, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := &http.Transport{ResponseHeaderTimeout: tt.existing}
			// Connect keeps configuration active when both response policies are disabled.
			got := configuredAdapterTransport(base, &AdapterTimeout{Connect: time.Second, Request: tt.request, StreamRead: tt.idle})
			if got.ResponseHeaderTimeout != tt.want {
				t.Errorf("ResponseHeaderTimeout = %v, want %v", got.ResponseHeaderTimeout, tt.want)
			}
			if base.ResponseHeaderTimeout != tt.existing {
				t.Fatal("caller transport mutated")
			}
		})
	}
}
