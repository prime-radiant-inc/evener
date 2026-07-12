//go:build serffuzz

package jobstore

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

func FuzzJobstorePackageUnion(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		jobstorePackageMetadataEdges(t)
		jobstorePackageStoreEdges(t)
		jobstorePackageMatcherEdges(t)
	})
}

func jobstorePackageMetadataEdges(t *testing.T) {
	t.Helper()
	base := afero.NewMemMapFs()
	const output = "/output.log"
	const final = "/output.meta.json"
	const pending = "/output.meta.json.pending"
	mustWrite := func(path string, data []byte) {
		if err := afero.WriteFile(base, path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hash := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
	mustWrite(output, []byte("abcdef"))

	want := errors.New("marshal metadata")
	if err := writeOutputMetaFileFsSyncMarshal(base, final, outputMeta{}, false, func(any) ([]byte, error) { return nil, want }); !errors.Is(err, want) {
		t.Fatalf("marshal error = %v", err)
	}
	if err := writeOutputMetaFileFsSync(&jcpHookFS{Fs: base, wrap: func(f afero.File) afero.File { return &jcpHookFile{File: f, closeErr: jcpInjectedErr} }}, final, outputMeta{}, false); err == nil {
		t.Fatal("close metadata succeeded")
	}

	writeMeta := func(path string, meta outputMeta) {
		if err := writeOutputMetaFileFsSync(base, path, meta, false); err != nil {
			t.Fatal(err)
		}
	}
	writeMeta(pending, outputMeta{TotalBytes: 6, RetainedStart: 2, RetainedSHA256: hash([]byte("cdef"))})
	writeMeta(final, outputMeta{TotalBytes: 6, RetainedStart: 0, RetainedSHA256: hash([]byte("abcdef"))})
	if total, start, err := readOutputMetaForFile(base, final, output, 6); err != nil || total != 6 || start != 0 {
		t.Fatalf("pending recovery = %d/%d/%v", total, start, err)
	}

	cases := []struct {
		pending, final outputMeta
		retained       int64
	}{
		{outputMeta{TotalBytes: 3, RetainedStart: 0, RetainedSHA256: "bad"}, outputMeta{}, 6},
		{outputMeta{TotalBytes: 5, RetainedStart: 1, RetainedSHA256: hash([]byte("cdef"))}, outputMeta{}, 6},
		{outputMeta{TotalBytes: 8, RetainedStart: 4, RetainedSHA256: hash([]byte("cdef"))}, outputMeta{}, 6},
		{outputMeta{TotalBytes: 8, RetainedStart: 4, RetainedSHA256: hash([]byte("cdef"))}, outputMeta{TotalBytes: 9, RetainedStart: 0, RetainedSHA256: hash([]byte("abcdef"))}, 6},
		{outputMeta{TotalBytes: 6, RetainedStart: 0, RetainedSHA256: "bad"}, outputMeta{}, 6},
		{outputMeta{TotalBytes: 7, RetainedStart: 0, RetainedSHA256: hash([]byte("abcdef"))}, outputMeta{}, 6},
	}
	for _, tc := range cases {
		_ = base.Remove(final)
		_ = base.Remove(pending)
		writeMeta(pending, tc.pending)
		if tc.final.TotalBytes != 0 {
			writeMeta(final, tc.final)
		}
		_, _, _ = readOutputMetaForFile(base, final, output, tc.retained)
	}

	for _, meta := range []outputMeta{
		{TotalBytes: 3, RetainedStart: 0, RetainedSHA256: hash([]byte("abc"))},
		{TotalBytes: 3, RetainedStart: 0, RetainedSHA256: "bad"},
		{TotalBytes: 7, RetainedStart: 0, RetainedSHA256: hash([]byte("abcdef"))},
	} {
		_ = base.Remove(pending)
		writeMeta(final, meta)
		_, _, _ = readOutputMetaForFile(base, final, output, 6)
	}

	fault := &jcpHookFS{Fs: base, openErr: jcpInjectedErr}
	_, _, _ = readOutputMetaForFile(fault, final, output, 6)
	_, _, _ = readValidPendingOutputMeta(fault, pending, final, output, 6)
	_, _, _ = readValidOutputMetaFs(fault, final, output, 6)
	_, _ = outputFileHasSuffixSHA256(fault, output, 0, 1, "")
	_, _ = outputFileHasSuffixSHA256(base, output, 5, 3, "")

	metaCases := []struct {
		pending, final outputMeta
		failAt         int
	}{
		{outputMeta{TotalBytes: 6, RetainedStart: 2, RetainedSHA256: hash([]byte("cdef"))}, outputMeta{TotalBytes: 6}, 1},
		{outputMeta{TotalBytes: 6, RetainedStart: 2, RetainedSHA256: hash([]byte("cdef"))}, outputMeta{TotalBytes: 1, RetainedStart: 2}, 0},
		{outputMeta{TotalBytes: 6, RetainedStart: 2, RetainedSHA256: hash([]byte("cdef"))}, outputMeta{TotalBytes: 6, RetainedSHA256: "bad"}, 0},
		{outputMeta{TotalBytes: 6, RetainedStart: 2, RetainedSHA256: hash([]byte("cdef"))}, outputMeta{TotalBytes: 6, RetainedSHA256: hash([]byte("abcdef"))}, 3},
	}
	for _, tc := range metaCases {
		writeMeta(pending, tc.pending)
		writeMeta(final, tc.final)
		fs := afero.Fs(base)
		if tc.failAt > 0 {
			fs = &jobstorePackageOpenFaultFS{Fs: base, path: output, failAt: tc.failAt}
		}
		_, _, _ = readOutputMetaForFile(fs, final, output, 6)
	}
	writeMeta(pending, outputMeta{TotalBytes: 6, RetainedStart: 2, RetainedSHA256: hash([]byte("cdef"))})
	writeMeta(final, outputMeta{TotalBytes: 6, RetainedSHA256: hash([]byte("abcdef"))})
	_, _, _ = readOutputMetaForFile(&jobstorePackageOpenFaultFS{Fs: base, path: final, failAt: 1}, final, output, 6)
	writeMeta(pending, outputMeta{TotalBytes: 6, RetainedSHA256: hash([]byte("abcdef"))})
	_, _, _ = readValidPendingOutputMeta(&jobstorePackageOpenFaultFS{Fs: base, path: output, failAt: 1}, pending, final, output, 6)
	for _, tc := range []struct {
		meta   outputMeta
		failAt int
	}{
		{outputMeta{TotalBytes: 3, RetainedSHA256: hash([]byte("abc"))}, 1},
		{outputMeta{TotalBytes: 3, RetainedSHA256: hash([]byte("abc"))}, 2},
		{outputMeta{TotalBytes: 6, RetainedSHA256: hash([]byte("abcdef"))}, 1},
	} {
		_ = base.Remove(pending)
		writeMeta(final, tc.meta)
		_, _, _ = readOutputMetaForFile(&jobstorePackageOpenFaultFS{Fs: base, path: output, failAt: tc.failAt}, final, output, 6)
	}

	// Keep the prefix-mismatch arm independent from the fault-sweep state above.
	prefixFS := afero.NewMemMapFs()
	if err := afero.WriteFile(prefixFS, output, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOutputMetaFileFsSync(prefixFS, pending, outputMeta{
		TotalBytes: 6, RetainedStart: 2, RetainedSHA256: hash([]byte("cdef")),
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := writeOutputMetaFileFsSync(prefixFS, final, outputMeta{
		TotalBytes: 6, RetainedStart: 0, RetainedSHA256: hash([]byte("wrong-prefix")),
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readOutputMetaForFile(prefixFS, final, output, 6); err == nil {
		t.Fatal("mismatched final prefix hash was accepted")
	}
	if err := writeOutputMetaFileFsSync(prefixFS, final, outputMeta{
		TotalBytes: 6, RetainedStart: 0, RetainedSHA256: hash([]byte("abcdef")),
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readOutputMetaForFile(&jobstorePackageOpenFaultFS{
		Fs: prefixFS, path: output, failAt: 2,
	}, final, output, 6); err == nil {
		t.Fatal("prefix hash open fault was ignored")
	}
}

func jobstorePackageStoreEdges(t *testing.T) {
	t.Helper()
	const path = "/jobs.jsonl"
	for _, setup := range []func(afero.Fs){nil, func(fs afero.Fs) { _ = afero.WriteFile(fs, path, []byte("\n"), 0o644) }} {
		base := afero.NewMemMapFs()
		if setup != nil {
			setup(base)
		}
		s := &Store{fs: base, path: path, disableSync: true}
		_, _ = s.readAll()
	}
	for _, fs := range []afero.Fs{
		&jcpHookFS{Fs: afero.NewMemMapFs(), openErr: jcpInjectedErr},
		&jcpHookFS{Fs: afero.NewMemMapFs(), statErr: jcpInjectedErr},
	} {
		s := &Store{fs: fs, path: path, disableSync: true}
		_, _ = s.readAll()
	}
	base := afero.NewMemMapFs()
	_ = afero.WriteFile(base, path, []byte("{}\n"), 0o644)
	for _, fault := range []string{"close", "read"} {
		calls := 0
		fs := &jcpHookFS{Fs: base, wrap: func(f afero.File) afero.File {
			calls++
			if calls != 2 {
				return f
			}
			if fault == "close" {
				return &jcpHookFile{File: f, closeErr: jcpInjectedErr}
			}
			return &jcpHookFile{File: f, readErr: jcpInjectedErr}
		}}
		s := &Store{fs: fs, path: path, disableSync: true}
		_, _ = s.readAll()
	}
	for _, wrap := range []func(afero.File) afero.File{
		func(f afero.File) afero.File { return &jcpHookFile{File: f, closeErr: jcpInjectedErr} },
		func(f afero.File) afero.File { return &jcpHookFile{File: f, seekErr: jcpInjectedErr} },
	} {
		fs := &jcpHookFS{Fs: base, wrap: wrap}
		s := &Store{fs: fs, path: path, disableSync: true}
		_, _ = s.readAll()
	}
	_ = afero.WriteFile(base, path, []byte("{}"), 0o644)
	calls := 0
	fs := &jcpHookFS{Fs: base, wrap: func(f afero.File) afero.File {
		calls++
		if calls == 2 {
			return &jcpHookFile{File: f, readErr: jcpInjectedErr}
		}
		return f
	}}
	s := &Store{fs: fs, path: path, disableSync: true}
	_, _ = s.readAll()
	_ = os.ErrNotExist
}

func jobstorePackageMatcherEdges(t *testing.T) {
	t.Helper()
	m := NewOutputMatcher(regexp.MustCompile("x"))
	m.carry = make([]byte, maxOutputMatcherLineBytes+1)
	_ = m.FlushWithProvenance(nil)
	_, _ = grepReaderLimit(bufio.NewReaderSize(&jobstorePackageErrReader{}, 1), regexp.MustCompile("x"), 8, 0, 8)
	_, _ = grepReaderLimit(bufio.NewReader(bytes.NewBufferString("xxxx\r")), regexp.MustCompile("x"), 8, 0, 4)
	_, _ = grepFileLimitAtOpen("x", regexp.MustCompile("x"), 8, 0, 8, 0, func(string) (io.ReadCloser, error) {
		return &jobstorePackageReadCloser{Reader: strings.NewReader("x\n"), closeErr: jcpInjectedErr}, nil
	})
	_, _ = grepFileLimitAtOpen("x", regexp.MustCompile("x"), 8, 0, 8, 0, func(string) (io.ReadCloser, error) {
		return &jobstorePackageReadCloser{Reader: &jobstorePackageErrReader{}}, nil
	})
}

type jobstorePackageErrReader struct{}

func (*jobstorePackageErrReader) Read([]byte) (int, error) { return 0, jcpInjectedErr }

type jobstorePackageOpenFaultFS struct {
	afero.Fs
	path          string
	failAt, calls int
}

func (fs *jobstorePackageOpenFaultFS) Open(name string) (afero.File, error) {
	if name == fs.path {
		fs.calls++
		if fs.calls == fs.failAt {
			return nil, jcpInjectedErr
		}
	}
	return fs.Fs.Open(name)
}

type jobstorePackageReadCloser struct {
	io.Reader
	closeErr error
}

func (r *jobstorePackageReadCloser) Close() error { return r.closeErr }
