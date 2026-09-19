// Package research implements the SoL-Pi auto-research loop's offline
// machinery: the oracle analyzer over real session transcripts, and the
// rollout runner that drives headless harness runs against committed
// research environments. See docs/superpowers/specs/2026-09-19-sol-pi-auto-research-loop-design.md.
package research

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/transcript"
)

type sessionTranscript struct {
	Path      string
	SessionID string
	ModTime   time.Time
}

// walkSessionTranscripts finds transcript files under
// <stateBase>/projects/*/sessions/*.transcript.jsonl, newest first.
func walkSessionTranscripts(stateBase string, limit int) ([]sessionTranscript, error) {
	pattern := filepath.Join(stateBase, "projects", "*", "sessions", "*.transcript.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("walk transcripts: %w", err)
	}
	out := make([]sessionTranscript, 0, len(matches))
	for _, p := range matches {
		st, err := os.Stat(p)
		if err != nil {
			continue // raced with deletion; skip
		}
		out = append(out, sessionTranscript{
			Path:      p,
			SessionID: strings.TrimSuffix(filepath.Base(p), ".transcript.jsonl"),
			ModTime:   st.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// loadEntries decodes one transcript file. The first line must be the v2
// header: a corrupt header, and equally a file with no header at all
// (including a 0-byte file), is an error. Undecodable entry lines are skipped
// and counted: real corpora contain torn tail lines, and the oracle measures,
// it does not reject.
func loadEntries(path string) (entries []transcript.Entry, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			if _, err := transcript.DecodeHeader(line); err != nil {
				return nil, 0, fmt.Errorf("%s: %w", path, err)
			}
			first = false
			continue
		}
		e, err := transcript.DecodeEntry(line)
		if err != nil {
			skipped++
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("scan %s: %w", path, err)
	}
	if first {
		return nil, 0, fmt.Errorf("%s: no transcript header", path)
	}
	return entries, skipped, nil
}
