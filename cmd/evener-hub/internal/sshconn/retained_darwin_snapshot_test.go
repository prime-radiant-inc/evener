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

// Execute the production byte-bounded record command against real files. Only
// this shell/file boundary is real here, not Darwin lsof or launchd.
func TestRetainedDarwinRecordEvidence(t *testing.T) {
	for _, mode := range []string{"same", "boot", "pid", "config", "root", "build", "malformed", "unknown-field", "trailing-json", "oversized", "symlink", "missing-prior", "startup", "version-omitted", "sha-omitted", "retained-version-omitted", "retained-sha-omitted", "schema", "started-at", "requested-address", "bound-address", "local-origin", "loopback-origin", "public-origin", "listen-source", "sha"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state ")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			report := remoteEndpointFixture("/home/dev/hub.toml", "127.0.0.1:9180", "boot-one")
			report.HubStateRoot, report.Running.HubStateRoot = root, root
			report.Running.BackendGitSHA = "known-sha"
			record := *report.Running
			allow := mode == "same"
			switch mode {
			case "startup":
				record.Version, record.BackendGitSHA = "", ""
				allow = true
			case "version-omitted":
				record.Version = ""
				allow = true
			case "sha-omitted":
				record.BackendGitSHA = ""
				allow = true
			case "retained-version-omitted":
				report.Running.Version = ""
				allow = true
			case "retained-sha-omitted":
				report.Running.BackendGitSHA = ""
				allow = true
			case "schema":
				record.SchemaVersion++
			case "started-at":
				record.StartedAt = record.StartedAt.Add(1)
			case "requested-address":
				record.RequestedListenAddr = "127.0.0.1:1"
			case "bound-address":
				record.BoundAddr = "127.0.0.1:9181"
			case "local-origin":
				record.LocalURL = "http://127.0.0.1:9181"
			case "loopback-origin":
				record.LoopbackURL = "http://127.0.0.1:9181"
			case "public-origin":
				record.PublicURL = "https://different.example"
			case "listen-source":
				record.ListenSource = "flag"
			case "sha":
				record.BackendGitSHA = "other-sha"
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
			if (err == nil) != allow {
				t.Fatalf("record continuity: %v", err)
			}
		})
	}
}
