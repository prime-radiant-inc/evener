package transcript

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzTranscriptWriterPersistence is the differential that proves the afero
// filesystem seam changes nothing observable: the same program of writer
// operations (Append / AppendDurable / AppendAPICall / Close+reopen), replayed
// through two Writers whose ONLY difference is the injected afero.Fs — one an OS
// filesystem sandboxed under a t.TempDir (afero.NewBasePathFs over
// afero.NewOsFs), the other a pure in-memory afero.NewMemMapFs — must produce
// byte-identical persisted JSONL and identical error outcomes after every op.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to OsFs via NewWriter/OpenWriter, so if the MemMapFs path ever diverges from
// the OsFs path the refactor is unsound) and (b) a new in-memory persistence
// fuzzer that drives the create → append → sync → close → reopen (partial-line
// truncation + seq recovery) machinery with zero real-disk dependency in the
// mem lane.
//
// The fuzzed bytes decode into an op sequence. Every payload (turn, api_call,
// and any raw tail-corruption fragment used to exercise OpenWriter's partial
// line recovery) is built deterministically from the fuzz bytes and fed
// IDENTICALLY to both lanes, so equal programs marshal equal bytes; the oracle
// is purely the cross-filesystem differential. The Writer mints no persisted
// timestamps (lastSync is internal and never serialized), so no clock injection
// is needed for byte identity.
//
// Oracle checked after EVERY operation:
//   - error parity: an op errors on the OS lane iff it errors on the mem lane.
//   - byte-identical persistence: the persisted JSONL file (read back through
//     each lane's own fs at the same logical path) is byte-for-byte equal.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. No network, no
// subprocess, no destructive op outside the sandbox.
func FuzzTranscriptWriterPersistence(f *testing.F) {
	f.Add([]byte{opAppend, 1, opAppendDurable, 2, opAPICall, 3, opReopen, 0})
	f.Add([]byte{opAppend, 5, opAppend, 6, opReopen, 0, opAppend, 7})
	f.Add([]byte{opAPICall, 1, opAPICall, 2, opAppendDurable, 3, opReopen, 1, 42})
	f.Add([]byte{opReopen, 1, 9, opAppend, 1, opReopen, 0})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		const path = "/session/transcript.jsonl"
		header := Header{
			SessionID: "fuzz",
			CreatedAt: time.Unix(0, 0).UTC(),
			ProfileID: "openai",
			Model:     "gpt-5.5",
		}

		osFS := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFS := afero.NewMemMapFs()

		osW, errOS := newWriterFS(osFS, path, header)
		memW, errMem := newWriterFS(memFS, path, header)
		requireErrParity(t, "NewWriter", errOS, errMem)
		if errOS != nil {
			return
		}
		requireSamePersistedBytes(t, osFS, memFS, path)

		r := &byteReader{b: program}
		const maxOps = 128
		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % opCount {
			case opAppend:
				turn := makeTurn(r.next())
				errOS := osW.Append(turn)
				errMem := memW.Append(turn)
				requireErrParity(t, "Append", errOS, errMem)
			case opAppendDurable:
				turn := makeTurn(r.next())
				errOS := osW.AppendDurable(turn)
				errMem := memW.AppendDurable(turn)
				requireErrParity(t, "AppendDurable", errOS, errMem)
			case opAPICall:
				call := makeAPICall(r.next())
				errOS := osW.AppendAPICall(call)
				errMem := memW.AppendAPICall(call)
				requireErrParity(t, "AppendAPICall", errOS, errMem)
			case opReopen:
				if err := osW.Close(); err != nil {
					t.Fatalf("os Close: %v", err)
				}
				if err := memW.Close(); err != nil {
					t.Fatalf("mem Close: %v", err)
				}
				// Optionally corrupt the tail with a raw partial fragment
				// (no trailing newline) to exercise OpenWriter's partial-line
				// truncation identically on both lanes.
				if r.next()%2 == 1 {
					frag := makeFragment(r.next())
					appendRaw(t, osFS, path, frag)
					appendRaw(t, memFS, path, frag)
					requireSamePersistedBytes(t, osFS, memFS, path)
				}
				var errOS, errMem error
				osW, errOS = openWriterFS(osFS, path)
				memW, errMem = openWriterFS(memFS, path)
				requireErrParity(t, "OpenWriter", errOS, errMem)
				if errOS != nil {
					// Both failed to reopen (parity held); restart fresh so the
					// remaining program still has live writers to drive.
					osW, errOS = newWriterFS(osFS, path, header)
					memW, errMem = newWriterFS(memFS, path, header)
					requireErrParity(t, "NewWriter(restart)", errOS, errMem)
					if errOS != nil {
						return
					}
				}
			}
			requireSamePersistedBytes(t, osFS, memFS, path)
		}

		_ = osW.Close()
		_ = memW.Close()
		requireSamePersistedBytes(t, osFS, memFS, path)
	})
}

