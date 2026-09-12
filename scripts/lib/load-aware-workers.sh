#!/bin/sh
# load-aware-workers.sh — size a parallel worker pool to the machine's spare
# capacity instead of to its core count.
#
# Sourced, never executed, and POSIX sh so both a bash gate script and a dash
# `sh -c` npm script can source it. Sourcing defines three functions and runs
# nothing: no scratch, no locks, no output. Internals carry a _law_ prefix so
# sourcing cannot clobber a caller's variables.
#
# The design rule: a run that starts on an already-loaded machine should claim
# less. A fleet of concurrent gate runs — agent worktree sessions, CI, and a
# hand-run `make test` — each used to size itself as if it were alone, because
# vitest's default pool is os.availableParallelism() and `go test`'s default -p
# is GOMAXPROCS. The per-run ceilings (four vitest workers, the gate's -p
# budgets) bounded each run but never their sum, which is how a 16-core box
# reaches load 40. This narrows each run's claim as the load average rises.
#
# load_aware_cores — print this machine's effective CPU count, or an empty
# string when nothing can answer. Effective, not advertised: nproc reports the
# CPUs the process may actually run on (its affinity mask, which getconf's
# host-wide online count ignores), and a cgroup CPU quota clamps the result
# again so a quota-limited container does not size to CPUs it cannot use.
# Overstating the count here does not merely look wrong: it defeats the whole
# back-off, because cores - load stays above the ceiling however loaded the
# machine is.
#
# load_aware_quota_cores QUOTA PERIOD — print ceil(QUOTA/PERIOD), the CPUs a
# cgroup bandwidth limit allows, or an empty string when the limit is absent,
# "max", or -1 (v1's unlimited spelling). A pure function of its inputs.
#
# load_aware_cgroup_cores [CGROUP_FILE MOUNTINFO_FILE] — print the most
# restrictive finite CPU quota this process is subject to, or an empty string
# when there is none. The two files default to /proc/self/cgroup and
# /proc/self/mountinfo. Reading only the hierarchy root is not enough: under a
# nested cgroup (a systemd CPUQuota slice, for example) the root says "max"
# while the limit lives several levels down. Reading only one hierarchy is not
# enough either: a hybrid host can mount v2 while binding the cpu controller
# to v1. So both hierarchies are walked from the process's membership up to
# the mount point that exposes it, and the most restrictive finite limit wins.
#
# load_aware_load1 — print this machine's 1-minute load average, or an empty
# string when nothing can answer. Linux reads /proc/loadavg; Darwin's
# `sysctl -n vm.loadavg` prints "{ 2.16 3.57 4.34 }" and is parsed to its first
# field. An unreadable value is reported empty rather than as zero: unknown
# load must not look like an idle machine.
#
# load_aware_workers CAP [CORES] [LOAD1] — print how many workers to run: CAP
# on an idle machine, fewer as the load average rises, never fewer than one.
# CORES defaults to load_aware_cores and LOAD1 to load_aware_load1; a caller
# may pass both, which makes the result a pure function of its inputs (the
# tests do exactly that). LOAD1 may be fractional and is rounded UP before it
# is subtracted, so a partly busy core already costs a worker. CAP <= 0 means
# the ceiling is CORES itself.
#
# An unreadable core count or load average falls back to CAP, not to a guess:
# when the machine cannot be measured, behaving as it did before is safer than
# inventing a number. A malformed CAP is a caller bug, so it is refused with a
# non-zero return and no output rather than silently printing something.
#
# What this is not: admission control. It reads a 1-minute average once, at
# start, so runs that begin together still read the same load and can still
# stack. It narrows each run's claim; it does not serialize runs.

load_aware_cores() {
	_law_cores=
	if command -v nproc >/dev/null 2>&1; then
		_law_cores="$(nproc 2>/dev/null)"
	fi
	if [ -z "$_law_cores" ] && command -v getconf >/dev/null 2>&1; then
		_law_cores="$(getconf _NPROCESSORS_ONLN 2>/dev/null)"
	fi
	case "$_law_cores" in
	''|*[!0-9]*) _law_cores= ;;
	esac
	if [ -n "$_law_cores" ] && [ "$_law_cores" -lt 1 ]; then
		_law_cores=
	fi
	_law_quota="$(load_aware_cgroup_cores)"
	if [ -n "$_law_quota" ]; then
		if [ -z "$_law_cores" ] || [ "$_law_quota" -lt "$_law_cores" ]; then
			_law_cores="$_law_quota"
		fi
	fi
	printf '%s' "$_law_cores"
}

