package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTargetUnknownTarget(t *testing.T) {
	if _, err := ResolveTarget("banana", "release"); err == nil {
		t.Fatal("expected error for unknown target")
	}
	// A v-prefixed tag is accepted as a release.
	got, err := ResolveTarget("v9.9.9", "")
	if err != nil || got.Release != "v9.9.9" || got.Channel != "release" {
		t.Fatalf("ResolveTarget(v9.9.9)=%+v err=%v", got, err)
	}
}

func TestReleaseAssetPlatforms(t *testing.T) {
	asset, root, err := releaseAsset("darwin", "arm64")
	if err != nil || asset != "serf_darwin_arm64.tar.gz" || root != "serf_darwin_arm64" {
		t.Fatalf("darwin-arm64 asset=%q root=%q err=%v", asset, root, err)
	}
	asset, root, err = releaseAsset("linux", "amd64")
	if err != nil || asset != "serf_linux_amd64.tar.gz" {
		t.Fatalf("linux-amd64 asset=%q root=%q err=%v", asset, root, err)
	}
	if _, _, err := releaseAsset("windows", "amd64"); err == nil {
		t.Fatal("expected error for unsupported platform")
	}
}

func TestInstallPrefix(t *testing.T) {
	if got, err := installPrefix("/opt/serf"); err != nil || got != "/opt/serf" {
		t.Fatalf("explicit prefix=%q err=%v", got, err)
	}
	t.Setenv("HOME", "/home/tester")
	if got, err := installPrefix(""); err != nil || got != "/home/tester/.local" {
		t.Fatalf("default prefix=%q err=%v", got, err)
	}
	// No HOME → error. Unset both HOME and (defensively) any fallback.
	t.Setenv("HOME", "")
	if _, err := installPrefix(""); err == nil {
		t.Fatal("expected error when HOME is unset")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x"); got != "x" {
		t.Fatalf("firstNonEmpty=%q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("firstNonEmpty all-empty=%q", got)
	}
}

func TestDownloadErrors(t *testing.T) {
	// Non-2xx status is surfaced as an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	dest := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := download(context.Background(), server.Client(), server.URL, dest); err == nil {
		t.Fatal("expected error for HTTP 404")
	}

	// A malformed URL fails at request construction.
	if err := download(context.Background(), http.DefaultClient, "://bad-url", dest); err == nil {
		t.Fatal("expected error for malformed URL")
	}

	// O_EXCL: writing to an existing dest fails.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(ok.Close)
	existing := filepath.Join(t.TempDir(), "exists")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := download(context.Background(), ok.Client(), ok.URL, existing); err == nil {
		t.Fatal("expected error writing to an existing destination")
	}
}

func TestDownloadSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello payload"))
	}))
	t.Cleanup(server.Close)
	dest := filepath.Join(t.TempDir(), "out")
	if err := download(context.Background(), server.Client(), server.URL, dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "hello payload" {
		t.Fatalf("downloaded=%q err=%v", data, err)
	}
}

// tarGz builds a gzip-compressed tar from the given entries (name → body).
// Entries with a nil body are written as a directory to exercise the
// non-regular-file rejection path.
func tarGz(t *testing.T, entries map[string][]byte, dirs map[string]bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if dirs[name] {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
			body = nil
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("hdr: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gz: %v", err)
	}
	return buf.Bytes()
}