// Op codes for the persistence fuzzer program.
const (
	opAppend = iota
	opAppendDurable
	opAPICall
	opReopen
	opCount
)

// byteReader draws one byte at a time from the fuzz program, returning 0 once
// exhausted so op decoding stays deterministic at the tail.
type byteReader struct {
	b []byte
	i int
}

func (r *byteReader) more() bool { return r.i < len(r.b) }

func (r *byteReader) next() byte {
	if r.i >= len(r.b) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

// makeTurn builds a deterministic turn from a single byte so both lanes receive
// an identical value and therefore marshal identical bytes.
func makeTurn(b byte) schema.Turn {
	kinds := []schema.TurnKind{
		schema.TurnUserInput,
		schema.TurnAssistant,
		schema.TurnToolResults,
		schema.TurnSummary,
	}
	return schema.Turn{
		Kind:      kinds[int(b)%len(kinds)],
		Message:   llm.Assistant(fmt.Sprintf("m%d", b)),
		Timestamp: time.Unix(int64(b), 0).UTC(),
	}
}

// makeAPICall builds a deterministic api_call record from a single byte.
func makeAPICall(b byte) APICall {
	return APICall{
		Round:        int(b),
		Timestamp:    time.Unix(int64(b), 0).UTC().Format(time.RFC3339),
		LatencyMs:    int64(b),
		SystemPrompt: fmt.Sprintf("sp%d", b),
	}
}

// makeFragment builds a raw partial-line fragment (never containing a newline)
// to append to a closed transcript, exercising OpenWriter's truncation path.
func makeFragment(b byte) []byte {
	return []byte(fmt.Sprintf("partial-junk-%d", b))
}

// appendRaw appends bytes to the tail of an existing file through the given fs,
// simulating a crash that left an unterminated last line.
func appendRaw(t *testing.T, fs afero.Fs, path string, data []byte) {
	t.Helper()
	f, err := fs.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open for raw append: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatalf("raw append write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("raw append close: %v", err)
	}
}

// requireErrParity fails unless both lanes agree on whether the op errored.
func requireErrParity(t *testing.T, op string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("%s error parity broken: os=%v mem=%v", op, errOS, errMem)
	}
}

// requireSamePersistedBytes reads the persisted file through each lane's own fs
// at the shared logical path and asserts the bytes are identical. A missing
// file (nothing persisted yet) reads as nil on both, which also matches.
func requireSamePersistedBytes(t *testing.T, osFS, memFS afero.Fs, path string) {
	t.Helper()
	osBytes := readAll(osFS, path)
	memBytes := readAll(memFS, path)
	if !bytes.Equal(osBytes, memBytes) {
		t.Fatalf("persisted bytes diverge across filesystems:\n os =%q\n mem=%q", osBytes, memBytes)
	}
}

func readAll(fs afero.Fs, path string) []byte {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil
	}
	return data
}
