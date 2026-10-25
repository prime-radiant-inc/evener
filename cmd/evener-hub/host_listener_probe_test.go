package hub

import (
	"primeradiant.com/evener/execsupport/shellquote"
)

// The live host checks identify the listener on a private port with this probe
// script. It tries lsof, then ss, then the kernel's TCP tables, so a bare host
// without lsof is still probed. Output contract: one PID per line,
// hostListenerPresentMarker, hostNoListenerMarker, or a nonzero exit. Only a nil
// error together with hostNoListenerMarker proves the port is free; a nonzero
// exit means the probe could not say, and the tiers fail closed rather than
// reporting a free port.
const (
	// hostNoListenerMarker is printed when a probe tool ran and found no listener.
	hostNoListenerMarker = "__sshconn_no_listener__"
	// hostListenerPresentMarker is printed when a fallback tool proved a listener
	// exists but could not name its process. It is held, not absent.
	hostListenerPresentMarker = "__sshconn_listener_present__"
)

func hostListenerProbeScript(port string) string {
	colonPort := shellquote.RemoteWord(":" + port)
	return `p=` + shellquote.RemoteWord(port) + `
if command -v lsof >/dev/null 2>&1; then
	lsof -ti ` + colonPort + ` -sTCP:LISTEN; s=$?; if [ $s -eq 1 ]; then echo ` + hostNoListenerMarker + `; exit 0; fi; exit $s
fi
if command -v ss >/dev/null 2>&1; then
	out=$(ss -ltnp 2>/dev/null) && {
		printf '%s\n' "$out" | awk -v port=` + colonPort + ` -v nomatch=` + hostNoListenerMarker + ` -v nopid=` + hostListenerPresentMarker + ` '
			$1 == "LISTEN" && index($4, port) == length($4) - length(port) + 1 {
				hit = 1
				n = split($0, a, "pid=")
				for (i = 2; i <= n; i++) { q = a[i]; sub(/[^0-9].*/, "", q); if (q != "") { print q; named = 1 } }
			}
			END { if (!hit) print nomatch; else if (!named) print nopid }
		'
		s=$?
		exit $s
	}
fi
if [ -r /proc/net/tcp ]; then
	hex=$(printf '%04X' "$p")
	found=0
	for f in /proc/net/tcp /proc/net/tcp6; do
		[ -r "$f" ] || continue
		v=$(awk -v hp="$hex" 'NR > 1 && toupper($4) == "0A" { n = split($2, a, ":"); if (toupper(a[n]) == hp) f = 1 } END { print f ? "hit" : "miss" }' "$f") || { s=$?; echo 'sshconn: cannot read the host TCP tables (awk failed on '"$f"')' >&2; exit $s; }
		[ "$v" = "hit" ] && found=1
	done
	if [ "$found" = 1 ]; then echo ` + hostListenerPresentMarker + `; else echo ` + hostNoListenerMarker + `; fi
	exit 0
fi
echo 'sshconn: no listener probe is available on this host (lsof, ss, and /proc/net/tcp are all missing); install lsof or iproute2' >&2
exit 1
`
}
