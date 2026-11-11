package sshconn

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/internal/testfs"
)

// bsdReadlink models the readlink a macOS remote host runs: without -n it ends
// its output with a newline only when the link target does not already end in
// one, so a target named "evener\n" prints the same as "evener" would.
const bsdReadlink = `nl='
'
readlink() {
  if [ "$1" = -n ]; then command readlink -n "$2"; return; fi
  t=$(command readlink -n "$1" && printf x) || return 1
  t=${t%x}
  case $t in
    *"$nl") printf '%s' "$t" ;;
    *) printf '%s\n' "$t" ;;
  esac
}
`

func TestRetainedDeployPathResolution(t *testing.T) {
	for _, mode := range []string{"existing", "missing", "missing-parent", "directory", "cycle", "trailing-space", "newline"} {
		t.Run(mode, func(t *testing.T) {
			root := testfs.CanonicalTempDir(t)
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
			for _, readlink := range []struct{ name, prelude string }{{"native", ""}, {"bsd", bsdReadlink}} {
				for _, retained := range []bool{false, true} {
					command := resolveDeployCommand(alias)
					if retained {
						command = resolveRetainedDeployCommand(alias)
					}
					out, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", readlink.prelude+command).Output()
					wantOK := mode == "existing" || retained && (mode == "missing" || mode == "trailing-space")
					if wantOK {
						if err != nil || string(out) != target+"\n" {
							t.Fatalf("readlink=%s retained=%v: out=%q err=%v want=%q", readlink.name, retained, out, err, target+"\n")
						}
					} else if err == nil || len(out) != 0 {
						t.Fatalf("readlink=%s retained=%v accepted invalid path: out=%q err=%v", readlink.name, retained, out, err)
					}
				}
			}
		})
	}
}
