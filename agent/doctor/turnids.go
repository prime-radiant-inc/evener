package doctor

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"primeradiant.com/serf/identifier"
)

// entryIndexTurnID matches the turn-id shape the transcript's own entry-index
// numbering owns: "turn_<position>", minted at read time for every entry that
// carries no reserved id of its own.
//
// A turn id persisted ON an entry is a reservation the daemon minted for a
// client-authored mutation, and the reseeding readers honor it verbatim. Those
// reservations now live in their own namespace ("turn_m<sequence>"), precisely
// so a reservation can never name the same turn as an entry's position — but
// reservations minted before that namespace existed are still on disk, and each
// one publishes its id for two different turns once a session is reseeded.
// This shape is what identifies them.
//
// The two namespaces are owned by the serving side (the transcript reader's
// entry-index fallback and the daemon's reservation minter), which this module
// cannot import: they are in another module and behind an internal wall. So the
// shape is restated here, the way the client-mutation store's shape is
// restated in mutations.go, and the sweep is a one-time cleanup instrument
// rather than a permanent invariant.
var entryIndexTurnID = regexp.MustCompile(`^turn_[0-9]+$`)

// TurnIDScan is the sweep's answer for a whole state root: which sessions carry
// a reserved turn id inside the entry-index namespace, and which sessions it
// could not answer for at all.
type TurnIDScan struct {
	StateBase       string                 `json:"state_base"`
	SessionsScanned int                    `json:"sessions_scanned"`
	Sessions        []TurnIDSession        `json:"sessions"`
	Unreadable      []UnreadableTranscript `json:"unreadable"`
}

// TurnIDSession is one session whose persisted transcript carries at least one
// reserved turn id in the entry-index namespace, with the ids that condemn it.
type TurnIDSession struct {
	SessionID      string         `json:"session_id"`
	TranscriptRef  string         `json:"transcript_ref"`
	TranscriptPath string         `json:"transcript_path"`
	ReservedTurns  []ReservedTurn `json:"reserved_turns"`
}

// ReservedTurn is one persisted reserved turn id and the entry that carries it.
//
// EntryIndex is the entry's 1-based position among the transcript's entries —
// the same number the reader's entry-index fallback would have given it — so
// "turn_11 on entry 3" reads as exactly the collision it is: the id names both
// this entry and the eleventh. Kind is the entry's turn kind, because a
// reservation lands on whichever turn its mutation authored (a user input, a
// steer, or the diagnostic turn a failed mutation records).
type ReservedTurn struct {
	TurnID     string `json:"turn_id"`
	EntryIndex int    `json:"entry_index"`
	Kind       string `json:"kind"`
}

// UnreadableTranscript is a session the sweep could not answer for, and why.
// Reporting it as clean would be the dangerous answer: the sweep decides which
// sessions get deleted, so "I could not read this one" has to stay visible.
type UnreadableTranscript struct {
	SessionID     string `json:"session_id"`
	TranscriptRef string `json:"transcript_ref"`
	Error         string `json:"error"`
}

// ScanTurnIDs sweeps every session under stateBase for reserved turn ids minted
// inside the transcript's entry-index namespace. Unlike the other views it is
// not session-scoped: the question it answers is "which sessions on this
// machine carry the shape", so it enumerates rather than resolving a selector.
func ScanTurnIDs(stateBase string) (TurnIDScan, error) {
	sessions, err := allSessions(stateBase)
	if err != nil {
		return TurnIDScan{}, err
	}
	scan := TurnIDScan{
		StateBase:       stateBase,
		SessionsScanned: len(sessions),
		Sessions:        []TurnIDSession{},
		Unreadable:      []UnreadableTranscript{},
	}
	for _, paths := range sessions {
		doc, err := loadTranscript(paths.TranscriptPath)
		if err != nil {
			scan.Unreadable = append(scan.Unreadable, UnreadableTranscript{
				SessionID:     paths.SessionID,
				TranscriptRef: paths.TranscriptRef,
				Error:         err.Error(),
			})
			continue
		}
		reserved := entryIndexReservedTurns(doc)
		if len(reserved) == 0 {
			continue
		}
		scan.Sessions = append(scan.Sessions, TurnIDSession{
			SessionID:      paths.SessionID,
			TranscriptRef:  paths.TranscriptRef,
			TranscriptPath: paths.TranscriptPath,
			ReservedTurns:  reserved,
		})
	}
	return scan, nil
}

