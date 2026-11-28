package sandboxtest

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestFakeProberRepresentsFloorRows proves every host tier of the spec's
// fail-closed floor matrix is representable as HostFacts and that the derived
// capability predicate (SeatbeltAvailable) reads correctly.
// The resolver keys the run-vs-refuse decision off exactly these facts, so if a
// tier were unrepresentable the contract suite could not express it.
func TestFakeProberRepresentsFloorRows(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		facts        sandbox.HostFacts
		wantBwrap    bool
		wantSeatbelt bool
		wantOverlay  bool
	}{
		{
			name:        "bwrap-capable linux with overlay",
			facts:       sandbox.HostFacts{OS: "linux", BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true},
			wantBwrap:   true,
			wantOverlay: true,
		},
		{
			name:        "bwrap-capable linux without overlay",
			facts:       sandbox.HostFacts{OS: "linux", BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false},
			wantBwrap:   true,
			wantOverlay: false,
		},
		{
			name:      "non-bwrap linux",
			facts:     sandbox.HostFacts{OS: "linux"},
			wantBwrap: false,
		},
		{
			name:      "windows",
			facts:     sandbox.HostFacts{OS: "windows"},
			wantBwrap: false,
		},
		{
			name:         "darwin with sandbox-exec",
			facts:        sandbox.HostFacts{OS: "darwin", SandboxExecPath: "/usr/bin/sandbox-exec"},
			wantSeatbelt: true,
		},
		{
			name:         "darwin without sandbox-exec",
			facts:        sandbox.HostFacts{OS: "darwin"},
			wantSeatbelt: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p sandbox.Prober = FakeProber{Facts: tc.facts}
			got := p.Probe()
			if !reflect.DeepEqual(got, tc.facts) {
				t.Fatalf("FakeProber.Probe() = %+v, want %+v", got, tc.facts)
			}
			if got.BwrapCapable != tc.wantBwrap {
				t.Errorf("BwrapCapable = %v, want %v", got.BwrapCapable, tc.wantBwrap)
			}
			if got.SeatbeltAvailable() != tc.wantSeatbelt {
				t.Errorf("SeatbeltAvailable() = %v, want %v", got.SeatbeltAvailable(), tc.wantSeatbelt)
			}
			if got.OverlaySupported != tc.wantOverlay {
				t.Errorf("OverlaySupported = %v, want %v", got.OverlaySupported, tc.wantOverlay)
			}
		})
	}
}
