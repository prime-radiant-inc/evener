package hub

// Tests for a document read's revision identity (S9): the raw /doc/file read
// names the version it served, so the phone can tell "changed since you last
// read" from "the same file", and a client that already holds that version
// can revalidate it without the bytes.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func docRevisionOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// docTestRoot is a fresh session folder by its real path: readDocFile takes a
// path fspaths.ResolveInRoot has already symlink-resolved, and on macOS
// t.TempDir sits under /var, a symlink to /private/var.
func docTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeDocAt(t *testing.T, path string, content []byte, modified time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

// writeSparseDoc creates a file of size bytes that takes no disk space: the
// size is real, the disk use is not.
func writeSparseDoc(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

// The revision is the sha256 of the whole file, sent as a strong ETag, with
// the file's modification time in Unix milliseconds beside it. The response
// must be revalidated before reuse, so no cache serves an old version.
func TestDocFile_Raw_NamesTheRevisionItServed(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	content := []byte("# Plan\n\nStep one.\n")
	modified := time.UnixMilli(1_790_000_000_123)
	writeDocAt(t, filepath.Join(cwd, "plan.md"), content, modified)

	rec := docRawRequest(t, web, session, "plan.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("ETag"), `"`+docRevisionOf(content)+`"`; got != want {
		t.Fatalf("ETag=%q, want %q (the whole file's sha256)", got, want)
	}
	if got, want := rec.Header().Get("X-Doc-Modified-At"), strconv.FormatInt(modified.UnixMilli(), 10); got != want {
		t.Fatalf("X-Doc-Modified-At=%q, want %q", got, want)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control=%q, want private, no-cache", got)
	}
}

// An unchanged file keeps its revision, and any edit changes it, including an
// edit past the 512 KiB the read serves: two files whose served heads are
// identical still differ in revision.
func TestDocFile_Raw_RevisionFollowsTheWholeFile(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	path := filepath.Join(cwd, "long.md")
	modified := time.UnixMilli(1_790_000_000_000)
	head := bytes.Repeat([]byte("a"), docFileMaxBytes)
	writeDocAt(t, path, append(append([]byte{}, head...), "tail one"...), modified)

	first := docRawRequest(t, web, session, "long.md")
	again := docRawRequest(t, web, session, "long.md")
	if first.Header().Get("ETag") == "" || first.Header().Get("ETag") != again.Header().Get("ETag") {
		t.Fatalf("unchanged file ETags %q then %q, want one stable revision", first.Header().Get("ETag"), again.Header().Get("ETag"))
	}

	writeDocAt(t, path, append(append([]byte{}, head...), "tail two"...), modified)
	edited := docRawRequest(t, web, session, "long.md")
	if !bytes.Equal(first.Body.Bytes(), edited.Body.Bytes()) {
		t.Fatal("the served heads differ; the edit must sit past the cap for this test to mean anything")
	}
	if edited.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatalf("an edit past the cap kept revision %q", first.Header().Get("ETag"))
	}
}

// A client that sends the revision it holds gets 304 with no body while the
// file is unchanged, and the new version once it changes.
func TestDocFile_Raw_IfNoneMatchRevalidates(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	path := filepath.Join(cwd, "notes.txt")
	writeDocAt(t, path, []byte("first"), time.UnixMilli(1_790_000_000_000))
	etag := docRawRequest(t, web, session, "notes.txt").Header().Get("ETag")

	for _, header := range []string{etag, `"other", ` + etag, "W/" + etag, "*"} {
		rec := docRawRequestIfNoneMatch(t, web, session, "notes.txt", header)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
			t.Fatalf("If-None-Match %s: status=%d body=%q ETag=%q, want 304, empty, %s", header, rec.Code, rec.Body.String(), rec.Header().Get("ETag"), etag)
		}
	}

	writeDocAt(t, path, []byte("second"), time.UnixMilli(1_790_000_060_000))
	rec := docRawRequestIfNoneMatch(t, web, session, "notes.txt", etag)
	if rec.Code != http.StatusOK || rec.Body.String() != "second" {
		t.Fatalf("after an edit: status=%d body=%q, want 200 with the new version", rec.Code, rec.Body.String())
	}
}