load_aware_quota_cores() {
	_law_quota=${1-}
	_law_period=${2-}
	case "$_law_quota" in
	''|*[!0-9]*) printf ''; return 0 ;;
	esac
	case "$_law_period" in
	''|*[!0-9]*) printf ''; return 0 ;;
	esac
	if [ "$_law_period" -lt 1 ] || [ "$_law_quota" -lt 1 ]; then
		printf ''
		return 0
	fi
	awk -v q="$_law_quota" -v p="$_law_period" '
		BEGIN {
			c = int((q + p - 1) / p)
			if (c < 1) c = 1
			print c
		}'
}

# load_aware_cgroup_relpath FILE VERSION — the process's path inside the
# hierarchy: v2's "0::/path" membership, or a v1 membership whose controller
# list contains cpu.
load_aware_cgroup_relpath() {
	_law_file=${1-/proc/self/cgroup}
	_law_version=${2-v2}
	[ -r "$_law_file" ] || { printf ''; return 0; }
	if [ "$_law_version" = v1 ]; then
		awk -F: '
			{
				n = split($2, controllers, ",")
				for (i = 1; i <= n; i++) {
					if (controllers[i] == "cpu") { print $3; exit }
				}
			}' "$_law_file" 2>/dev/null
		return 0
	fi
	awk -F: '$1 == "0" && $2 == "" { print $3; exit }' "$_law_file" 2>/dev/null
}

# load_aware_cgroup_mount FILE VERSION — print "MOUNTPOINT ROOT" for the mount
# that owns this process's CPU accounting.
load_aware_cgroup_mount() {
	_law_file=${1-/proc/self/mountinfo}
	_law_version=${2-v2}
	[ -r "$_law_file" ] || { printf ''; return 0; }
	if [ "$_law_version" = v1 ]; then
		awk '
			{
				sep = 0
				for (i = 1; i <= NF; i++) { if ($i == "-") { sep = i; break } }
				if (sep == 0 || $(sep + 1) != "cgroup") next
				for (j = sep + 3; j <= NF; j++) {
					n = split($j, controllers, ",")
					for (k = 1; k <= n; k++) {
						if (controllers[k] == "cpu") { print $5, $4; exit }
					}
				}
			}' "$_law_file" 2>/dev/null
		return 0
	fi
	awk '
		{
			sep = 0
			for (i = 1; i <= NF; i++) { if ($i == "-") { sep = i; break } }
			if (sep != 0 && $(sep + 1) == "cgroup2") { print $5, $4; exit }
		}' "$_law_file" 2>/dev/null
}

# load_aware_cgroup_level_cores DIR VERSION — the quota one hierarchy level
# declares, or empty when it declares none.
load_aware_cgroup_level_cores() {
	_law_dir=${1-}
	_law_version=${2-v2}
	if [ "$_law_version" = v1 ]; then
		if [ ! -r "$_law_dir/cpu.cfs_quota_us" ] || [ ! -r "$_law_dir/cpu.cfs_period_us" ]; then
			printf ''
			return 0
		fi
		load_aware_quota_cores \
			"$(cat "$_law_dir/cpu.cfs_quota_us" 2>/dev/null)" \
			"$(cat "$_law_dir/cpu.cfs_period_us" 2>/dev/null)"
		return 0
	fi
	[ -r "$_law_dir/cpu.max" ] || { printf ''; return 0; }
	# v2 keeps "<quota|max> <period>" on one line.
	set -- $(cat "$_law_dir/cpu.max" 2>/dev/null)
	load_aware_quota_cores "${1-}" "${2-}"
}

# load_aware_join MOUNT RELPATH — join without doubling or dropping a slash.
load_aware_join() {
	case "$1" in
	/) printf '/%s' "${2#/}" ;;
	*/) printf '%s%s' "$1" "${2#/}" ;;
	*) printf '%s/%s' "$1" "${2#/}" ;;
	esac
}

load_aware_cgroup_cores() {
	load_aware_cgroup_cores_from "${1-/proc/self/cgroup}" "${2-/proc/self/mountinfo}"
}

load_aware_cgroup_cores_from() {
	_law_cg=${1-}
	_law_mi=${2-}

	_law_best=
	for _law_version in v2 v1; do
		_law_hierarchy="$(load_aware_cgroup_hierarchy_cores "$_law_cg" "$_law_mi" "$_law_version")"
		if [ -n "$_law_hierarchy" ]; then
			if [ -z "$_law_best" ] || [ "$_law_hierarchy" -lt "$_law_best" ]; then
				_law_best="$_law_hierarchy"
			fi
		fi
	done
	printf '%s' "$_law_best"
}