func TestExtractReleaseArchiveMissingBinary(t *testing.T) {
	root := "serf_linux_amd64"
	// Only the first binary present → extraction must report the first missing one.
	entries := map[string][]byte{path.Join(root, installBinaries[0]): []byte("x")}
	archivePath := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(archivePath, tarGz(t, entries, nil), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := extractReleaseArchive(archivePath, root, filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "did not contain") {
		t.Fatalf("err=%v, want missing-binary error", err)
	}
}

func TestExtractReleaseArchiveRejectsNonRegularFile(t *testing.T) {
	root := "serf_linux_amd64"
	entries := map[string][]byte{}
	dirs := map[string]bool{}
	for _, bin := range installBinaries {
		entries[path.Join(root, bin)] = []byte("x")
	}
	// Turn the first expected entry into a directory entry.
	dirName := path.Join(root, installBinaries[0])
	dirs[dirName] = true

	archivePath := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(archivePath, tarGz(t, entries, dirs), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := extractReleaseArchive(archivePath, root, filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err=%v, want non-regular-file error", err)
	}
}

func TestExtractReleaseArchiveBadGzip(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := os.WriteFile(archivePath, []byte("not gzip"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := extractReleaseArchive(archivePath, "root", filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected gzip error")
	}
	// A missing archive file surfaces an open error.
	if err := extractReleaseArchive(filepath.Join(t.TempDir(), "nope"), "root", t.TempDir()); err == nil {
		t.Fatal("expected open error for missing archive")
	}
}

func TestCopyExecutableMissingSource(t *testing.T) {
	err := copyExecutable(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "dst"))
	if err == nil {
		t.Fatal("expected error copying a missing source")
	}
}

func TestInstallExtractedBinariesMissingSource(t *testing.T) {
	// extractDir has no binaries, so copyExecutable fails on the first one.
	err := installExtractedBinaries(t.TempDir(), filepath.Join(t.TempDir(), "share"), filepath.Join(t.TempDir(), "bin"))
	if err == nil {
		t.Fatal("expected error when extracted binaries are absent")
	}
}

func TestUpgradeDownloadFailurePropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	_, err := Upgrade(context.Background(), Options{
		CurrentChannel: "snapshot",
		Prefix:         t.TempDir(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		RepoURL:        server.URL,
	})
	if err == nil {
		t.Fatal("expected Upgrade to fail when the download 500s")
	}
}

func TestUpgradeStageFailures(t *testing.T) {
	t.Run("target", func(t *testing.T) {
		if _, err := Upgrade(t.Context(), Options{Requested: "bad"}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("prefix", func(t *testing.T) {
		t.Setenv("HOME", "")
		if _, err := Upgrade(t.Context(), Options{GOOS: "linux", GOARCH: "amd64"}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("temp", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", file)
		if _, err := Upgrade(t.Context(), Options{Prefix: t.TempDir(), GOOS: "linux", GOARCH: "amd64"}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("extract", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("bad gzip")) }))
		defer server.Close()
		if _, err := Upgrade(t.Context(), Options{Prefix: t.TempDir(), GOOS: "linux", GOARCH: "amd64", RepoURL: server.URL}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("install", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(releaseArchive(t, "serf_linux_amd64")) }))
		defer server.Close()
		block := filepath.Join(t.TempDir(), "block")
		if err := os.WriteFile(block, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Upgrade(t.Context(), Options{Prefix: t.TempDir(), ShareBinDir: filepath.Join(block, "share"), GOOS: "linux", GOARCH: "amd64", RepoURL: server.URL}); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestExtractArchiveErrors(t *testing.T) {
	root := "root"
	t.Run("mkdir", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "a")
		if err := os.WriteFile(archive, tarGz(t, nil, nil), 0o600); err != nil {
			t.Fatal(err)
		}
		block := filepath.Join(t.TempDir(), "block")
		if err := os.WriteFile(block, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := extractReleaseArchive(archive, root, filepath.Join(block, "out")); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("truncated tar", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte("not a tar stream"))
		_ = gz.Close()
		archive := filepath.Join(t.TempDir(), "a")
		_ = os.WriteFile(archive, buf.Bytes(), 0o600)
		if err := extractReleaseArchive(archive, root, t.TempDir()); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("existing output", func(t *testing.T) {
		entries := map[string][]byte{path.Join(root, installBinaries[0]): []byte("x")}
		archive := filepath.Join(t.TempDir(), "a")
		_ = os.WriteFile(archive, tarGz(t, entries, nil), 0o600)
		out := t.TempDir()
		_ = os.WriteFile(filepath.Join(out, installBinaries[0]), nil, 0o600)
		if err := extractReleaseArchive(archive, root, out); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("ignores unrelated entry", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "a")
		_ = os.WriteFile(archive, tarGz(t, map[string][]byte{"unrelated/readme": []byte("x")}, nil), 0o600)
		if err := extractReleaseArchive(archive, root, t.TempDir()); err == nil || !strings.Contains(err.Error(), "did not contain") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestInstallDirectoryAndSymlinkErrors(t *testing.T) {
	extract := t.TempDir()
	for _, bin := range installBinaries {
		_ = os.WriteFile(filepath.Join(extract, bin), []byte(bin), 0o755)
	}
	block := filepath.Join(t.TempDir(), "block")
	_ = os.WriteFile(block, nil, 0o600)
	if err := installExtractedBinaries(extract, filepath.Join(block, "share"), t.TempDir()); err == nil {
		t.Fatal("share mkdir")
	}
	if err := installExtractedBinaries(extract, t.TempDir(), filepath.Join(block, "bin")); err == nil {
		t.Fatal("bin mkdir")
	}
	binDir := t.TempDir()
	_ = os.Mkdir(filepath.Join(binDir, installBinaries[0]), 0o755)
	_ = os.WriteFile(filepath.Join(binDir, installBinaries[0], "keep"), nil, 0o600)
	if err := installExtractedBinaries(extract, t.TempDir(), binDir); err == nil {
		t.Fatal("symlink")
	}
}

func FuzzResolveTarget(f *testing.F) {
	for _, s := range []string{"", "current", "snapshot", "release", "latest", "v1", "bad"} {
		f.Add(s, "snapshot")
	}
	f.Fuzz(func(t *testing.T, requested, current string) {
		got, err := ResolveTarget(requested, current)
		if err == nil && (got.Release == "" || got.Channel == "") {
			t.Fatalf("empty target: %+v", got)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestDownloadTransportAndIOErrors(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}
	if err := download(t.Context(), client, "http://local.invalid", dest); err == nil {
		t.Fatal("transport")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("body")) }))
	defer server.Close()
	oldCopy, oldClose := copyStream, closeFile
	t.Cleanup(func() { copyStream, closeFile = oldCopy, oldClose })
	copyStream = func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("copy") }
	closeFile = func(*os.File) error { return errors.New("close") }
	if err := download(t.Context(), server.Client(), server.URL, dest); err == nil || !strings.Contains(err.Error(), "copy") || !strings.Contains(err.Error(), "close") {
		t.Fatalf("joined err=%v", err)
	}
	closeFile = oldClose
	if err := download(t.Context(), server.Client(), server.URL, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("copy")
	}
	copyStream = oldCopy
	closeFile = func(*os.File) error { return errors.New("close") }
	if err := download(t.Context(), server.Client(), server.URL, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("close")
	}
}

func TestExtractCopyAndCloseErrors(t *testing.T) {
	root := "root"
	entries := map[string][]byte{}
	for _, bin := range installBinaries {
		entries[path.Join(root, bin)] = []byte(bin)
	}
	entries["unrelated/readme.txt"] = []byte("ignored")
	archive := filepath.Join(t.TempDir(), "a")
	_ = os.WriteFile(archive, tarGz(t, entries, nil), 0o600)
	oldCopy, oldClose := copyStream, closeFile
	t.Cleanup(func() { copyStream, closeFile = oldCopy, oldClose })
	copyStream = func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("copy") }
	if err := extractReleaseArchive(archive, root, t.TempDir()); err == nil {
		t.Fatal("copy")
	}
	copyStream = oldCopy
	closeFile = func(*os.File) error { return errors.New("close") }
	if err := extractReleaseArchive(archive, root, t.TempDir()); err == nil {
		t.Fatal("close")
	}
}

func TestCopyExecutableIOErrors(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	_ = os.WriteFile(src, []byte("body"), 0o755)
	oldCopy, oldClose, oldRename := copyStream, closeFile, renameFile
	t.Cleanup(func() { copyStream, closeFile, renameFile = oldCopy, oldClose, oldRename })
	copyStream = func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("copy") }
	if err := copyExecutable(src, filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("copy")
	}
	copyStream = oldCopy
	closeFile = func(*os.File) error { return errors.New("close") }
	if err := copyExecutable(src, filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("close")
	}
	closeFile = oldClose
	renameFile = func(string, string) error { return errors.New("rename") }
	if err := copyExecutable(src, filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("rename")
	}
	if err := copyExecutable(src, filepath.Join(t.TempDir(), "missing", "dst")); err == nil {
		t.Fatal("open output")
	}
}
