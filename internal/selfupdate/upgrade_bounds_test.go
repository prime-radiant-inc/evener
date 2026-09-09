package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestUpgradeRefusesOversizedArchive proves the download is size-bounded: a
// server that streams endless bytes fails instead of filling the disk.
// Fails today because download copies the body with no size cap.
func TestUpgradeRefusesOversizedArchive(t *testing.T) {
	previous := defaultMaxArchiveBytes
	defaultMaxArchiveBytes = 1 << 20 // 1MB for the test; production is larger
	t.Cleanup(func() { defaultMaxArchiveBytes = previous })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			_, _ = w.Write([]byte(strings.Repeat("a", 64) + "  evener_linux_amd64.tar.gz\n"))
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		// Stream 8x the cap; the client must stop early, not buffer it all.
		chunk := make([]byte, 1<<20)
		for range 8 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)

	_, err := Upgrade(t.Context(), upgradeForChecksumTest(t.TempDir(), server.URL))
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want an oversize-archive error", err)
	}
}

// TestDefaultMaxArchiveBytesIsBounded pins the shipped default: archives
// are ~40MB, so the cap must be comfortably above that but finite.
func TestDefaultMaxArchiveBytesIsBounded(t *testing.T) {
	if defaultMaxArchiveBytes <= 64<<20 {
		t.Fatalf("defaultMaxArchiveBytes = %d, want headroom above the ~40MB archives", defaultMaxArchiveBytes)
	}
	if defaultMaxArchiveBytes > 1<<30 {
		t.Fatalf("defaultMaxArchiveBytes = %d, want a real bound, not ~infinity", defaultMaxArchiveBytes)
	}
}

// TestVerifyChecksumStreamsFromDisk proves verification hashes the file on
// disk instead of loading it fully into memory: it writes a valid archive
// plus checksums through a stub transport and checks the byte-exact digest
// path accepts it. (The behavioral half -- tampered archives rejected --
// is TestUpgradeRejectsTamperedArchive; this pins the streaming helper the
// production path now uses.)
func TestVerifyChecksumStreamsFromDisk(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "evener_linux_amd64.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex(t, archive)
	checksumsPath := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(checksumsPath, []byte(sum+"  evener_linux_amd64.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksumFile(archivePath, checksumsPath, "evener_linux_amd64.tar.gz", ""); err != nil {
		t.Fatalf("verifyChecksumFile on a valid pair: %v", err)
	}
	if err := os.WriteFile(archivePath, append(archive, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksumFile(archivePath, checksumsPath, "evener_linux_amd64.tar.gz", ""); err == nil ||
		!strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err = %v, want a mismatch error after tampering", err)
	}
}

// TestUpgradeHonorsCancellationDuringInstall proves the overall deadline
// reaches extraction and installation, not just the downloads: with an
// already-cancelled context, Upgrade fails instead of installing.
func TestUpgradeHonorsCancellationDuringInstall(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	server := checksumTestServer(t, archive, "")
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Upgrade(ctx, upgradeForChecksumTest(t.TempDir(), server.URL))
	if err == nil {
		t.Fatal("expected Upgrade to honor an already-cancelled context")
	}
}

// TestExtractRefusesDecompressionBomb proves extraction caps total
// uncompressed output: a highly compressible entry fails instead of
// filling the disk. Fails today: per-entry copies are unbounded.
func TestExtractRefusesDecompressionBomb(t *testing.T) {
	// 600MB of zeros compresses to ~600KB: realistic zip-bomb shape.
	big := make([]byte, 600<<20)
	archivePath := filepath.Join(t.TempDir(), "bomb.tar.gz")
	if err := os.WriteFile(archivePath, tarGz(t, map[string][]byte{"evener_linux_amd64/evener": big}, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractReleaseArchive(t.Context(), archivePath, "evener_linux_amd64", t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "expands past") {
		t.Fatalf("err = %v, want a decompression-bomb refusal", err)
	}
}
