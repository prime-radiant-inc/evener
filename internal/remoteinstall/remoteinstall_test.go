package remoteinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestEmbeddedScriptMatchesReviewedInstallers(t *testing.T) {
	want, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatalf("read the repository's install.sh: %v", err)
	}
	if !bytes.Equal(Script, want) {
		t.Fatalf("the embedded installer (%d bytes) differs from the repository's install.sh (%d bytes); the fix is to re-copy the reviewed script into internal/remoteinstall/install.sh",
			len(Script), len(want))
	}
}

func TestCommandChecksTheEmbeddedScriptSize(t *testing.T) {
	command := Command("snapshot", "/tmp/evener;do-not-run", "", "")
	if !bytes.Contains([]byte(command), []byte("[ \"$v\" = "+strconv.Itoa(len(Script)))) {
		t.Fatalf("command does not check the embedded script size: %q", command)
	}
	if !bytes.Contains([]byte(command), []byte("PREFIX='/tmp/evener;do-not-run'")) {
		t.Fatalf("command does not quote PREFIX: %q", command)
	}
}

// TestCommandPinsUnsetPathVariablesToEmpty pins the property the hub earned in
// round twelve, now shared with the CLI path: install.sh honors inherited
// PREFIX/BINDIR/EVENER_SHARE_BINDIR, and a remote login shell can export any of
// them, so an omitted flag must pin its variable to empty — where install.sh
// computes the documented default — rather than leave it to whatever the
// session environment carries.
func TestCommandPinsUnsetPathVariablesToEmpty(t *testing.T) {
	got := Command("snapshot", "", "", "")
	for _, want := range []string{"PREFIX=''", "BINDIR=''", "EVENER_SHARE_BINDIR=''"} {
		if !strings.Contains(got, want) {
			t.Fatalf("an omitted path flag can be overridden by the remote environment (%s missing): %q", want, got)
		}
	}
}

// TestScriptRefusesRelativeInstallPaths pins install.sh's own contract: a
// relative PREFIX, BINDIR, or EVENER_SHARE_BINDIR resolves against the
// caller's working directory, and the symlink the script writes resolves its
// target against the symlink's own directory, so a relative value yields a
// broken install rather than a movable one. The refusal must come before
// anything is downloaded or created — the run provides no curl at all, so a
// guard that fires late would surface as a download error instead of the
// contract message.
func TestScriptRefusesRelativeInstallPaths(t *testing.T) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64", "darwin/arm64":
	default:
		t.Skipf("install.sh ships no release archive for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	for _, kv := range [][2]string{
		{"PREFIX", "rel"},
		{"BINDIR", "rel/bin"},
		{"EVENER_SHARE_BINDIR", "rel/share/evener/bin"},
	} {
		t.Run(kv[0], func(t *testing.T) {
			runDir := t.TempDir()
			home := t.TempDir()
			scriptPath := filepath.Join(runDir, "install.sh")
			if err := os.WriteFile(scriptPath, Script, 0o644); err != nil {
				t.Fatalf("stage the embedded installer: %v", err)
			}
			cmd := exec.Command("sh", scriptPath)
			cmd.Dir = runDir
			cmd.Env = []string{
				"HOME=" + home,
				"TMPDIR=" + runDir,
				// No tools on PATH at all: the refusal must come from the
				// script's own guard, not from a failed download or probe.
				"PATH=" + runDir,
				kv[0] + "=" + kv[1],
			}
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("install.sh accepted a relative %s:\n%s", kv[0], out)
			}
			if !strings.Contains(string(out), "must be absolute") {
				t.Fatalf("install.sh does not refuse a relative %s with the contract error:\n%s", kv[0], out)
			}
		})
	}
}

