package server

import (
	"testing"
)

func TestAppCapabilities_SteerGatedOnProcessing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		state      string
		processing bool
		setSteer   bool
		wantSteer  bool
	}{
		{"processing with steerFunc", "PROCESSING", true, true, true},
		{"idle with steerFunc", "IDLE", false, true, false},
		{"awaiting with steerFunc", "AWAITING_INPUT", false, true, false},
		{"closed with steerFunc", "CLOSED", false, true, false},
		{"processing without steerFunc", "PROCESSING", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			if tc.setSteer {
				s.SetSteerFunc(func(string) {})
			}
			got := s.appCapabilities(tc.state, tc.processing)
			if got.Steer != tc.wantSteer {
				t.Fatalf("Steer = %v, want %v", got.Steer, tc.wantSteer)
			}
		})
	}
}
