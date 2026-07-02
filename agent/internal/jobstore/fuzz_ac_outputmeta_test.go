package jobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
)

// output.go persists a small JSON sidecar (outputMeta: total_bytes,
// retained_start, retained_sha256) next to each job's retained output tail, and
// on read validates it against the on-disk bytes with SHA-256 before trusting
// the lifetime counters. readOutputMeta decodes the untrusted JSON;
// readValidOutputMetaFs and readValidPendingOutputMeta then cross-check the
// decoded numbers and hashes against the output file, including the recovery
// path where the output file grew past what the sidecar last recorded (a crash
// between an append and its metadata flush). These readers eat untrusted sidecar
// bytes + arbitrary output content and had no fuzz coverage.
//
// FuzzAcOutputMetaCodec asserts:
//
//   - never-panic + determinism: all three readers run twice over arbitrary
//     sidecar/output/pending bytes on an in-memory fs and must agree with
//     themselves (a codec that returns different answers for identical inputs
//     is a bug even when neither answer panics).
//   - accept-valid: a correctly-hashed final sidecar for the retained bytes is
//     accepted and its counters are returned verbatim.
//   - reject-tampered: flipping the retained hash (or the byte length the hash
//     covers) must make the integrity check reject the sidecar — the validator
//     must never hand back counters it did not actually verify.
//   - accept-grown (final + pending): a sidecar describing a prefix/earlier
//     state of an output file that has since grown is accepted via the
//     prefix/suffix SHA reconstruction, and the recovered counters/hash reflect
//     the current full file.
//
// No real disk or process is touched: everything runs on afero.MemMapFs.
func FuzzAcOutputMetaCodec(f *testing.F) {
	seeds := []struct {
		output      []byte
		meta        []byte
		start       uint32
		tamperHash  bool
		tamperTotal bool
		splitF      uint16
		splitM      uint16
	}{
		{[]byte("hello\nworld\n"), []byte(`{"total_bytes":12,"retained_start":0,"retained_sha256":"x"}`), 0, false, false, 4, 3},
		{[]byte(""), []byte(`{}`), 0, false, false, 0, 0},
		{[]byte("abc"), []byte(`{"total_bytes":-1}`), 0, false, false, 1, 1},
		{[]byte("abc"), []byte(`not json at all`), 5, false, false, 2, 0},
		{[]byte("payload bytes"), nil, 100, true, false, 3, 5},
		{[]byte("payload bytes"), nil, 7, false, true, 6, 2},
		{make([]byte, 0), []byte(`{"total_bytes":0,"retained_start":0,"retained_sha256":"` +
			hex.EncodeToString(sha256sum(nil)) + `"}`), 0, false, false, 0, 0},
	}
	for _, s := range seeds {
		f.Add(s.output, s.meta, s.start, s.tamperHash, s.tamperTotal, s.splitF, s.splitM)
	}

	f.Fuzz(func(t *testing.T, output, metaBytes []byte, startRaw uint32, tamperHash, tamperTotal bool, splitF, splitM uint16) {
		const (
			outputPath = "job/out.log"
			metaPath   = "job/out.log.meta.json"
		)
		pendingPath := outputPendingMetaPath(metaPath)

		// --- Part A: never-panic + determinism over arbitrary sidecar bytes. ---
		fs := afero.NewMemMapFs()
		mustWrite(t, fs, outputPath, output)
		mustWrite(t, fs, metaPath, metaBytes)
		mustWrite(t, fs, pendingPath, metaBytes)
		retained := int64(len(output))

		assertReadOutputMetaDeterministic(t, fs, metaPath)
		assertValidReaderDeterministic(t, "final", fs, metaPath, outputPath, retained,
			func(fs afero.Fs) (outputMeta, bool, error) {
				return readValidOutputMetaFs(fs, metaPath, outputPath, retained)
			})
		assertValidReaderDeterministic(t, "pending", fs, metaPath, outputPath, retained,
			func(fs afero.Fs) (outputMeta, bool, error) {
				return readValidPendingOutputMeta(fs, pendingPath, metaPath, outputPath, retained)
			})

		// --- Part B: accept-valid / reject-tampered on a freshly built sidecar. ---
		start := int64(startRaw)
		want := outputMeta{
			TotalBytes:     start + int64(len(output)),
			RetainedStart:  start,
			RetainedSHA256: hex.EncodeToString(sha256sum(output)),
		}
		if tamperHash {
			want.RetainedSHA256 = flipHex(want.RetainedSHA256)
		}
		if tamperTotal {
			want.TotalBytes++
		}

		vfs := writeOutputAndMeta(t, outputPath, output, metaPath, want)
		got, ok, verr := readValidOutputMetaFs(vfs, metaPath, outputPath, int64(len(output)))
		tampered := tamperHash || tamperTotal
		switch {
		case tampered:
			if ok {
				t.Fatalf("tampered sidecar accepted (tamperHash=%v tamperTotal=%v): %+v", tamperHash, tamperTotal, got)
			}
		case verr != nil || !ok:
			t.Fatalf("valid sidecar rejected: ok=%v err=%v (start=%d len=%d)", ok, verr, start, len(output))
		case got != want:
			t.Fatalf("valid sidecar counters not returned verbatim:\n  got =%+v\n  want=%+v", got, want)
		}

		// The remaining growth oracles need at least one output byte to have a
		// non-degenerate prefix/suffix split.
		if len(output) == 0 {
			return
		}
		L := int64(len(output))

		// --- Part C: final sidecar that recorded a prefix of a since-grown file. ---
		// metaRetained = F < retained = L, so the file is [prefix][appended].
		F := int64(splitF) % L
		prefixMeta := outputMeta{
			TotalBytes:     start + F,
			RetainedStart:  start,
			RetainedSHA256: hex.EncodeToString(sha256sum(output[:F])),
		}
		cfs := writeOutputAndMeta(t, outputPath, output, metaPath, prefixMeta)
		cgot, cok, cerr := readValidOutputMetaFs(cfs, metaPath, outputPath, L)
		if cerr != nil || !cok {
			t.Fatalf("grown final sidecar rejected: ok=%v err=%v (F=%d L=%d)", cok, cerr, F, L)
		}
		wantC := outputMeta{
			TotalBytes:     start + L,
			RetainedStart:  start,
			RetainedSHA256: hex.EncodeToString(sha256sum(output)),
		}
		if cgot != wantC {
			t.Fatalf("grown final recovery mismatch:\n  got =%+v\n  want=%+v", cgot, wantC)
		}

		// --- Part D: pending sidecar recovery across a grown file + final meta. ---
		// Models a crash after appending: the final meta describes a prefix
		// (first F bytes, retained_start 0), and the pending meta describes the
		// last metaR bytes of the now-larger file. The validator must stitch
		// them: verify the prefix hash, verify the suffix hash, recompute the
		// full hash, and report the current lifetime counters.
		metaR := int64(splitM) % L // 0..L-1, strictly less than retained
		finalMeta := outputMeta{
			TotalBytes:     F,
			RetainedStart:  0,
			RetainedSHA256: hex.EncodeToString(sha256sum(output[:F])),
		}
		pendingMeta := outputMeta{
			TotalBytes:     L,
			RetainedStart:  L - metaR,
			RetainedSHA256: hex.EncodeToString(sha256sum(output[L-metaR:])),
		}
		dfs := writeOutputAndMeta(t, outputPath, output, metaPath, finalMeta)
		pb, err := json.Marshal(pendingMeta)
		if err != nil {
			t.Fatalf("marshal pending meta: %v", err)
		}
		if err := afero.WriteFile(dfs, pendingPath, append(pb, '\n'), 0o644); err != nil {
			t.Fatalf("write pending meta: %v", err)
		}
		dgot, dok, derr := readValidPendingOutputMeta(dfs, pendingPath, metaPath, outputPath, L)
		if derr != nil || !dok {
			t.Fatalf("grown pending sidecar rejected: ok=%v err=%v (F=%d metaR=%d L=%d)", dok, derr, F, metaR, L)
		}
		wantD := outputMeta{
			TotalBytes:     L,
			RetainedStart:  0,
			RetainedSHA256: hex.EncodeToString(sha256sum(output)),
		}
		if dgot != wantD {
			t.Fatalf("grown pending recovery mismatch:\n  got =%+v\n  want=%+v", dgot, wantD)
		}
	})
}