// entryIndexReservedTurns returns the entries whose persisted reserved turn id
// sits in the entry-index namespace, in transcript order.
func entryIndexReservedTurns(doc transcriptDoc) []ReservedTurn {
	var found []ReservedTurn
	for i, entry := range doc.Entries {
		if !entryIndexTurnID.MatchString(entry.Turn.StableTurnID) {
			continue
		}
		found = append(found, ReservedTurn{
			TurnID:     entry.Turn.StableTurnID,
			EntryIndex: i + 1,
			Kind:       string(entry.Turn.Kind),
		})
	}
	return found
}

// allSessions enumerates every session that has a transcript under stateBase,
// across every bucket, ordered by bucket then session id so two sweeps of an
// unchanged state root produce the same list.
func allSessions(stateBase string) ([]Paths, error) {
	buckets, err := resolveBuckets(stateBase)
	if err != nil {
		return nil, err
	}
	var all []Paths
	for _, b := range buckets {
		matches, err := filepath.Glob(filepath.Join(b.dir, "sessions", "*"+transcriptFileSuffix))
		if err != nil {
			return nil, fmt.Errorf("glob sessions in %s: %w", b.dir, err)
		}
		for _, match := range matches {
			sid := strings.TrimSuffix(filepath.Base(match), transcriptFileSuffix)
			// A file whose name is not a session id is not a session's
			// transcript, whatever else it may be.
			if identifier.ValidateSessionID(sid) != nil {
				continue
			}
			all = append(all, pathsFor(b, sid))
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ProjectID != all[j].ProjectID {
			return all[i].ProjectID < all[j].ProjectID
		}
		return all[i].SessionID < all[j].SessionID
	})
	return all, nil
}

const transcriptFileSuffix = ".transcript.jsonl"

// RenderTurnIDScan renders a TurnIDScan as a human-readable summary (the
// default, non-JSON output) — the list a human reads before deleting sessions.
func RenderTurnIDScan(scan TurnIDScan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "scanned %d %s under %s\n", scan.SessionsScanned, plural(scan.SessionsScanned, "session"), scan.StateBase)
	if len(scan.Sessions) == 0 {
		b.WriteString("no session carries a reserved turn id in the transcript's entry-index namespace\n")
	} else {
		fmt.Fprintf(&b, "%d %s %s a reserved turn id in the transcript's entry-index namespace:\n",
			len(scan.Sessions), plural(len(scan.Sessions), "session"), verb(len(scan.Sessions), "carries", "carry"))
		for _, session := range scan.Sessions {
			fmt.Fprintf(&b, "  · %s  (%s)\n", session.TranscriptRef, session.TranscriptPath)
			for _, reserved := range session.ReservedTurns {
				fmt.Fprintf(&b, "      %s  entry %d  %s\n", reserved.TurnID, reserved.EntryIndex, reserved.Kind)
			}
		}
	}
	if len(scan.Unreadable) > 0 {
		fmt.Fprintf(&b, "%d %s could not be read — those sessions are neither cleared nor condemned:\n",
			len(scan.Unreadable), plural(len(scan.Unreadable), "transcript"))
		for _, unreadable := range scan.Unreadable {
			fmt.Fprintf(&b, "  · %s — %s\n", unreadable.TranscriptRef, unreadable.Error)
		}
	}
	return b.String()
}

// verb returns the verb form the count takes, so one session "carries" and two
// "carry".
func verb(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