# load_aware_cgroup_hierarchy_cores CGROUP_FILE MOUNTINFO_FILE VERSION — the
# most restrictive finite quota in one hierarchy, or empty. Both hierarchies
# are evaluated because presence is not the same as a finite limit: a hybrid
# host can mount the v2 unified hierarchy while binding the cpu controller to
# v1, and committing to v2 on sight would find "max" there and never consult
# the v1 quota that actually applies.
load_aware_cgroup_hierarchy_cores() {
	_law_cg=${1-}
	_law_mi=${2-}
	_law_version=${3-v2}

	_law_relpath="$(load_aware_cgroup_relpath "$_law_cg" "$_law_version")"
	_law_mount="$(load_aware_cgroup_mount "$_law_mi" "$_law_version")"
	if [ -z "$_law_relpath" ] || [ -z "$_law_mount" ]; then
		printf ''
		return 0
	fi

	# mountinfo field 5 is the mount point. The membership path is relative to
	# the root the mount exposes AT that mount point, so the walk starts at
	# mount point + membership and stops at the mount point itself. Field 4 is
	# where that root already begins, not a directory beneath the mount.
	set -- $_law_mount
	_law_mpoint=${1-/}
	_law_dir="$(load_aware_join "$_law_mpoint" "$_law_relpath")"
	_law_dir=${_law_dir%/}
	[ -n "$_law_dir" ] || _law_dir=/
	_law_stop=${_law_mpoint%/}
	[ -n "$_law_stop" ] || _law_stop=/

	_law_best=
	while :; do
		_law_level="$(load_aware_cgroup_level_cores "$_law_dir" "$_law_version")"
		if [ -n "$_law_level" ]; then
			if [ -z "$_law_best" ] || [ "$_law_level" -lt "$_law_best" ]; then
				_law_best="$_law_level"
			fi
		fi
		[ "$_law_dir" = "$_law_stop" ] && break
		_law_parent="$(dirname "$_law_dir")"
		[ "$_law_parent" = "$_law_dir" ] && break
		case "$_law_parent" in
		"$_law_stop"|"$_law_stop"/*) _law_dir="$_law_parent" ;;
		*) _law_dir="$_law_stop" ;;
		esac
	done
	printf '%s' "$_law_best"
}

load_aware_load1() {
	_law_load=
	if [ -r /proc/loadavg ]; then
		_law_load="$(cut -d' ' -f1 /proc/loadavg 2>/dev/null)"
	elif command -v sysctl >/dev/null 2>&1; then
		_law_load="$(sysctl -n vm.loadavg 2>/dev/null | tr -d '{}' | awk '{print $1}')"
	fi
	case "$_law_load" in
	''|*[!0-9.]*) _law_load= ;;
	esac
	printf '%s' "$_law_load"
}

load_aware_workers() {
	_law_cap=${1-}
	_law_cores=${2-}
	_law_load=${3-}

	case "$_law_cap" in
	''|*[!0-9]*) return 2 ;;
	esac

	[ -n "$_law_cores" ] || _law_cores="$(load_aware_cores)"
	[ -n "$_law_load" ] || _law_load="$(load_aware_load1)"

	# Unknown inputs are unknown, not zero. With no core count, the caller's
	# ceiling is all that can be honored; with no ceiling either, one worker.
	case "$_law_cores" in
	''|*[!0-9]*)
		if [ "$_law_cap" -gt 0 ]; then
			printf '%s\n' "$_law_cap"
		else
			printf '1\n'
		fi
		return 0
		;;
	esac
	if [ "$_law_cores" -lt 1 ]; then
		_law_cores=1
	fi
	case "$_law_load" in
	''|*[!0-9.]*|*.*.*)
		if [ "$_law_cap" -gt 0 ]; then
			printf '%s\n' "$_law_cap"
		else
			printf '%s\n' "$_law_cores"
		fi
		return 0
		;;
	esac

	if [ "$_law_cap" -lt 1 ]; then
		_law_cap="$_law_cores"
	fi

	awk -v cores="$_law_cores" -v load1="$_law_load" -v cap="$_law_cap" '
		BEGIN {
			whole = int(load1)
			if (load1 > whole) whole++
			workers = cores - whole
			if (workers < 1) workers = 1
			if (workers > cap) workers = cap
			print workers
		}'
}