// TestScriptInstallsTheReleaseFromAnyWhitespaceChecksumLine runs the embedded
// installer end to end against a scripted download boundary: curl is faked
// (the only network seam), while sh, tar, install, and the checksum tool are
// the machine's real ones. install.sh's checksum grep accepts any [[:space:]]
// separator, so a release whose checksums.txt separates hash and name with a
// tab is as valid as the space-separated kind sha256sum writes; the hash
// extraction must read the separator the same way, or verification fails on a
// valid release and the install refuses to happen.
func TestScriptInstallsTheReleaseFromAnyWhitespaceChecksumLine(t *testing.T) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64", "darwin/arm64":
		// install.sh ships release archives for exactly these; the harness
		// serves the archive the script computes for its own platform.
	default:
		t.Skipf("install.sh ships no release archive for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	for _, tool := range []string{"sh", "tar", "install", "mktemp", "uname"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH; the installer needs it", tool)
		}
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		if _, err := exec.LookPath("shasum"); err != nil {
			t.Skip("neither sha256sum nor shasum is on PATH; install.sh refuses to verify without one")
		}
	}

	// The release: a real tar.gz holding the archive root install.sh expects.
	// It also carries evener-dev, as a legacy archive might, so the run below
	// proves install.sh skips the dev tooling binary rather than installing it.
	var archive bytes.Buffer
	rootName := fmt.Sprintf("evener_%s_%s", runtime.GOOS, runtime.GOARCH)
	archiveName := rootName + ".tar.gz"
	bins := map[string]string{
		"evener":     "#!/bin/sh\necho evener binary\n",
		"evener-dev": "#!/bin/sh\necho evener-dev binary\n",
	}
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"evener", "evener-dev"} {
		body := bins[name]
		hdr := &tar.Header{Name: path.Join(rootName, name), Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	archiveBytes := archive.Bytes()
	sum := sha256.Sum256(archiveBytes)

	for _, tc := range []struct{ name, sep string }{
		{"space-separated", " "},
		{"tab-separated", "\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			home := t.TempDir()
			archivePath := filepath.Join(runDir, archiveName)
			if err := os.WriteFile(archivePath, archiveBytes, 0o644); err != nil {
				t.Fatalf("stage the release archive: %v", err)
			}
			checksumsPath := filepath.Join(runDir, "checksums.txt")
			checksumLine := hex.EncodeToString(sum[:]) + tc.sep + archiveName + "\n"
			if err := os.WriteFile(checksumsPath, []byte(checksumLine), 0o644); err != nil {
				t.Fatalf("stage checksums.txt: %v", err)
			}

			// The download boundary: the only fake in the run. It serves the
			// staged files to whatever release URL install.sh computes.
			curlShim := `#!/bin/sh
# install.sh test double: serves the staged release files to the download URLs.
set -eu
url=
out=
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out=$2; shift; shift ;;
	-w) shift; shift ;;
	-*) shift ;;
	*) if [ -z "$url" ]; then url=$1; fi; shift ;;
	esac
done
case "$url" in
*checksums.txt) cat "$EVENER_TEST_CHECKSUMS" ;;
*) cat "$EVENER_TEST_ARCHIVE" ;;
esac > "$out"
printf '200\n'
`
			fakeBin := t.TempDir()
			if err := os.WriteFile(filepath.Join(fakeBin, "curl"), []byte(curlShim), 0o755); err != nil {
				t.Fatalf("stage the fake curl: %v", err)
			}

			scriptPath := filepath.Join(runDir, "install.sh")
			if err := os.WriteFile(scriptPath, Script, 0o644); err != nil {
				t.Fatalf("stage the embedded installer: %v", err)
			}

			cmd := exec.Command("sh", scriptPath)
			cmd.Dir = runDir
			// The child runs with a minimal controlled environment: only PATH is
			// inherited, so the real tools the LookPath probes found resolve the
			// same way. Everything else is set deliberately — including a hostile
			// TAR_OPTIONS, because the login shells of real remote hosts export
			// such overrides and the extraction must not obey one.
			cmd.Env = []string{
				"HOME=" + home,
				"TMPDIR=" + runDir,
				"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"EVENER_TEST_ARCHIVE=" + archivePath,
				"EVENER_TEST_CHECKSUMS=" + checksumsPath,
				"TAR_OPTIONS=--strip-components=1",
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("install.sh: %v\n%s", err, out)
			}

			shareBin := filepath.Join(home, ".local", "share", "evener", "bin")
			binDir := filepath.Join(home, ".local", "bin")
			for _, dir := range []string{shareBin, binDir} {
				if _, err := os.Lstat(filepath.Join(dir, "evener-dev")); err == nil {
					t.Fatalf("install.sh installed evener-dev into %s; it is dev tooling, not part of an install\n%s", dir, out)
				} else if !os.IsNotExist(err) {
					t.Fatalf("lstat %s: %v", filepath.Join(dir, "evener-dev"), err)
				}
			}
			for _, name := range []string{"evener"} {
				installed := filepath.Join(shareBin, name)
				got, err := os.ReadFile(installed)
				if err != nil {
					t.Fatalf("install.sh did not install %s: %v\n%s", name, err, out)
				}
				if string(got) != bins[name] {
					t.Fatalf("%s = %q, want the release's %q", name, got, bins[name])
				}
				if fi, err := os.Stat(installed); err != nil || fi.Mode().Perm() != 0o755 {
					t.Fatalf("%s was not installed executable (0755): %v", name, err)
				}
				link, err := os.Readlink(filepath.Join(binDir, name))
				if err != nil {
					t.Fatalf("install.sh did not symlink %s into the bin dir: %v\n%s", name, err, out)
				}
				if link != installed {
					t.Fatalf("%s symlink -> %s, want %s", name, link, installed)
				}
			}
			if !strings.Contains(string(out), "Installed Evener binaries to") {
				t.Fatalf("install.sh output is missing the completion line:\n%s", out)
			}
		})
	}
}

