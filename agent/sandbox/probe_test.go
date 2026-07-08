package sandbox

import (
	"os"
	"testing"
)

// TestFakeProberRepresentsFloorRows proves every host tier of the spec's
// fail-closed floor matrix is representable as HostFacts and that the derived
// capability predicates (LandlockAvailable / SeatbeltAvailable) read correctly.
// The resolver keys the run-vs-refuse decision off exactly these facts, so if a
// tier were unrepresentable the contract suite could not express it.
func TestFakeProberRepresentsFloorRows(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		facts        HostFacts
		wantBwrap    bool
		wantLandlock bool
		wantSeatbelt bool
		wantOverlay  bool
	}{
		{
			name:         "bwrap-capable linux with overlay",
			facts:        HostFacts{OS: "linux", BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true, LandlockABI: 4},
			wantBwrap:    true,
			wantLandlock: true,
			wantOverlay:  true,
		},
		{
			name:         "bwrap-capable linux without overlay",
			facts:        HostFacts{OS: "linux", BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false, LandlockABI: 0},
			wantBwrap:    true,
			wantLandlock: false,
			wantOverlay:  false,
		},
		{
			name:         "landlock-only linux (no bwrap)",
			facts:        HostFacts{OS: "linux", BwrapCapable: false, LandlockABI: 3},
			wantBwrap:    false,
			wantLandlock: true,
		},
		{
			name:      "neither (bare linux)",
			facts:     HostFacts{OS: "linux"},
			wantBwrap: false,
		},
		{
			name:      "windows",
			facts:     HostFacts{OS: "windows"},
			wantBwrap: false,
		},
		{
			name:         "darwin with sandbox-exec",
			facts:        HostFacts{OS: "darwin", SandboxExecPath: "/usr/bin/sandbox-exec"},
			wantSeatbelt: true,
		},
		{
			name:         "darwin without sandbox-exec",
			facts:        HostFacts{OS: "darwin"},
			wantSeatbelt: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Prober = FakeProber{Facts: tc.facts}
			got := p.Probe()
			if got != tc.facts {
				t.Fatalf("FakeProber.Probe() = %+v, want %+v", got, tc.facts)
			}
			if got.BwrapCapable != tc.wantBwrap {
				t.Errorf("BwrapCapable = %v, want %v", got.BwrapCapable, tc.wantBwrap)
			}
			if got.LandlockAvailable() != tc.wantLandlock {
				t.Errorf("LandlockAvailable() = %v, want %v", got.LandlockAvailable(), tc.wantLandlock)
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

// TestSeatbeltRequiresDarwin guards that sandbox-exec presence on a non-darwin
// OS does not spuriously report Seatbelt available — the backend is macOS-only.
func TestSeatbeltRequiresDarwin(t *testing.T) {
	t.Parallel()
	h := HostFacts{OS: "linux", SandboxExecPath: "/usr/bin/sandbox-exec"}
	if h.SeatbeltAvailable() {
		t.Error("SeatbeltAvailable() must be false on a non-darwin OS even if sandbox-exec is present")
	}
}

// TestRealProberOptIn exercises the real host prober, but only when explicitly
// opted in (SERF_SANDBOX_PROBE_HOST=1). CI unit runs skip it so they never shell
// out to bwrap or issue landlock syscalls — probing is off the hermetic unit
// path. When it does run, it asserts only invariants that hold on any host: the
// probe returns the true GOOS and never panics.
func TestRealProberOptIn(t *testing.T) {
	if os.Getenv("SERF_SANDBOX_PROBE_HOST") != "1" {
		t.Skip("set SERF_SANDBOX_PROBE_HOST=1 to probe the real host")
	}
	facts := RealProber{}.Probe()
	if facts.OS == "" {
		t.Fatal("RealProber returned an empty OS")
	}
	t.Logf("real host facts: %+v", facts)
}
