package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checksumTestServer serves a release archive plus its checksums.txt, with
// hooks to tamper with either. It returns the server and the correct hex
// digest of the archive it serves.
func checksumTestServer(t *testing.T, archive []byte, checksums string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(archive)
	correct := hex.EncodeToString(sum[:])
	if checksums == "" {
		checksums = correct + "  evener_linux_amd64.tar.gz\n"
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			_, _ = w.Write([]byte(checksums))
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func upgradeForChecksumTest(prefix, repoURL string) Options {
	return Options{
		Requested:      "snapshot",
		CurrentChannel: "snapshot",
		Prefix:         prefix,
		GOOS:           "linux",
		GOARCH:         "amd64",
		RepoURL:        repoURL,
	}
}

// TestUpgradeVerifiesArchiveChecksum proves a downloaded archive installs
// only when its SHA-256 matches the release's checksums.txt entry: the
// happy path installs, and the installed bytes are the served archive.
func TestUpgradeVerifiesArchiveChecksum(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	server := checksumTestServer(t, archive, "")
	t.Cleanup(server.Close)

	prefix := filepath.Join(t.TempDir(), ".local")
	if _, err := Upgrade(t.Context(), upgradeForChecksumTest(prefix, server.URL)); err != nil {
		t.Fatalf("Upgrade with matching checksum: %v", err)
	}
	installed, err := os.ReadFile(filepath.Join(prefix, "share", "evener", "bin", "evener"))
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if !strings.Contains(string(installed), "archive evener") {
		t.Fatalf("installed binary has unexpected content %q", string(installed))
	}
}

// TestUpgradeRejectsTamperedArchive proves an archive whose bytes differ
// from the checksums.txt entry is refused BEFORE extraction or install:
// no binary lands in the prefix and the error names the mismatch.
func TestUpgradeRejectsTamperedArchive(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	sum := sha256.Sum256(archive)
	correct := hex.EncodeToString(sum[:])
	// checksums.txt vouches for different bytes than the server sends.
	server := checksumTestServer(t, append(archive, byte(0)), correct+"  evener_linux_amd64.tar.gz\n")
	t.Cleanup(server.Close)

	prefix := filepath.Join(t.TempDir(), ".local")
	_, err := Upgrade(t.Context(), upgradeForChecksumTest(prefix, server.URL))
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum mismatch error", err)
	}
	if _, statErr := os.Stat(filepath.Join(prefix, "share", "evener", "bin", "evener")); !os.IsNotExist(statErr) {
		t.Fatalf("tampered archive was installed: stat err = %v", statErr)
	}
}

// TestUpgradeRefusesMissingChecksumEntry proves fail-closed behavior when
// checksums.txt has no line for the archive: same guarantee install.sh
// makes ("refusing to install an unverified archive").
func TestUpgradeRefusesMissingChecksumEntry(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	server := checksumTestServer(t, archive, "deadbeef  some-other-file.tar.gz\n")
	t.Cleanup(server.Close)

	prefix := filepath.Join(t.TempDir(), ".local")
	_, err := Upgrade(t.Context(), upgradeForChecksumTest(prefix, server.URL))
	if !errors.Is(err, errChecksumMissing) {
		t.Fatalf("err = %v, want a missing-entry error", err)
	}
	if _, statErr := os.Stat(filepath.Join(prefix, "share", "evener", "bin")); !os.IsNotExist(statErr) {
		t.Fatalf("unverified archive was installed: stat err = %v", statErr)
	}
}

// TestUpgradeRefusesAmbiguousChecksumEntry proves two lines for the same
// archive also fail closed rather than picking one.
func TestUpgradeRefusesAmbiguousChecksumEntry(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	sum := sha256.Sum256(archive)
	correct := hex.EncodeToString(sum[:])
	dup := correct + "  evener_linux_amd64.tar.gz\n" + correct + "  evener_linux_amd64.tar.gz\n"
	server := checksumTestServer(t, archive, dup)
	t.Cleanup(server.Close)

	if _, err := Upgrade(t.Context(), upgradeForChecksumTest(t.TempDir(), server.URL)); !errors.Is(err, errChecksumAmbiguous) {
		t.Fatalf("err = %v, want an ambiguous-entry error", err)
	}
}

// TestParseChecksumEntry pins the accepted line format: 64 hex digits,
// whitespace, then the bare archive name, tolerating goreleaser's
// "dist/<name>" spelling the way install.sh does.
func TestParseChecksumEntry(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name  string
		line  string
		asset string
		want  string
	}{
		{"bare", sum + "  evener_linux_amd64.tar.gz", "evener_linux_amd64.tar.gz", sum},
		{"dist prefixed", sum + "  dist/evener_linux_amd64.tar.gz", "evener_linux_amd64.tar.gz", sum},
		{"tab separated", sum + "\tevener_linux_amd64.tar.gz", "evener_linux_amd64.tar.gz", sum},
		{"other archive", sum + "  evener_darwin_arm64.tar.gz", "evener_linux_amd64.tar.gz", ""},
		{"short hash", "abc  evener_linux_amd64.tar.gz", "evener_linux_amd64.tar.gz", ""},
		{"no filename", sum, "evener_linux_amd64.tar.gz", ""},
		{"non-hex sum", strings.Repeat("g", 64) + "  evener_linux_amd64.tar.gz", "evener_linux_amd64.tar.gz", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseChecksumEntry(tc.line, tc.asset); got != tc.want {
				t.Fatalf("parseChecksumEntry(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestVerifyChecksumFileRefusesUnusableEvidence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.tar.gz")
	if err := os.WriteFile(archive, []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := strings.Repeat("a", 64)
	if err := verifyChecksumFile(archive, filepath.Join(dir, "missing-checksums.txt"), "archive.tar.gz", ""); err == nil {
		t.Fatal("a missing checksums file was accepted")
	}

	noEntry := filepath.Join(dir, "no-entry.txt")
	if err := os.WriteFile(noEntry, []byte(strings.Repeat("b", 64)+"  other.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyChecksumFile(archive, noEntry, "archive.tar.gz", "")
	if !errors.Is(err, errChecksumMissing) {
		t.Fatalf("a checksums file without the asset = %v, want the missing sentinel", err)
	}

	ambiguous := filepath.Join(dir, "ambiguous.txt")
	line := sum + "  archive.tar.gz\n"
	if err := os.WriteFile(ambiguous, []byte(line+line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksumFile(archive, ambiguous, "archive.tar.gz", ""); !errors.Is(err, errChecksumAmbiguous) {
		t.Fatalf("two checksum entries for the asset = %v, want the ambiguous sentinel", err)
	}

	checksums := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(checksums, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksumFile(filepath.Join(dir, "missing-archive.tar.gz"), checksums, "archive.tar.gz", sum); err == nil {
		t.Fatal("a missing archive was accepted")
	}
}

func TestVerifyArchiveChecksumSurfacesTransportFailures(t *testing.T) {
	t.Parallel()
	archive := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(archive, []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchiveChecksum(t.Context(), http.DefaultClient, "http://spaced host/owner/repo", "v1", "archive.tar.gz", archive); err == nil {
		t.Fatal("a checksums URL that cannot be requested succeeded")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if err := verifyArchiveChecksum(t.Context(), http.DefaultClient, closed.URL+"/owner/repo", "v1", "archive.tar.gz", archive); err == nil {
		t.Fatal("a checksums download from a dead server succeeded")
	}
	if err := verifyArchiveChecksum(t.Context(), &http.Client{Transport: failingTransport{}}, "http://updates.example/owner/repo", "v1", "archive.tar.gz", archive); err == nil {
		t.Fatal("a checksums body that cannot be read succeeded")
	}
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	if err := verifyArchiveChecksum(t.Context(), http.DefaultClient, server.URL+"/owner/repo", "v1", "archive.tar.gz", archive); err == nil {
		t.Fatal("a checksums download that 404s succeeded")
	}
}