// TestScriptFailedPublishLeavesThePreviousBinary proves install.sh publishes by
// rename: a copy that fails after writing part of its payload must leave the
// previous complete binary and symlink in place, never a truncated live file
// (#2345). The fake install writes "partial" into its destination and exits 1;
// with an in-place install that destination is the live binary.
func TestScriptFailedPublishLeavesThePreviousBinary(t *testing.T) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64", "darwin/arm64":
		// install.sh ships release archives for exactly these; this harness
		// serves the archive the script computes for its own platform.
	default:
		t.Skipf("install.sh ships no release archive for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	for _, tool := range []string{"sh", "tar", "install", "mktemp", "uname"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH; the installer needs it", tool)
		}
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		if _, err := exec.LookPath("shasum"); err != nil {
			t.Skip("neither sha256sum nor shasum is on PATH; install.sh refuses to verify without one")
		}
	}
	realInstall, err := exec.LookPath("install")
	if err != nil {
		t.Skipf("install is not on PATH: %v", err)
	}

	var archive bytes.Buffer
	rootName := fmt.Sprintf("evener_%s_%s", runtime.GOOS, runtime.GOARCH)
	archiveName := rootName + ".tar.gz"
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\necho release binary\n"
	hdr := &tar.Header{Name: path.Join(rootName, "evener"), Mode: 0o755, Size: int64(len(body))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	sum := sha256.Sum256(archive.Bytes())

	runDir := t.TempDir()
	home := t.TempDir()
	archivePath := filepath.Join(runDir, archiveName)
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o644); err != nil {
		t.Fatalf("stage the release archive: %v", err)
	}
	checksumsPath := filepath.Join(runDir, "checksums.txt")
	if err := os.WriteFile(checksumsPath, []byte(hex.EncodeToString(sum[:])+"  "+archiveName+"\n"), 0o644); err != nil {
		t.Fatalf("stage checksums.txt: %v", err)
	}

	shareBin := filepath.Join(home, ".local", "share", "evener", "bin")
	binDir := filepath.Join(home, ".local", "bin")
	for _, dir := range []string{shareBin, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	old := []byte("#!/bin/sh\necho old binary\n")
	installed := filepath.Join(shareBin, "evener")
	if err := os.WriteFile(installed, old, 0o755); err != nil {
		t.Fatalf("stage the previous binary: %v", err)
	}
	if err := os.Symlink(installed, filepath.Join(binDir, "evener")); err != nil {
		t.Fatalf("stage the previous symlink: %v", err)
	}

	fakeBin := t.TempDir()
	curlShim := `#!/bin/sh
set -eu
url=
out=
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out=$2; shift; shift ;;
	-w) shift; shift ;;
	-*) shift ;;
	*) if [ -z "$url" ]; then url=$1; fi; shift ;;
	esac
done
case "$url" in
*checksums.txt) cat "$EVENER_TEST_CHECKSUMS" ;;
*) cat "$EVENER_TEST_ARCHIVE" ;;
esac > "$out"
printf '200\n'
`
	installShim := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1" = "-d" ]; then
	exec %q "$@"
fi
for last; do :; done
printf 'partial' > "$last"
exit 1
`, realInstall)
	for name, body := range map[string]string{"curl": curlShim, "install": installShim} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(body), 0o755); err != nil {
			t.Fatalf("stage the fake %s: %v", name, err)
		}
	}

	scriptPath := filepath.Join(runDir, "install.sh")
	if err := os.WriteFile(scriptPath, Script, 0o644); err != nil {
		t.Fatalf("stage the embedded installer: %v", err)
	}
	cmd := exec.Command("sh", scriptPath)
	cmd.Dir = runDir
	cmd.Env = []string{
		"HOME=" + home,
		"TMPDIR=" + runDir,
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"EVENER_TEST_ARCHIVE=" + archivePath,
		"EVENER_TEST_CHECKSUMS=" + checksumsPath,
		"TAR_OPTIONS=--strip-components=1",
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("install.sh succeeded despite the failed publish:\n%s", out)
	}

	got, rerr := os.ReadFile(installed)
	if rerr != nil || !bytes.Equal(got, old) {
		t.Fatalf("the failed publish replaced the live binary with %q (err %v):\n%s", got, rerr, out)
	}
	if link, lerr := os.Readlink(filepath.Join(binDir, "evener")); lerr != nil || link != installed {
		t.Fatalf("the failed publish changed the binary symlink to %q (err %v):\n%s", link, lerr, out)
	}
	for _, dir := range []string{shareBin, binDir} {
		stages, gerr := filepath.Glob(filepath.Join(dir, ".evener.install.*"))
		if gerr != nil {
			t.Fatalf("glob staging files in %s: %v", dir, gerr)
		}
		if len(stages) != 0 {
			t.Fatalf("install.sh left staging files behind: %v", stages)
		}
	}
}
