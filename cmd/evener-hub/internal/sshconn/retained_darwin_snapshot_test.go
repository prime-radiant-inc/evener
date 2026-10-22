package sshconn

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestDarwinRetainedLockSnapshot(t *testing.T) {
	for _, row := range []struct {
		name, out, want string
		refuse          bool
	}{
		{"free", "p99\nf1\ntREG\nD0x4d2\ni123\n", "", false},
		{"owner", "p4242\nf3\ntREG\nkW\nD0x4d2\ni678\n", "4242", false},
		{"multiple-files", "p99\nf1\ntREG\nD0x4d2\ni123\np4242\nf3\ntREG\nD0x4d2\ni678\nkW\nf4\ntIPv4\n", "4242", false},
		{"other-device", "p4242\nf3\ntREG\nD0x4d3\ni678\nkW\n", "", false},
		{"unlocked-open", "p4242\nf3\ntREG\nD0x4d2\ni678\n", "", true},
		{"partial-lock", "p4242\nf3\ntREG\nD0x4d2\ni678\nkw\n", "", true},
		{"two-owners", "p4242\nf3\ntREG\nD0x4d2\ni678\nkW\np9090\nf3\ntREG\nD0x4d2\ni678\nkW\n", "", true},
		{"empty", "", "", true},
		{"pid-only", "p4242\n", "", true},
		{"missing-pid", "f3\ntREG\nD0x4d2\ni678\nkW\n", "", true},
		{"missing-inode", "p4242\nf3\ntREG\nD0x4d2\nkW\n", "", true},
		{"missing-device", "p4242\nf3\ntREG\ni678\nkW\n", "", true},
		{"missing-type", "p4242\nf3\nD0x4d2\ni678\nkW\n", "", true},
		{"bad-device", "p4242\nf3\ntREG\nDinvalid\ni678\nkW\n", "", true},
		{"bad-inode", "p4242\nf3\ntREG\nD0x4d2\niunknown\nkW\n", "", true},
		{"warning-with-success", "lsof: cannot examine a process\np99\nf1\ntREG\nD0x4d2\ni123\n", "", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			got, err := darwinLockSnapshot([]byte(row.out), 1234, 678)
			if (err != nil) != row.refuse || (err == nil && got != row.want) {
				t.Fatalf("pid=%q err=%v, want %q refusal=%t", got, err, row.want, row.refuse)
			}
		})
	}
}

// Execute the production byte-bounded record command against real files. Only
// this shell/file boundary is real here, not Darwin lsof or launchd.
func TestRetainedDarwinRecordEvidence(t *testing.T) {
	for _, mode := range []string{"same", "boot", "pid", "config", "root", "build", "malformed", "unknown-field", "trailing-json", "oversized", "symlink", "missing-prior"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state ")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			report := remoteEndpointFixture("/home/dev/hub.toml", "127.0.0.1:9180", "boot-one")
			report.HubStateRoot, report.Running.HubStateRoot = root, root
			record := *report.Running
			switch mode {
			case "boot":
				record.BootID = "boot-two"
			case "pid":
				record.PID++
			case "config":
				record.ConfigPath = "/other.toml"
			case "root":
				record.HubStateRoot = "/other"
			case "build":
				record.Version = "other-build"
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "malformed":
				data = []byte("{")
			case "unknown-field":
				data = append([]byte(`{"unknown":true,`), data[1:]...)
			case "trailing-json":
				data = append(data, []byte(" {}")...)
			case "oversized":
				data = []byte(strings.Repeat(" ", 65537))
			case "missing-prior":
				report.Running = nil
			}
			file := filepath.Join(root, "hub-endpoint.json")
			if mode == "symlink" {
				other := filepath.Join(root, "other.json")
				if err := os.WriteFile(other, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, file); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(file, data, 0o600); err != nil {
				t.Fatal(err)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
				return exec.CommandContext(ctx, "/bin/sh", "-c", strings.Join(argv[12:], " ")).Output()
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			err = m.retainedDarwinRecord(t.Context(), host, selectedOwner{report: report})
			if (err != nil) != (mode != "same") {
				t.Fatalf("record continuity: %v", err)
			}
		})
	}
}
