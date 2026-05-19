package server

import (
	"testing"
)

func TestAppCapabilities_SteerGatedOnActiveTurn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		state      string
		processing bool
		reserved   bool
		stale      bool
		setSteer   bool
		wantSteer  bool
	}{
		{"processing with steerFunc", "PROCESSING", true, false, false, true, true},
		{"reserved idle with steerFunc", "IDLE", false, true, false, true, true},
		{"stale projected active turn with steerFunc", "IDLE", false, false, true, true, false},
		{"idle with steerFunc", "IDLE", false, false, false, true, false},
		{"awaiting with steerFunc", "AWAITING_INPUT", false, false, false, true, false},
		{"closed with steerFunc", "CLOSED", false, false, false, true, false},
		{"processing without steerFunc", "PROCESSING", true, false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			if tc.setSteer {
				s.SetSteerFunc(func(string) {})
			}
			if tc.reserved {
				s.appActiveTurnID = "turn_reserved"
				s.appReservedTurnID = "turn_reserved"
			}
			if tc.stale {
				s.appActiveTurnID = "turn_stale"
			}
			got := s.appCapabilities(tc.state, tc.processing)
			if got.Steer != tc.wantSteer {
				t.Fatalf("Steer = %v, want %v", got.Steer, tc.wantSteer)
			}
		})
	}
}
