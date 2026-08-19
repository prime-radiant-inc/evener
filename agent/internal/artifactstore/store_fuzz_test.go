//go:build evenerfuzz

package artifactstore

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzArtifactStoreRefBoundary drives the store's untrusted-input seam: the ref
// string handed to Open, plus the bytes handed to Put.
//
// A ref reaches Open from model-authored tool arguments, so it is exactly the
// kind of value this repo treats as hostile. The oracles are semantic rather
// than "it did not panic":
//
//   - Round trip. Whatever Put stored, Open returns byte-for-byte. A store that
//     silently truncates or re-encodes an artifact is broken even though nothing
//     panics, and arbitrary bytes are the interesting input — NUL, newlines,
//     invalid UTF-8, empty.
//   - Containment. Open must only ever hand back a file the store itself
//     created. `ref` is joined into a filesystem path via the refs map, so the
//     property worth proving against a fuzzer is that no ref value — traversal
//     sequences, absolute paths, separators, hex-shaped near-misses — opens
//     anything the store did not issue, or anything outside its directory.
//   - Expiry. After Close, both Put and Open report ErrExpired rather than
//     touching a directory that has been removed.
//
// The seam is safe to fuzz: Put takes content, never a caller-supplied path, so
// nothing here executes a handler or writes outside the temp root.
func FuzzArtifactStoreRefBoundary(f *testing.F) {
	valid := refPrefix + strings.Repeat("0", refIDLength)
	f.Add([]byte("artifact body"), valid)
	f.Add([]byte(""), "")
	f.Add([]byte{0, '\n', 0xff}, refPrefix+"../../../etc/passwd")
	f.Add([]byte("x"), "/etc/passwd")
	f.Add([]byte("x"), refPrefix+strings.Repeat("g", refIDLength)) // hex-shaped, non-hex
	f.Add([]byte("x"), refPrefix)                                  // prefix with no id
	f.Add([]byte("x"), strings.ToUpper(valid))                     // uppercase hex

	f.Fuzz(func(t *testing.T, data []byte, ref string) {
		store, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = store.Close() }()

		root, err := filepath.EvalSymlinks(store.dir)
		if err != nil {
			t.Fatalf("resolving store dir: %v", err)
		}

		issued, err := store.Put(data)
		if err != nil {
			t.Fatalf("Put(%d bytes): %v", len(data), err)
		}
		if !validRef(issued) {
			t.Fatalf("Put issued %q, which validRef rejects", issued)
		}

		handle, err := store.Open(issued)
		if err != nil {
			t.Fatalf("Open(%q) after Put: %v", issued, err)
		}
		got, readErr := io.ReadAll(handle)
		_ = handle.Close()
		if readErr != nil {
			t.Fatalf("reading %q: %v", issued, readErr)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("round trip changed the artifact: got %d bytes, stored %d", len(got), len(data))
		}

		// The containment oracle. An arbitrary ref may legitimately succeed only
		// when it is the one Put just issued; anything else opening a file means
		// the ref reached the filesystem without being validated.
		if opened, err := store.Open(ref); err == nil {
			name := opened.Name()
			_ = opened.Close()
			if ref != issued {
				t.Fatalf("Open accepted ref %q that Put never issued (opened %q)", ref, name)
			}
			resolved, err := filepath.EvalSymlinks(name)
			if err != nil {
				t.Fatalf("resolving opened path %q: %v", name, err)
			}
			if !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
				t.Fatalf("Open(%q) escaped the store: %q is outside %q", ref, resolved, root)
			}
		} else if ref == issued {
			t.Fatalf("Open rejected the ref Put issued (%q): %v", ref, err)
		}

		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if _, err := store.Put(data); !errors.Is(err, ErrExpired) {
			t.Fatalf("Put after Close = %v, want ErrExpired", err)
		}
		// A closed store must report expiry for a ref it really did issue; an
		// unissued one is free to report either expiry or invalidity.
		if _, err := store.Open(issued); !errors.Is(err, ErrExpired) {
			t.Fatalf("Open(%q) after Close = %v, want ErrExpired", issued, err)
		}
	})
}
