package hubcore

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StallThreshold is how long a session may report active with no transcript
// growth before the watcher runs the wedge probe. New constant: the wedge
// heuristic itself has no age gate; this mirrors the web client's
// LIVENESS_STALL_MS (3 min) so hub and client agree on "suspiciously quiet"
// (spec v5, round-4 B6).
const StallThreshold = 3 * time.Minute

// wedgeTranscriptMaxLineBytes bounds the scanner buffer used to read a
// transcript's tail line. Mirrors transcriptJSONLMaxLineBytes in cmd/serf-hub
// (128MiB) — duplicated rather than imported, since that constant is
// unexported in package main; every other package in this repo that scans
// transcript JSONL (agent, agent/transcript, cmd/serf-fuzz-harvest) likewise
// keeps its own copy of the same limit instead of sharing one canonical
// constant across module/package boundaries.
const wedgeTranscriptMaxLineBytes = 128 << 20

// WedgedStatus reports whether the session at entry is wedged: the live
// source may still claim it is active, but the on-disk transcript's tail
// shows the last thing recorded was a failed LLM call. The agent loop has a
// known gap where a retryable stream error (e.g. "stream ended without
// finish event") returns from session.Input without flipping the in-memory
// state back to idle; the daemon then keeps answering /status with active
// forever, even though nothing is in flight. Hub readers conclude "active"
// and the workspace UI disables steer/send. The wedge signature is: the
// last transcript line is an api_call whose Error field is non-empty (kata
// r6y9). All other tail shapes — completed assistant turns, bare USER_INPUT
// entries with no api_call yet, successful api_calls mid-round — are NOT
// wedged, because the daemon may legitimately still be processing them. A
// transcript that cannot be read (missing, truncated, malformed tail) is
// also not treated as wedged.
func WedgedStatus(entry PastEntry) bool {
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	tailKind, tailHasError := transcriptTailSummary(transcriptPath)
	return tailKind == "api_call" && tailHasError
}

// transcriptTailSummary returns the kind ("entry" | "api_call" | "") of the
// final non-empty line of the transcript at path, and whether that api_call
// recorded a non-empty Error field. Returns ("", false) on read failures so the
// caller leaves the thread status unchanged when it cannot inspect the tail.
func transcriptTailSummary(path string) (kind string, hasError bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), wedgeTranscriptMaxLineBytes)
	var lastLine []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lastLine = append(lastLine[:0], line...)
	}
	if scanner.Err() != nil || len(lastLine) == 0 {
		return "", false
	}
	var head struct {
		Kind  string `json:"kind"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(lastLine, &head); err != nil {
		return "", false
	}
	return head.Kind, strings.TrimSpace(head.Error) != ""
}

// StaleActives tracks, across successive calls sharing the same seen map,
// how long each id in cur has continuously reported Level == "working" —
// recording a first-seen timestamp the first time an id is observed working,
// and dropping it the moment it is no longer working (including having
// disappeared from cur entirely), so a later return to working restarts its
// clock rather than resuming the old one. It returns the ids that have been
// working continuously for at least StallThreshold as of now, i.e. the
// candidates the watcher should run the WedgedStatus probe against. seen is
// mutated in place; the caller owns it and passes the same map back in on
// every tick.
func StaleActives(seen map[string]time.Time, cur map[string]AttentionEntry, now time.Time) []string {
	for id := range seen {
		if e, ok := cur[id]; !ok || e.Level != "working" {
			delete(seen, id)
		}
	}
	var stale []string
	for id, e := range cur {
		if e.Level != "working" {
			continue
		}
		first, ok := seen[id]
		if !ok {
			seen[id] = now
			continue
		}
		if now.Sub(first) >= StallThreshold {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	return stale
}

// ApplyWedgeOverride flips m[id]'s Level to "error" and moves it from
// Working to Error in sum, keeping the summary consistent with the map after
// the watcher overrides a wedged entry. No-op if id is not in m or is not
// currently "working" (e.g. it changed level between derivation and probe,
// or was already overridden).
func ApplyWedgeOverride(m map[string]AttentionEntry, sum *AttentionSummary, id string) {
	e, ok := m[id]
	if !ok || e.Level != "working" {
		return
	}
	e.Level = "error"
	m[id] = e
	sum.Working--
	sum.Error++
}
