// Package remoteinstall ships the repository's reviewed installer and the
// remote command that runs it: the one handoff every path installs Evener on a
// remote host with. The `evener install` CLI and the hub's deploy fallback both
// stream Script over ssh and run Command.
package remoteinstall

import (
	_ "embed"
	"strconv"

	"primeradiant.com/evener/internal/shellquote"
)

// Script is the reviewed installer, compiled into this binary: a byte-for-byte
// copy of the repository's install.sh, the same file the documented quickstart
// serves. It is embedded rather than generated so it stays auditable as plain
// shell text and byte-identical across every build.
//
// Why a compiled-in copy rather than a URL: the hub's fallback used to download
// install.sh from the repository's mutable `main` branch and execute it on every
// remote host. A future branch change, or any compromise of the repository,
// therefore gained arbitrary code execution on every auto-deployed host, under
// the controller's ssh identity and with no operator in the loop. The binary the
// operator chose to run is the trust anchor instead: its installer copy is
// reviewed, built, and (for a release) published as part of the same artifact,
// and it cannot change under the controller's feet.
//
// The copy is kept honest by TestEmbeddedScriptMatchesReviewedInstallers and by
// sshconn's TestRound11EmbeddedInstallerMatchesTheReviewedScript, which fail
// whenever it drifts from the repository's install.sh; the fix for a failure is
// to re-copy the reviewed script, never to loosen the check.
//
//go:embed install.sh
var Script []byte

// Command builds the remote POSIX command that writes Script to a temp file and
// executes it, pinned to version, installing into prefix/binDir/shareBinDir.
// An empty path argument pins that variable to an explicitly empty assignment,
// so install.sh computes its documented default and nothing the remote login
// shell exports can override an omitted flag — the pin the hub earned in round
// twelve, now shared with the CLI. The script itself arrives on the command's stdin,
// so the host fetches nothing to execute: its only network use is install.sh's
// own archive and checksums.txt download. The installer runs with the variables
// passed to `env` (not to sh), and every value is rendered as one shell word by
// internal/shellquote.
//
// The command writes the script to a temp file and runs it only after that
// write succeeds AND the file's byte count matches Script's length, rather
// than feeding it straight to an interpreter: a dropped or truncated ssh stream
// must not execute half a script, and ssh reports a dropped stream as a
// successful EOF, so `cat` alone exits 0 on a partial transfer. A pipeline
// returns the LAST command's status, so `cat … | sh` would report sh's status
// and hide the write failure entirely. The count is the same check
// pushBinaryRemote makes — the two handoffs a host executes from a stream must
// fail closed identically.
//
// The count is bound to the script it ships: it is not a parameter, so a
// command that verifies one length while streaming another is not representable.
//
// A tilde in a value does not expand: the values are arguments to `env`, not
// shell assignments, so a leading '~' would name a directory literally called
// '~' on the remote host. Callers pass absolute paths (the CLI rejects
// tilde-prefixed flags for exactly this reason).
func Command(version, prefix, binDir, shareBinDir string) string {
	// Every variable is rendered unconditionally: an omitted flag becomes an
	// explicitly empty assignment (RemoteWord("") is '' and install.sh's
	// ${VAR:-} treats empty as unset), never a variable left to whatever the
	// remote session environment carries — an inherited PREFIX would silently
	// move the install the way it moved the hub's before round twelve.
	env := "EVENER_INSTALL_VERSION=" + shellquote.RemoteWord(version)
	env += " PREFIX=" + shellquote.RemoteWord(prefix)
	env += " BINDIR=" + shellquote.RemoteWord(binDir)
	env += " EVENER_SHARE_BINDIR=" + shellquote.RemoteWord(shareBinDir)
	tmp := `tmp=$(mktemp "${TMPDIR:-/tmp}/evener-install.XXXXXX") || exit 1`
	cleanup := `trap 'rm -f "$tmp"' EXIT HUP INT TERM`
	verify := `v=$(wc -c < "$tmp" | tr -d '[:space:]') && [ "$v" = ` + strconv.Itoa(len(Script)) + ` ]`
	return tmp + "; " + cleanup + "; cat > \"$tmp\" && " + verify + " && env " + env + " sh \"$tmp\""
}
