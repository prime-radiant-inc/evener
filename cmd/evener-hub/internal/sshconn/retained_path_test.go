package sshconn

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRetainedDeployPathResolution(t *testing.T) {
	for _, mode := range []string{"existing", "missing", "missing-parent", "directory", "cycle", "trailing-space", "newline"} {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			name := "evener"
			switch mode {
			case "trailing-space":
				name += " "
			case "newline":
				name += "\n"
			case "missing-parent":
				name = "absent/evener"
			}
			target, alias := filepath.Join(root, name), filepath.Join(root, "launch")
			if err := os.Symlink(name, alias); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "existing":
				if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			case "cycle":
				if err := os.Symlink("launch", target); err != nil {
					t.Fatal(err)
				}
			}
			for _, retained := range []bool{false, true} {
				command := resolveDeployCommand(alias)
				if retained {
					command = resolveRetainedDeployCommand(alias)
				}
				out, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", command).Output()
				wantOK := mode == "existing" || retained && (mode == "missing" || mode == "trailing-space")
				if wantOK {
					if err != nil || string(out) != target+"\n" {
						t.Fatalf("retained=%v: out=%q err=%v want=%q", retained, out, err, target+"\n")
					}
				} else if err == nil || len(out) != 0 {
					t.Fatalf("retained=%v accepted invalid path: out=%q err=%v", retained, out, err)
				}
			}
		})
	}
}
