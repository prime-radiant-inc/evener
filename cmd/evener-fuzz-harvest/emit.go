package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
)

// Emitter writes seeds into a target's native testdata/fuzz/<FuzzName>/ in Go's
// corpus-literal format, content-addressed so identical (post-scrub) seeds
// collapse to one file. Dedup + an on-disk skip make re-runs idempotent.
type Emitter struct {
	dryRun       bool
	maxSeedBytes int
	seen         map[string]bool

	written    int
	deduped    int
	oversized  int
	wouldWrite int
}

// NewEmitter builds an emitter with the given drop threshold (seed-data bytes).
func NewEmitter(dryRun bool, maxSeedBytes int) *Emitter {
	return &Emitter{dryRun: dryRun, maxSeedBytes: maxSeedBytes, seen: map[string]bool{}}
}

// emitStatus is the disposition of one Emit call, for per-surface accounting.
type emitStatus int

const (
	statusWritten   emitStatus = iota // new file written
	statusDeduped                     // identical seed already present (this run or on disk)
	statusOversized                   // seed-data bytes exceeded the drop threshold
	statusDryRun                      // would have written, but --dry-run
)

// EmitBytes writes a single-[]byte-arg seed (FuzzParseSSE, FuzzMessageDecode,
// the provider SSE targets, the jobstore-Event decode target).
func (e *Emitter) EmitBytes(dir string, data []byte) (emitStatus, error) {
	return e.write(dir, encodeBytesSeed(data), len(data))
}

// EmitIntBytes writes a two-arg (int, []byte) seed (FuzzToolArgsValidate,
// FuzzMethodParams).
func (e *Emitter) EmitIntBytes(dir string, n int, data []byte) (emitStatus, error) {
	return e.write(dir, encodeIntBytesSeed(n, data), len(data))
}

// EmitUint8String writes a (uint8, string) seed for FuzzWebHandler(routeIdx, suffix).
func (e *Emitter) EmitUint8String(dir string, n uint8, s string) (emitStatus, error) {
	return e.write(dir, encodeUint8StringSeed(n, s), len(s))
}

func (e *Emitter) write(dir string, encoded []byte, seedLen int) (emitStatus, error) {
	if seedLen > e.maxSeedBytes {
		e.oversized++
		return statusOversized, nil
	}
	sum := sha256.Sum256(encoded)
	path := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if e.seen[path] {
		e.deduped++
		return statusDeduped, nil
	}
	e.seen[path] = true
	if fileExists(path) {
		e.deduped++ // already committed; re-run produces no diff
		return statusDeduped, nil
	}
	if e.dryRun {
		e.wouldWrite++
		return statusDryRun, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return statusWritten, err
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return statusWritten, err
	}
	e.written++
	return statusWritten, nil
}

// encodeBytesSeed renders a one-argument []byte corpus file.
func encodeBytesSeed(data []byte) []byte {
	return []byte("go test fuzz v1\n[]byte(" + strconv.Quote(string(data)) + ")\n")
}

// encodeIntBytesSeed renders a two-argument (int, []byte) corpus file.
func encodeIntBytesSeed(n int, data []byte) []byte {
	return []byte("go test fuzz v1\nint(" + strconv.Itoa(n) + ")\n[]byte(" + strconv.Quote(string(data)) + ")\n")
}

// encodeUint8StringSeed renders a two-argument (uint8, string) corpus file.
func encodeUint8StringSeed(n uint8, s string) []byte {
	return []byte("go test fuzz v1\nuint8(" + strconv.Itoa(int(n)) + ")\nstring(" + strconv.Quote(s) + ")\n")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