// writeOutputAndMeta builds a fresh in-memory fs holding the output file (always
// present, even when empty) and a JSON-encoded sidecar at metaPath.
func writeOutputAndMeta(t *testing.T, outputPath string, output []byte, metaPath string, meta outputMeta) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, outputPath, output, 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	if err := afero.WriteFile(fs, metaPath, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	return fs
}

func assertReadOutputMetaDeterministic(t *testing.T, fs afero.Fs, path string) {
	t.Helper()
	m1, ok1, e1 := readOutputMeta(fs, path)
	m2, ok2, e2 := readOutputMeta(fs, path)
	if ok1 != ok2 || (e1 == nil) != (e2 == nil) || m1 != m2 {
		t.Fatalf("readOutputMeta nondeterministic: (%+v,%v,%v) vs (%+v,%v,%v)", m1, ok1, e1, m2, ok2, e2)
	}
}

func assertValidReaderDeterministic(t *testing.T, label string, fs afero.Fs, metaPath, outputPath string, retained int64, read func(afero.Fs) (outputMeta, bool, error)) {
	t.Helper()
	m1, ok1, e1 := read(fs)
	m2, ok2, e2 := read(fs)
	if ok1 != ok2 || (e1 == nil) != (e2 == nil) || m1 != m2 {
		t.Fatalf("%s reader nondeterministic: (%+v,%v,%v) vs (%+v,%v,%v)", label, m1, ok1, e1, m2, ok2, e2)
	}
}

func mustWrite(t *testing.T, fs afero.Fs, path string, b []byte) {
	t.Helper()
	if b == nil {
		return
	}
	if err := afero.WriteFile(fs, path, b, 0o644); err != nil {
		t.Fatalf("seed write %s: %v", path, err)
	}
}

func sha256sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// flipHex returns a hex string guaranteed to differ from s: it flips the first
// nibble (mapping the empty string to a non-empty, definitely-wrong value so the
// tamper case is always an actual mismatch).
func flipHex(s string) string {
	if s == "" {
		return "00"
	}
	b := []byte(s)
	if b[0] == '0' {
		b[0] = '1'
	} else {
		b[0] = '0'
	}
	return string(b)
}
