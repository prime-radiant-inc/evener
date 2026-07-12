//go:build serffuzz

package serf_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzInstallScript drives the release installer through its environment,
// platform, archive-validation, and filesystem branches. Network and platform
// discovery are replaced at the process boundary; installation and symlink
// creation still execute through the real install.sh implementation.
func FuzzInstallScript(f *testing.F) {
	for _, seed := range [][]byte{
		{0, 0, 0, 0, 0}, // Linux/amd64, latest, complete archive, HOME.
		{1, 1, 1, 0, 1}, // Darwin/arm64, versioned, complete archive, PREFIX.
		{2, 0, 0, 0, 0}, // unsupported OS.
		{0, 2, 0, 0, 0}, // unsupported architecture.
		{0, 1, 0, 0, 0}, // supported platform, unavailable release pair.
		{0, 0, 0, 1, 0}, // missing archive root.
		{0, 0, 0, 2, 0}, // missing binary.
		{0, 0, 0, 0, 2}, // neither HOME nor PREFIX.
		{0, 0, 0, 0, 3}, // explicit bindir overrides.
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 4096 {
			t.Skip()
		}
		var choice [5]byte
		copy(choice[:], program)

		osNames := []string{"Linux", "Darwin", "Plan9"}
		archNames := []string{"x86_64", "arm64", "riscv64"}
		osName := osNames[int(choice[0])%len(osNames)]
		archName := archNames[int(choice[1])%len(archNames)]
		versioned := choice[2]&1 != 0
		archiveMode := int(choice[3] % 3)
		envMode := int(choice[4] % 4)

		repoRoot, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		fakeBin := filepath.Join(root, "fake-bin")
		if err := os.Mkdir(fakeBin, 0o755); err != nil {
			t.Fatal(err)
		}

		writeExecutable(t, filepath.Join(fakeBin, "uname"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  -s) printf '%%s\n' %q ;;
  -m) printf '%%s\n' %q ;;
  *) exit 2 ;;
esac
`, osName, archName))
		writeExecutable(t, filepath.Join(fakeBin, "curl"), `#!/bin/sh
printf '%s\n' "$2" > "$SERF_FUZZ_URL_FILE"
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then : > "$2"; exit 0; fi
  shift
done
exit 2
`)
		writeExecutable(t, filepath.Join(fakeBin, "tar"), `#!/bin/sh
dest=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -C ]; then dest=$2; break; fi
  shift
done
[ -n "$dest" ] || exit 2
[ "$SERF_FUZZ_ARCHIVE_MODE" != 1 ] || exit 0
mkdir -p "$dest/$SERF_FUZZ_ARCHIVE_ROOT"
for bin in serf serf-hub serf-tui serf-doctor; do
  [ "$SERF_FUZZ_ARCHIVE_MODE:$bin" = 2:serf-doctor ] && continue
  printf '#!/bin/sh\nexit 0\n' > "$dest/$SERF_FUZZ_ARCHIVE_ROOT/$bin"
done
`)

		home := filepath.Join(root, "home")
		prefix := filepath.Join(root, "prefix")
		urlFile := filepath.Join(root, "url")
		env := overlayEnv(os.Environ(), map[string]string{
			"PATH":                   fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"HOME":                   home,
			"PREFIX":                 "",
			"BINDIR":                 "",
			"SERF_SHARE_BINDIR":      "",
			"SERF_INSTALL_VERSION":   "",
			"SERF_FUZZ_URL_FILE":     urlFile,
			"SERF_FUZZ_ARCHIVE_MODE": fmt.Sprint(archiveMode),
			"SERF_FUZZ_ARCHIVE_ROOT": installArchiveRoot(osName, archName),
		})
		if versioned {
			env = overlayEnv(env, map[string]string{"SERF_INSTALL_VERSION": "v1.2.3"})
		}
		switch envMode {
		case 1:
			env = overlayEnv(env, map[string]string{"PREFIX": prefix})
		case 2:
			env = overlayEnv(env, map[string]string{"HOME": "", "PREFIX": ""})
		case 3:
			env = overlayEnv(env, map[string]string{
				"BINDIR":            filepath.Join(root, "commands"),
				"SERF_SHARE_BINDIR": filepath.Join(root, "payload"),
			})
		}

		var output bytes.Buffer
		cmd := exec.Command("sh", filepath.Join(repoRoot, "install.sh"))
		cmd.Dir = root
		cmd.Env = env
		cmd.Stdout = &output
		cmd.Stderr = &output
		err = cmd.Run()

		supportedPair := osName == "Linux" && archName == "x86_64" || osName == "Darwin" && archName == "arm64"
		wantSuccess := envMode != 2 && supportedPair && archiveMode == 0
		if (err == nil) != wantSuccess {
			t.Fatalf("success = %v, want %v (os=%q arch=%q archive=%d env=%d):\n%s", err == nil, wantSuccess, osName, archName, archiveMode, envMode, output.String())
		}
		if !wantSuccess {
			return
		}

		wantPrefix := filepath.Join(home, ".local")
		if envMode == 1 {
			wantPrefix = prefix
		}
		bindir, shareDir := filepath.Join(wantPrefix, "bin"), filepath.Join(wantPrefix, "share", "serf", "bin")
		if envMode == 3 {
			bindir, shareDir = filepath.Join(root, "commands"), filepath.Join(root, "payload")
		}
		for _, bin := range []string{"serf", "serf-hub", "serf-tui", "serf-doctor"} {
			installed := filepath.Join(shareDir, bin)
			info, statErr := os.Stat(installed)
			if statErr != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("installed %s: mode=%v err=%v", bin, info.Mode().Perm(), statErr)
			}
			if target, readErr := os.Readlink(filepath.Join(bindir, bin)); readErr != nil || target != installed {
				t.Fatalf("link %s: target=%q err=%v, want %q", bin, target, readErr, installed)
			}
		}

		gotURL, readErr := os.ReadFile(urlFile)
		if readErr != nil {
			t.Fatal(readErr)
		}
		versionPath := "latest/download"
		if versioned {
			versionPath = "download/v1.2.3"
		}
		wantURL := fmt.Sprintf("https://github.com/prime-radiant-inc/serf/releases/%s/serf_%s_%s.tar.gz", versionPath, strings.ToLower(osName), installArch(archName))
		if strings.TrimSpace(string(gotURL)) != wantURL {
			t.Fatalf("URL = %q, want %q", strings.TrimSpace(string(gotURL)), wantURL)
		}
	})
}

func installArch(arch string) string {
	if arch == "x86_64" {
		return "amd64"
	}
	return arch
}

func installArchiveRoot(osName, arch string) string {
	return "serf_" + strings.ToLower(osName) + "_" + installArch(arch)
}
