package remoteinstall

import (
	_ "embed"
	"strconv"

	"primeradiant.com/evener/internal/shellquote"
)

// Script is the reviewed installer sent to a remote host by the CLI and hub.
// The remote command verifies its byte count before executing it.
//
//go:embed install.sh
var Script []byte

// Command builds the remote POSIX command that writes and executes Script.
// The caller must send exactly len(Script) bytes on the command's stdin.
func Command(version, prefix, binDir, shareBinDir string) string {
	env := "EVENER_INSTALL_VERSION=" + shellquote.RemoteWord(version)
	if prefix != "" {
		env += " PREFIX=" + shellquote.RemoteWord(prefix)
	}
	if binDir != "" {
		env += " BINDIR=" + shellquote.RemoteWord(binDir)
	}
	if shareBinDir != "" {
		env += " EVENER_SHARE_BINDIR=" + shellquote.RemoteWord(shareBinDir)
	}
	tmp := `tmp=$(mktemp "${TMPDIR:-/tmp}/evener-install.XXXXXX") || exit 1`
	cleanup := `trap 'rm -f "$tmp"' EXIT HUP INT TERM`
	verify := `v=$(wc -c < "$tmp" | tr -d '[:space:]') && [ "$v" = ` + strconv.Itoa(len(Script)) + ` ]`
	return tmp + "; " + cleanup + "; cat > "$tmp" && " + verify + " && env " + env + " sh "$tmp""
}
