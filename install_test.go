package serf_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	agentplugin "primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/rendezvous"
)

const installedDoctoringSkill = "doctoring-serf"

func TestInstallHomeGeneratedHome(t *testing.T) {
	if testing.Short() {
		t.Skip("install integration test")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	stateHome := filepath.Join(home, ".local", "state")
	cacheHome := filepath.Join(home, ".cache")
	env := installTestEnv(t, home, map[string]string{
		"XDG_CONFIG_HOME": configHome,
		"XDG_STATE_HOME":  stateHome,
		"XDG_CACHE_HOME":  cacheHome,
	})

	runCommand(t, repoRoot, env, "make", "install")

	binDir := filepath.Join(home, ".local", "bin")
	shareBinDir := filepath.Join(home, ".local", "share", "serf", "bin")
	for _, bin := range []string{"serf", "serf-hub", "serf-tui", "serf-doctor"} {
		installed := filepath.Join(shareBinDir, bin)
		info, err := os.Stat(installed)
		if err != nil {
			t.Fatalf("installed binary %s: %v", installed, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("installed binary %s is not executable: mode %s", installed, info.Mode())
		}

		link := filepath.Join(binDir, bin)
		linkInfo, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("installed link %s: %v", link, err)
		}
		if linkInfo.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("installed entry %s is not a symlink: mode %s", link, linkInfo.Mode())
		}
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("readlink %s: %v", link, err)
		}
		if target != installed {
			t.Fatalf("symlink %s -> %s, want %s", link, target, installed)
		}
	}

	if exists(filepath.Join(configHome, "serf")) {
		t.Fatalf("install created %s; Serf should create config dirs on first run", filepath.Join(configHome, "serf"))
	}
	if exists(filepath.Join(home, ".serf")) {
		t.Fatalf("install created %s; Serf should create runtime dirs on first run", filepath.Join(home, ".serf"))
	}

	serfBin := filepath.Join(binDir, "serf")
	runCommand(t, repoRoot, env, serfBin, "--version")
	runCommand(t, repoRoot, env, filepath.Join(binDir, "serf-hub"), "--help")
	runCommand(t, repoRoot, env, filepath.Join(binDir, "serf-tui"), "--help")
	runCommand(t, repoRoot, env, filepath.Join(binDir, "serf-doctor"), "--help")
	runCommand(t, repoRoot, env, serfBin, "--list-sessions")

	for _, dir := range []string{
		filepath.Join(configHome, "serf", "skills"),
		filepath.Join(configHome, "serf", "plugins"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("first Serf run did not create %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
	if exists(filepath.Join(home, ".serf")) {
		t.Fatalf("serf --list-sessions created %s; no hub runtime state should be needed", filepath.Join(home, ".serf"))
	}

	expectedAgents := expectedBundledAgents(t, repoRoot)
	expectedSkills := expectedBundledSkills(t, repoRoot)
	status := installedServeStatus(t, repoRoot, env, serfBin)

	if status.Detailed == nil {
		t.Fatal("installed serf serve /status omitted detailed status")
	}
	installedSkillNames := status.Detailed.SkillNames()
	assertContainsAll(t, "bundled agents", status.Detailed.Agents, expectedAgents)
	assertContainsAll(t, "installed skills", installedSkillNames, []string{installedDoctoringSkill})
	assertSameSet(t, "bundled skills", installedSkillNames, expectedSkills)
}

func TestInstallScriptInstallsReleaseArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	for _, tc := range []struct {
		name     string
		osName   string
		archName string
		asset    string
	}{
		{
			name:     "linux amd64",
			osName:   "Linux",
			archName: "x86_64",
			asset:    "serf_linux_amd64.tar.gz",
		},
		{
			name:     "darwin arm64",
			osName:   "Darwin",
			archName: "arm64",
			asset:    "serf_darwin_arm64.tar.gz",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			fixtures := t.TempDir()
			archive := filepath.Join(fixtures, tc.asset)
			writeInstallReleaseArchive(t, archive, strings.TrimSuffix(tc.asset, ".tar.gz"))

			fakeBin := filepath.Join(fixtures, "bin")
			if err := os.Mkdir(fakeBin, 0o755); err != nil {
				t.Fatalf("mkdir fake bin: %v", err)
			}
			writeExecutable(t, filepath.Join(fakeBin, "uname"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  -s) echo %q ;;
  -m) echo %q ;;
  *) echo "unsupported uname args: $*" >&2; exit 2 ;;
esac
`, tc.osName, tc.archName))

			urlFile := filepath.Join(fixtures, "curl-url")
			writeExecutable(t, filepath.Join(fakeBin, "curl"), `#!/bin/sh
out=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [ -z "$out" ]; then
  echo "missing -o" >&2
  exit 2
fi
printf '%s\n' "$url" > "$SERF_FAKE_CURL_URL_FILE"
cp "$SERF_FAKE_CURL_ARCHIVE" "$out"
`)

			env := installTestEnv(t, home, map[string]string{
				"PATH":                    fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"SERF_INSTALL_VERSION":    "v1.2.3",
				"SERF_FAKE_CURL_ARCHIVE":  archive,
				"SERF_FAKE_CURL_URL_FILE": urlFile,
			})
			runCommand(t, repoRoot, env, "sh", script)

			expectedURL := "https://github.com/prime-radiant-inc/serf/releases/download/v1.2.3/" + tc.asset
			data, err := os.ReadFile(urlFile)
			if err != nil {
				t.Fatalf("read fake curl URL: %v", err)
			}
			if got := strings.TrimSpace(string(data)); got != expectedURL {
				t.Fatalf("download URL = %q, want %q", got, expectedURL)
			}

			binDir := filepath.Join(home, ".local", "bin")
			shareBinDir := filepath.Join(home, ".local", "share", "serf", "bin")
			for _, bin := range []string{"serf", "serf-hub", "serf-tui", "serf-doctor"} {
				installed := filepath.Join(shareBinDir, bin)
				info, err := os.Stat(installed)
				if err != nil {
					t.Fatalf("installed binary %s: %v", installed, err)
				}
				if info.Mode().Perm()&0o111 == 0 {
					t.Fatalf("installed binary %s is not executable: mode %s", installed, info.Mode())
				}

				link := filepath.Join(binDir, bin)
				target, err := os.Readlink(link)
				if err != nil {
					t.Fatalf("readlink %s: %v", link, err)
				}
				if target != installed {
					t.Fatalf("symlink %s -> %s, want %s", link, target, installed)
				}
			}
		})
	}
}

func installedServeStatus(t *testing.T, repoRoot string, baseEnv []string, serfBin string) installedStatus {
	t.Helper()

	runDir := t.TempDir()
	stateDir := t.TempDir()
	workDir := t.TempDir()
	providersPath := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(providersPath, []byte(`
schema = 1
default = "work"

[instances.work]
type = "openai"
api_style = "responses"
api_key = "sk-install-test"
`), 0o600); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	env := overlayEnv(baseEnv, map[string]string{
		"SERF_PROVIDERS_CONFIG": providersPath,
		"SERF_HUB_TOKEN":        "",
	})

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(serfBin,
		"serve",
		"--model", "work/gpt-5.2",
		"--addr", "127.0.0.1:0",
		"--dir", workDir,
		"--run-dir", runDir,
		"--state-dir", stateDir,
		"--no-project-prompts",
	)
	cmd.Dir = repoRoot
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start installed serf serve: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var entry rendezvous.Entry
	started := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("installed serf serve exited before rendezvous entry: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		default:
		}

		entries, err := rendezvous.List(runDir)
		if err != nil {
			t.Fatalf("list rendezvous entries: %v", err)
		}
		if len(entries) > 0 && entries[0].Address != "" {
			entry = entries[0]
			started = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("installed serf serve did not start\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		resp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
		if err == nil {
			_ = resp.Body.Close()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + entry.Address + "/status")
	if err != nil {
		t.Fatalf("get installed serf serve /status: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200", resp.StatusCode)
	}

	var status installedStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	return status
}

type installedStatus struct {
	Detailed *installedDetailedStatus `json:"detailed"`
}

type installedDetailedStatus struct {
	Agents []string             `json:"agents"`
	Skills []installedSkillInfo `json:"skills"`
}

type installedSkillInfo struct {
	Name string `json:"name"`
}

func (s installedDetailedStatus) SkillNames() []string {
	names := make([]string, 0, len(s.Skills))
	for _, meta := range s.Skills {
		names = append(names, meta.Name)
	}
	return names
}

func expectedBundledAgents(t *testing.T, repoRoot string) []string {
	t.Helper()

	dir := filepath.Join(repoRoot, "internal", "bundled", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundled agents: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read bundled agent %s: %v", entry.Name(), err)
		}
		agent, err := agentplugin.ParseAgent(data, "builtin")
		if err != nil {
			t.Fatalf("parse bundled agent %s: %v", entry.Name(), err)
		}
		names = append(names, agent.Name)
	}
	sort.Strings(names)
	return names
}

func expectedBundledSkills(t *testing.T, repoRoot string) []string {
	t.Helper()

	metas := map[string]skill.SkillMeta{}
	skill.ScanSkillsDir(filepath.Join(repoRoot, "internal", "bundled", "skills"), metas)
	names := make([]string, 0, len(metas))
	for name := range metas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func assertContainsAll(t *testing.T, label string, got, want []string) {
	t.Helper()

	gotSet := make(map[string]bool, len(got))
	for _, name := range got {
		gotSet[name] = true
	}
	var missing []string
	for _, name := range want {
		if !gotSet[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(got)
		t.Fatalf("%s missing %v; got %v", label, missing, got)
	}
}

func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()

	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func runCommand(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func writeInstallReleaseArchive(t *testing.T, path, root string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create release archive: %v", err)
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, bin := range []string{"serf", "serf-hub", "serf-tui", "serf-doctor"} {
		body := fmt.Sprintf("#!/bin/sh\necho %s\n", bin)
		header := &tar.Header{
			Name: filepath.ToSlash(filepath.Join(root, bin)),
			Mode: 0o755,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func installTestEnv(t *testing.T, home string, extra map[string]string) []string {
	t.Helper()

	overrides := map[string]string{
		"HOME":       home,
		"GOMODCACHE": goEnv(t, "GOMODCACHE"),
		"GOCACHE":    goEnv(t, "GOCACHE"),
		"GOPATH":     goEnv(t, "GOPATH"),
	}
	for key, value := range extra {
		overrides[key] = value
	}
	return overlayEnv(os.Environ(), overrides)
}

func goEnv(t *testing.T, key string) string {
	t.Helper()

	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", key, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		t.Fatalf("go env %s returned empty value", key)
	}
	return value
}

func overlayEnv(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, override := overrides[key]; override {
			continue
		}
		out = append(out, item)
	}
	for key, value := range overrides {
		out = append(out, fmt.Sprintf("%s=%s", key, value))
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
