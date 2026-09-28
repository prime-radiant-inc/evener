package hostfence

// Crash-fencing 08c §9's local `BoundaryEntry` rendering. These builders are
// platform-neutral: the local reap uses them on every platform (the unix reap
// for its fail-closed marks, and the non-local arm for the durable disposition
// it persists), so they carry no build tag.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// boundaryHasForeignVariant reports whether a persisted `orphan-unverified`
// boundary carries any member that is not one of the local reap's own variants
// (`remote-fencing`, `boundary-unavailable`, or anything a build this old
// cannot parse). Such a record is never the local reap's to rewrite.
func boundaryHasForeignVariant(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		// Nil or empty bytes carry no member at all: local, with nothing foreign
		// to protect. (An `orphan-unverified` record is validated to carry an
		// array, so this is the defensive arm, not the common one.)
		return false
	}
	var members []struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &members); err != nil {
		// Present but unparseable is not this pass's evidence to act on: fail
		// closed by leaving the record alone.
		return true
	}
	for _, member := range members {
		switch member.Kind {
		case "local-linux", "local-darwin", "local-markerless":
		default:
			return true
		}
	}
	return false
}

// boundaryEntries renders the open intents as §9's per-member `BoundaryEntry[]`:
// a marked local-linux/local-darwin entry per intent that carries its launcher
// marker, and a single local-markerless entry per marker-less intent. The local
// variants are the ones the local reap persists; the union's remote and
// boundary-unavailable variants belong to the fencing slices that own them.
// Entries are sorted by nonce so a repeated boot composes byte-identical bytes.
func boundaryEntries(intents []hostops.SpawnIntent) (json.RawMessage, error) {
	ordered := slices.Clone(intents)
	slices.SortFunc(ordered, func(a, b hostops.SpawnIntent) int {
		if a.Nonce < b.Nonce {
			return -1
		}
		if a.Nonce > b.Nonce {
			return 1
		}
		return 0
	})
	members := make([]any, 0, len(ordered))
	for _, intent := range ordered {
		entry, err := boundaryEntry(intent)
		if err != nil {
			return nil, err
		}
		members = append(members, entry)
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return nil, fmt.Errorf("hostfence: render the orphan boundary: %w", err)
	}
	return raw, nil
}

// boundaryEntry renders one intent as its §9 entry.
func boundaryEntry(intent hostops.SpawnIntent) (any, error) {
	switch intent.Platform {
	case hostops.SpawnPlatformLinux:
		if intent.ValidMarker() {
			return struct {
				Kind      string `json:"kind"`
				CgroupID  string `json:"cgroupId"`
				Nonce     string `json:"nonce"`
				PID       int    `json:"pid"`
				StartTime string `json:"startTime"`
			}{Kind: "local-linux", CgroupID: intent.CgroupID, Nonce: intent.Nonce, PID: *intent.PID, StartTime: intent.StartTime}, nil
		}
		return struct {
			Kind     string `json:"kind"`
			Platform string `json:"platform"`
			CgroupID string `json:"cgroupId"`
			Nonce    string `json:"nonce"`
		}{Kind: "local-markerless", Platform: "linux", CgroupID: intent.CgroupID, Nonce: intent.Nonce}, nil
	case hostops.SpawnPlatformDarwin:
		if intent.PGID == nil || intent.SessionID == nil {
			return nil, fmt.Errorf("%w: intent %q carries no darwin pair", hostops.ErrInvalidSpawnIntent, intent.Nonce)
		}
		if intent.ValidMarker() {
			return struct {
				Kind      string `json:"kind"`
				PGID      int    `json:"pgid"`
				SessionID int    `json:"sessionId"`
				PID       int    `json:"pid"`
				StartTime string `json:"startTime"`
				Nonce     string `json:"nonce"`
			}{Kind: "local-darwin", PGID: *intent.PGID, SessionID: *intent.SessionID, PID: *intent.PID, StartTime: intent.StartTime, Nonce: intent.Nonce}, nil
		}
		return struct {
			Kind      string `json:"kind"`
			Platform  string `json:"platform"`
			PGID      *int   `json:"pgid"`
			SessionID *int   `json:"sessionId"`
			Nonce     string `json:"nonce"`
		}{Kind: "local-markerless", Platform: "darwin", PGID: intent.PGID, SessionID: intent.SessionID, Nonce: intent.Nonce}, nil
	default:
		return nil, fmt.Errorf("%w: intent %q carries platform %q", hostops.ErrInvalidSpawnIntent, intent.Nonce, intent.Platform)
	}
}
