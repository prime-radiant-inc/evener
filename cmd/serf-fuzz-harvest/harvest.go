package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
)

// runner carries the shared harvest state: where seeds land, the emitter, the
// optional provenance log, per-surface stats, and the secret-leak tally that
// drives the process exit code.
type runner struct {
	outRoot string
	emit    *Emitter
	log     io.Writer // optional per-seed provenance; nil discards
	stats   map[string]*surfaceStat
	leaks   int
}

// surfaceStat is the one-line summary accounting for a single surface.
type surfaceStat struct {
	scanned   int // source records examined
	seeds     int // seeds written (or, under --dry-run, would-write)
	oversized int // dropped for exceeding --max-seed-bytes
	skipped   int // unscrubbable (e.g. malformed JSON) — dropped for safety
	leaks     int // dropped because a secret survived sanitization
}

func newRunner(outRoot string, emit *Emitter, log io.Writer) *runner {
	return &runner{outRoot: outRoot, emit: emit, log: log, stats: map[string]*surfaceStat{}}
}

func (r *runner) stat(surface string) *surfaceStat {
	s := r.stats[surface]
	if s == nil {
		s = &surfaceStat{}
		r.stats[surface] = s
	}
	return s
}

func (r *runner) dir(rel string) string { return filepath.Join(r.outRoot, rel) }

func (r *runner) logf(format string, args ...any) {
	if r.log == nil {
		return
	}
	fmt.Fprintf(r.log, format+"\n", args...) //nolint:errcheck
}

// scrub runs one payload through the sanitizer's full pipeline, recording a leak
// (and tallying it toward the non-zero exit) or an unscrubbable skip. It returns
// the sanitized bytes and whether they are safe to emit.
func (r *runner) scrub(st *surfaceStat, san *Sanitizer, raw []byte, sse bool) ([]byte, bool) {
	out, err := san.Process(raw, sse)
	if err != nil {
		var leak *SecretLeakError
		if errors.As(err, &leak) {
			st.leaks++
			r.leaks++
			r.logf("LEAK dropped seed: %v", err)
			return nil, false
		}
		st.skipped++
		return nil, false
	}
	return out, true
}

// gateString redacts known secrets in a structural string (an http path suffix)
// and runs the abort gate, preserving the value otherwise (so path-traversal
// shapes survive). It returns the safe string and whether it may be emitted.
func (r *runner) gateString(st *surfaceStat, s string) (string, bool) {
	red := redactKnownSecrets([]byte(s))
	if finding := detectSecret(red, false); finding != "" {
		st.leaks++
		r.leaks++
		r.logf("LEAK dropped http suffix: %s", finding)
		return "", false
	}
	return string(red), true
}

// recordEmit folds one Emit result into a surface's stats.
func (r *runner) recordEmit(st *surfaceStat, status emitStatus) {
	switch status {
	case statusWritten, statusDryRun:
		st.seeds++
	case statusOversized:
		st.oversized++
	}
}

// emitBytesTo scrubs+emits a single-arg seed to one or more target dirs.
func (r *runner) emitBytesTo(st *surfaceStat, out []byte, dirs ...string) {
	for _, d := range dirs {
		status, err := r.emit.EmitBytes(d, out)
		if err != nil {
			r.logf("emit error %s: %v", d, err)
			continue
		}
		r.recordEmit(st, status)
	}
}

// summary renders the per-surface one-liners in a stable order.
func (r *runner) summary() string {
	surfaces := make([]string, 0, len(r.stats))
	for s := range r.stats {
		surfaces = append(surfaces, s)
	}
	sort.Strings(surfaces)
	var b []byte
	for _, s := range surfaces {
		st := r.stats[s]
		line := fmt.Sprintf("%s: scanned %d → %d seeds / %d oversized / %d skipped / %d secret-aborts\n",
			s, st.scanned, st.seeds, st.oversized, st.skipped, st.leaks)
		b = append(b, line...)
	}
	return string(b)
}