// Hashing is bounded: a file past docRevisionMaxBytes is still served (its
// head, truncated) but names no revision, so a client falls back to comparing
// what it was shown.
func TestDocFile_Raw_NoRevisionPastTheHashLimit(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	writeSparseDoc(t, filepath.Join(cwd, "huge.log"), docRevisionMaxBytes+1)

	rec := docRawRequest(t, web, session, "huge.log")
	if rec.Code != http.StatusOK || rec.Body.Len() != docFileMaxBytes {
		t.Fatalf("status=%d served=%d, want 200 with the %d-byte head", rec.Code, rec.Body.Len(), docFileMaxBytes)
	}
	if got := rec.Header().Get("ETag"); got != "" {
		t.Fatalf("ETag=%q, want none past the hash limit", got)
	}
	if got := rec.Header().Get("X-Doc-Total-Size"); got != strconv.Itoa(docRevisionMaxBytes+1) {
		t.Fatalf("X-Doc-Total-Size=%q, want %d", got, docRevisionMaxBytes+1)
	}
	if rec.Header().Get("X-Doc-Modified-At") == "" {
		t.Fatal("X-Doc-Modified-At missing; the time is known at any size")
	}
	// "*" matches any current version, with or without a revision.
	if star := docRawRequestIfNoneMatch(t, web, session, "huge.log", "*"); star.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match * on an unhashed file: status=%d, want 304", star.Code)
	}
	if other := docRawRequestIfNoneMatch(t, web, session, "huge.log", `"other"`); other.Code != http.StatusOK {
		t.Fatalf("If-None-Match naming another tag on an unhashed file: status=%d, want 200", other.Code)
	}
}

// A modification time at or before the Unix epoch is not sent: the client
// reads a missing or non-positive time as no information, and the two agree.
func TestDocFile_Raw_NoTimeAtOrBeforeTheEpoch(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	writeDocAt(t, filepath.Join(cwd, "old.txt"), []byte("old"), time.UnixMilli(-1000))

	rec := docRawRequest(t, web, session, "old.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Doc-Modified-At"); got != "" {
		t.Fatalf("X-Doc-Modified-At=%q, want none for a pre-epoch time", got)
	}
}

// A file of exactly docRevisionMaxBytes is still hashed: the limit is inclusive.
func TestDocFile_Raw_RevisionAtTheHashLimit(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	writeSparseDoc(t, filepath.Join(cwd, "limit.log"), docRevisionMaxBytes)

	rec := docRawRequest(t, web, session, "limit.log")
	if got, want := rec.Header().Get("ETag"), `"`+docRevisionOf(make([]byte, docRevisionMaxBytes))+`"`; got != want {
		t.Fatalf("ETag=%q, want %q at exactly the limit", got, want)
	}
}

// A file that grows past the hash limit while it is read has no revision, and
// its size is at least what the read found, never the smaller size the stat
// saw before the open. The stat is stubbed to report the file as it was a
// moment earlier, which is the order a concurrent writer produces.
func TestReadDocFile_GrowingPastTheHashLimitReportsWhatWasRead(t *testing.T) {
	path := filepath.Join(docTestRoot(t), "growing.log")
	writeSparseDoc(t, path, docRevisionMaxBytes+100)
	small := filepath.Join(t.TempDir(), "earlier.log")
	writeDocAt(t, small, []byte("short"), time.UnixMilli(1_790_000_000_000))
	oldStat := docStat
	t.Cleanup(func() { docStat = oldStat })
	docStat = func(*os.File) (os.FileInfo, error) { return os.Stat(small) }

	read, err := readDocFile(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Revision != "" {
		t.Fatalf("Revision=%q, want none for a file that grew past the limit", read.Revision)
	}
	if read.TotalSize <= docRevisionMaxBytes {
		t.Fatalf("TotalSize=%d, want more than the %d bytes the read found", read.TotalSize, docRevisionMaxBytes)
	}
}

// A file that shrank below the hash limit after the stat is hashed, and its
// size is what the read found: the stat's larger size never decides the
// revision.
func TestReadDocFile_ShrinkingBelowTheHashLimitIsHashed(t *testing.T) {
	content := []byte("short now")
	path := filepath.Join(docTestRoot(t), "shrunk.log")
	writeDocAt(t, path, content, time.UnixMilli(1_790_000_000_000))
	big := filepath.Join(t.TempDir(), "earlier.log")
	writeSparseDoc(t, big, docRevisionMaxBytes+100)
	oldStat := docStat
	t.Cleanup(func() { docStat = oldStat })
	docStat = func(*os.File) (os.FileInfo, error) { return os.Stat(big) }

	read, err := readDocFile(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Revision != docRevisionOf(content) || read.TotalSize != int64(len(content)) {
		t.Fatalf("Revision/TotalSize = %q/%d, want %q/%d from the bytes read", read.Revision, read.TotalSize, docRevisionOf(content), len(content))
	}
}

// An empty file is a document with nothing in it, not a missing one.
func TestDocFile_Raw_EmptyFileIsServedEmpty(t *testing.T) {
	web, cwd, session := docServeTestServer(t)
	writeDocAt(t, filepath.Join(cwd, "empty.md"), nil, time.UnixMilli(1_790_000_000_000))

	rec := docRawRequest(t, web, session, "empty.md")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q, want 200 and empty", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("ETag"), `"`+docRevisionOf(nil)+`"`; got != want {
		t.Fatalf("ETag=%q, want %q", got, want)
	}
}
