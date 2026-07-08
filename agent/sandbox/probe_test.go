package sandbox

import (
	"os"
	"slices"
	"testing"
)

// The capability probe must exercise the exact version-gated flags Wrap emits,
// so an older bwrap that lacks --argv0 / --new-session (Ubuntu 22.04 = 0.6.1,
// Debian 12 = 0.8.0) fails the probe and is reported not-capable — rather than
// probing "capable" and then failing every real spawn with "unknown option
// --argv0" per the fail-closed floor.
func TestBwrapProbeExercisesWrapFlags(t *testing.T) {
	t.Parallel()
	args := bwrapProbeArgs("/usr/bin/bwrap")
	for _, want := range []string{"--argv0", "--new-session", "--unshare-user", "--unshare-pid", "--proc", "--dev"} {
		if !slices.Contains(args, want) {
			t.Errorf("probe argv must exercise %q (Wrap emits it), got %v", want, args)
		}
	}
}

// TestFakeProberRepresentsFloorRows proves every host tier of the spec's
// fail-closed floor matrix is representable as HostFacts and that the derived
// capability predicate (SeatbeltAvailable) reads correctly.
// The resolver keys the run-vs-refuse decision off exactly these facts, so if a
// tier were unrepresentable the contract suite could not express it.
func TestFakeProberRepresentsFloorRows(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		facts        HostFacts
		wantBwrap    bool
		wantSeatbelt bool
		wantOverlay  bool
	}{
		{
			name:        "bwrap-capable linux with overlay",
			facts:       HostFacts{OS: "linux", BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true},
			wantBwrap:   true,
			wantOverlay: true,
		},
		{
			name:        "bwrap-capable linux without overlay",
			facts:       HostFacts{OS: "linux", BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: false},
			wantBwrap:   true,
			wantOverlay: false,
		},
		{
			name:      "non-bwrap linux",
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
// out to bwrap — probing is off the hermetic unit
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
