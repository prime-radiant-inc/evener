package hubcore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// HostPlanFacts is the refreshed preflight fact set evener/host/plan is built
// from (deploy pipeline 08b §1, §6 step 1): the host's OS and architecture, its
// home directory, the resolved roots, its UID, the installed evener version, the
// protocol version it speaks, and the launch flags its hub runs with.
//
// CapturedAt is when the refresh actually read them — never the mint's own
// clock. Both the plan's freshness term and the token's deadline are measured
// from it (§3), so a caller that stamps a later instant than it read would
// silently extend the token's life.
type HostPlanFacts struct {
	OS          string
	Arch        string
	Home        string
	Roots       []string
	UID         string
	Version     string
	Protocol    string
	LaunchFlags []string
	CapturedAt  time.Time
}

// HostRuntimeProbe is one running-state probe result (deploy pipeline 08b §6
// step 2): the revision the host reports running, its healthy flag, and the
// probed process start time when the probe carried one. The probe itself — the
// gated running read over the attach bridge, with the fence's takeover and
// bound on its write half — belongs to the slice that ships
// evener/host/running; this is only the value a plan binds.
type HostRuntimeProbe struct {
	Version          string
	RunningHealthy   bool
	ProcessStartTime *time.Time
}

// Revision is §1's factsRevision: the canonical digest of every preflight field
// planning or deploy decides on — OS/arch, home directory, resolved roots, UID,
// installed version, protocol version, and launch flags. The capture time is
// deliberately not part of it: freshness is bound separately (§3's
// factsCapturedAt), so the digest identifies the fact set itself.
//
// The digest is stable across spellings of "no roots" and "no flags": an absent
// list and an empty one digest the same, because both say the same thing.
func (f HostPlanFacts) Revision() string {
	// The encoding is length-prefixed field by field, so no value's own bytes
	// can be read as a field boundary or as another field's start: two different
	// fact sets cannot collide by concatenation.
	var canonical strings.Builder
	field := func(name, value string) {
		fmt.Fprintf(&canonical, "%d:%s=%d:%s\n", len(name), name, len(value), value)
	}
	field("os", f.OS)
	field("arch", f.Arch)
	field("home", f.Home)
	for _, root := range f.Roots {
		field("root", root)
	}
	field("uid", f.UID)
	field("version", f.Version)
	field("protocol", f.Protocol)
	for _, flag := range f.LaunchFlags {
		field("flag", flag)
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}
