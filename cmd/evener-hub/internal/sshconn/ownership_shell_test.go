package sshconn

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This executes our generated shell, not systemd. The shim accepts only the
// declared property and manager-environment queries and returns raw fixture data.
func TestServiceDefinitionRemoteShell(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sed", "cksum", "grep", "sort", "cat"} {
		target, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
if [ "$1" = --user ]; then shift; fi
case "$*" in
 'show --property=User,ExecStart,WorkingDirectory,Environment,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- evener-hub-selected.service') cat "$FIXTURE/definition" ;;
 show-environment) cat "$FIXTURE/manager" ;;
 *) exit 97 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "UNRELATED_SENTINEL_08a93c"
	definition := func(argv, stamp, file string) string {
		return fmt.Sprintf("User=dev\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub %s ; ignore_errors=no ; start_time=%s ; stop_time=n/a ; pid=4242 ; code=(null) ; status=0/0 }\nWorkingDirectory=/srv\nEnvironment=HOME=/home/dev SECRET=%s\nEnvironmentFiles=%s\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=yes\n", argv, stamp, secret, file)
	}
	write("manager", "HOME=/home/dev\nEVENER_HUB_LISTEN_ADDR=0.0.0.0:0\nUNRELATED="+secret+"\n")
	envFile := filepath.Join(dir, "launch.env")
	write("launch.env", "XDG_STATE_HOME=/srv/state\nSECRET="+secret+"\n")
	file := envFile + " (ignore_errors=no)"
	write("definition", definition("--config=/srv/hub.toml", "Fri 2026-10-16 10:00:00 UTC", file))
	run := func(kind supervisorKind) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", serviceDefinitionRemote(supervisor{kind: kind, label: "evener-hub-selected.service"}))
		cmd.Env = []string{"PATH=" + bin, "HOME=" + dir, "FIXTURE=" + dir, "LC_ALL=C"}
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err := cmd.Run()
		if bytes.Contains(out.Bytes(), []byte(secret)) || bytes.Contains(diagnostic.Bytes(), []byte(secret)) {
			t.Fatal("unrelated secret crossed shell output")
		}
		return out.Bytes(), err
	}
	for _, kind := range []supervisorKind{supervisorSystemd, supervisorSystemdUser} {
		baseline, err := run(kind)
		if err != nil {
			t.Fatal(err)
		}
		write("definition", strings.ReplaceAll(definition("--config=/srv/hub.toml", "Sat 2026-10-17 11:00:00 UTC", file), "pid=4242", "pid=4343"))
		stable, err := run(kind)
		if err != nil || !bytes.Equal(baseline, stable) {
			t.Fatalf("runtime fields changed definition: %q / %q, %v", baseline, stable, err)
		}
		write("definition", definition("--config=/srv/other.toml", "n/a", file))
		launch, err := run(kind)
		if err != nil || bytes.Equal(baseline, launch) {
			t.Fatalf("launch not invalidated: %v", err)
		}
		write("definition", definition("--config=/srv/hub.toml", "n/a", file))
		write("manager", "HOME=/home/dev\nEVENER_HUB_LISTEN_ADDR=[::]:0\nUNRELATED="+secret+"\n")
		env, err := run(kind)
		if err != nil || bytes.Equal(baseline, env) {
			t.Fatalf("relevant environment not invalidated: %v", err)
		}
		write("manager", "HOME=/home/dev\nEVENER_HUB_LISTEN_ADDR=0.0.0.0:0\nUNRELATED="+secret+"\n")
		write("launch.env", "XDG_STATE_HOME=/srv/changed\nSECRET="+secret+"\n")
		changed, err := run(kind)
		if err != nil || bytes.Equal(baseline, changed) {
			t.Fatalf("file content not invalidated: %v", err)
		}
		missing := filepath.Join(dir, "missing.env")
		write("definition", definition("--config=/srv/hub.toml", "n/a", missing+" (ignore_errors=yes)"))
		optional, err := run(kind)
		if err != nil || bytes.Equal(baseline, optional) {
			t.Fatalf("optional missing: %v", err)
		}
		write("definition", definition("--config=/srv/hub.toml", "n/a", missing+" (ignore_errors=no)"))
		if _, err := run(kind); err == nil {
			t.Fatal("required missing environment file accepted")
		}
		write("definition", definition("--config=/srv/hub.toml", "n/a", file))
		write("launch.env", "XDG_STATE_HOME=/srv/state\nSECRET="+secret+"\n")
	}
}

func TestFreshOwnerEnvironmentRemoteShell(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", freshOwnerEnvironmentRemote)
	cmd.Env = []string{"HOME=/home/has space", "XDG_CONFIG_HOME=", "EVENER_HUB_LISTEN_ADDR=first\nsecond", "UNRELATED=DO_NOT_EXPORT_40e29"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if want := "HOME=/home/has space\x00XDG_CONFIG_HOME=\x00EVENER_HUB_LISTEN_ADDR=first\nsecond\x00"; string(out) != want {
		t.Fatalf("fresh launch environment=%q", out)
	}
}
