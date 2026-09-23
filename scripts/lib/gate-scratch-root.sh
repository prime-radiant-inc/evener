#!/bin/sh
# gate-scratch-root.sh — choose the directory a gate mints its scratch under.
#
# Sourced, never executed; POSIX sh. Sourcing only defines a function.
#
# Every gate stream's TMPDIR, and so every t.TempDir, SQLite fixture, go build
# work directory and atomic file write in the suite, lives under the gate's
# scratch. On a disk those writes pay real fsync latency: on a busy shared NVMe
# the root module spent most of its wall time waiting on fsync (evener-doctor's
# SQLite fixtures went from 52s on disk to 0.5s on tmpfs). The tests assert
# behavior, not durability, so throwaway scratch belongs in RAM when the host
# offers it. /dev/shm is tmpfs on Linux; macOS has no /dev/shm and keeps the
# ambient TMPDIR.

# gate_scratch_root CANDIDATE MIN_KB — print CANDIDATE when it is a writable
# directory with at least MIN_KB kilobytes free that can execute what is written
# to it, else the ambient TMPDIR (or /tmp). The free-space floor keeps a
# container's default 64MB /dev/shm from turning into ENOSPC failures in
# whichever test writes next; the exec probe keeps a noexec /dev/shm (Docker's
# default) from failing every test binary go test builds and runs from TMPDIR.
gate_scratch_root() {
	_gsr_candidate=$1
	_gsr_min_kb=$2
	if [ -d "$_gsr_candidate" ] && [ -w "$_gsr_candidate" ] && gate_scratch_root_executes "$_gsr_candidate"; then
		_gsr_free_kb="$(df -Pk "$_gsr_candidate" 2>/dev/null | awk 'NR == 2 { print $4 }')"
		case "$_gsr_free_kb" in
		'' | *[!0-9]*) ;;
		*)
			if [ "$_gsr_free_kb" -ge "$_gsr_min_kb" ]; then
				printf '%s\n' "$_gsr_candidate"
				return 0
			fi
			;;
		esac
	fi
	printf '%s\n' "${TMPDIR:-/tmp}"
}

# gate_scratch_root_executes DIR — whether a script written to DIR can run: a
# noexec mount is writable and roomy but refuses execution.
gate_scratch_root_executes() {
	_gsr_probe=$(mktemp "$1/.gate-exec-probe.XXXXXX" 2>/dev/null) || return 1
	printf '#!/bin/sh\nexit 0\n' >"$_gsr_probe" && chmod u+x "$_gsr_probe" && "$_gsr_probe" 2>/dev/null
	_gsr_status=$?
	rm -f -- "$_gsr_probe"
	return "$_gsr_status"
}
