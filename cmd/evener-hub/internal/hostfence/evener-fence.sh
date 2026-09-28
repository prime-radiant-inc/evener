#!/bin/sh
# evener-fence — crash-fencing's remote lease wrapper, version 1.
#
# Install path (spec 08c §6): ~/.local/share/evener/fence. This helper is
# installed out-of-band before the first fenced operation; there is no in-band
# migration and no auto-install.
#
# Every argument is validated here, and this file is the whole remote fence: a
# controller that cannot present an epoch this guard names mutates nothing.
#
# Operations (one per invocation; all state under $EVENER_FENCE_STATE,
# default ~/.local/state/evener/fence):
#
#   version                       print the helper version (one number)
#   status                        print the guard/lease state as one JSON object
#   entries                       print the lease entries as one JSON object
#   takeover <bootId> <opSeq>     §4's preemptive fence-takeover: revoke the
#                                 superseded epoch, install this epoch as the
#                                 lease holder, record the fence state (fencing
#                                 epoch, superseded epoch, monotonic fence
#                                 sequence) in one atomic step, and advance the
#                                 guard file's monotonic sequence
#   advance <bootId> <opSeq>      §4's compare-and-advance: only the epoch the
#                                 pending fence names may advance, the sequence
#                                 advances by that one compare-and-swap, and an
#                                 older epoch never overwrites a newer one
#   perform <bootId> <opSeq> <cmd>
#                                 §4's register-fence-perform guarded unit: under
#                                 the exclusive lease, check the epoch against
#                                 the guard, register the command in the lease
#                                 file before its side effects, run it, and
#                                 record its exit. The command's stdout/stderr
#                                 pass through and its exit status is this
#                                 helper's exit status.
#   recheck <id>                  §9's nonce re-presentation: report whether the
#                                 lease entry carrying that identity still
#                                 names a live holder (read-only, lock-free)
#
# status, entries, and recheck are read-only and take no lock: a verifier must
# be able to enumerate and re-present identities while a command holds the
# exclusive lease, so they read one cat snapshot of each file and never block
# behind a running command. Only takeover, advance, and perform take the lock.
#
# Files, all mode 0600 (umask 077) and written temp+fsync+rename+dir-fsync:
#
#   guard       flat, one "key<TAB>value" line per field. Keys: version,
#               guardEpoch, epochBootId, epochOpSeq, supersededBootId,
#               supersededOpSeq, fenceBootId, fenceOpSeq,
#               fenceSupersededBootId, fenceSupersededOpSeq, fenceGuardEpoch,
#               plus one "boot.<bootId><TAB><opSeq>" high-water record per boot
#               the guard has admitted. "-" means absent; op sequences and the
#               guard epoch are decimal. A key outside this set is corrupt, and
#               an empty boot value is corrupt rather than absent.
#   leases/     one file per lease entry, named by the entry id (the per-spawn
#               nonce). Fields: id, command (stored JSON-escaped), registeredAt,
#               state, ownershipKind, pid, pidStartTime, nonce, cgroupId, exit,
#               exitedAt, descendants (the pids still carrying the nonce after
#               the command exited; while any live the entry stays running).
#   leases/holder  "<bootId> <opSeq>" of the epoch holding the exclusive lease.
#   lock        the exclusive-lease claim file: "<pid> <startToken>", published
#               with one atomic link so it always carries a provable owner. A
#               stale claim is moved aside and discarded only when it still
#               names the observed dead owner (a reused pid never authorizes
#               taking a live replacement's lock).
#
# Refusals: "evener-fence: " then one JSON object on stderr, {"version":1,
# "refused":true,"error":"<reason>","detail":"..."}, with reason one of
# stale-epoch, fenced, busy, state-corrupt, malformed, io-error. The prefix is
# the helper's own marker, so a wrapped command's stderr can never masquerade as
# a wrapper refusal; the Go decoder requires the prefix and the helper's own
# exit code. Exit codes: 0 success; the wrapped command's own status for
# perform; 64 malformed request; 69 corrupt state or I/O failure; 75 fencing
# refusal. Nothing here kills a remote process: kill/wait and the quarantine
# marker belong to the fencing worker (S18), which verifies entries through
# `entries` and `recheck` before signaling.
#
# Three environment seams exist for the tests' fault injection, mirroring the Go
# stores' fault seams: EVENER_FENCE_FAULT_AFTER_GUARD=1 exits right after a
# takeover's guard write (before the holder write, the crash window the replay
# reconciliation repairs), EVENER_FENCE_FAULT_AFTER_SPAWN=1 fails the post-spawn
# entry write so the kill-on-tracking-failure path is exercised, and
# EVENER_FENCE_FAULT_UNREADABLE_START=1 hides a live process's start token so
# the fail-closed recheck arm is exercised. None is set in production.
#
# EVENER_FENCE_TOKEN, when set, is echoed in every refusal: the caller mints it
# per invocation and clears it for the wrapped command, so a command's own
# stderr can never pass for a wrapper refusal. EVENER_FENCE_LOCK_ATTEMPTS bounds
# a waiter's 0.1s retries against a live holder (default 100).

set -eu
umask 077

VERSION=1
PROTOCOL=1

STATE_DIR=${EVENER_FENCE_STATE:-${XDG_STATE_HOME:-${HOME:-.}/.local/state}/evener/fence}
GUARD_FILE=$STATE_DIR/guard
LEASE_DIR=$STATE_DIR/leases
HOLDER_FILE=$LEASE_DIR/holder
LOCK_FILE=$STATE_DIR/lock

TAB=$(printf '\t')

refuse() { # refuse <reason> <detail> <exit>
	# Refusals wear the helper's own marker so a wrapped command's stderr can
	# never masquerade as one; the Go side strips and requires it.
	printf 'evener-fence: {"version":%d,"refused":true,"error":"%s","detail":"%s","token":"%s"}\n' \
		"$PROTOCOL" "$1" "$(json_escape "$2")" "$(json_escape "${EVENER_FENCE_TOKEN:-}")" >&2
	exit "$3"
}

refuse_malformed() { refuse malformed "$1" 64; }
refuse_stale() { refuse stale-epoch "$1" 75; }
refuse_fenced() { refuse fenced "$1" 75; }
refuse_corrupt() { refuse state-corrupt "$1" 69; }

json_escape() { # one value as a JSON string body
	# A per-character loop, not gsub: backslash handling in a gsub replacement
	# is awk-dependent (identity on gawk/POSIX), and the loop is exact. Every
	# C0 control except NUL is emitted as a \u00XX escape, so the value is
	# always valid JSON and always one line.
	printf '%s' "$1" | awk '
		BEGIN {
			ORS = ""
			for (i = 1; i < 32; i++) { ctrl[sprintf("%c", i)] = sprintf("\\u%04x", i) }
		}
		{
			out = ""
			n = length($0)
			for (i = 1; i <= n; i++) {
				c = substr($0, i, 1)
				if (c == "\\") { out = out "\\\\" }
				else if (c == "\"") { out = out "\\\"" }
				else if (c in ctrl) { out = out ctrl[c] }
				else { out = out c }
			}
			if (NR > 1) { printf "\\n" }
			printf "%s", out
		}'
}

is_uint() {
	case $1 in
	'' | *[!0-9]*) return 1 ;;
	*) return 0 ;;
	esac
}

sync_path() { # best-effort durability for a file or directory
	sync "$1" 2>/dev/null || sync 2>/dev/null || true
}

ensure_state() {
	if [ ! -d "$STATE_DIR" ]; then
		mkdir -p "$STATE_DIR" || refuse io-error "cannot create the helper state root" 69
	fi
	if [ ! -d "$LEASE_DIR" ]; then
		mkdir -p "$LEASE_DIR" || refuse io-error "cannot create the lease directory" 69
	fi
}

# --- lock --------------------------------------------------------------------

# The lock is a regular file claimed by an atomic hard link: the claim file is
# written with the owner's identity (pid plus its kernel-owned start token)
# before the link exists, so a visible lock always carries a provable owner. A
# stale claim is moved aside with an atomic rename — exactly one waiter wins —
# and discarded only when it still names the observed dead owner; a claim that
# was replaced in the window is put back.
acquire_lock() {
	ensure_state
	attempt=0
	limit=${EVENER_FENCE_LOCK_ATTEMPTS:-100}
	while :; do
		if claim_lock; then
			return 0
		fi
		observed=$(cat "$LOCK_FILE" 2>/dev/null || true)
		pid=${observed%% *}
		case $pid in
		'' | *[!0-9]*)
			# An empty or contentless claim is a crash artifact: nothing
			# provable holds it.
			steal_stale_lock "$observed"
			;;
		*)
			if ! kill -0 "$pid" 2>/dev/null; then
				steal_stale_lock "$observed"
			fi
			;;
		esac
		attempt=$((attempt + 1))
		[ "$attempt" -gt "$limit" ] && return 1
		sleep 0.1
	done
}

# claim_lock writes this invocation's claim and publishes it with one atomic
# link, so the identity is durable before the lock is visible.
claim_lock() {
	start=$(pid_start_time "$$" || printf unknown)
	claim=$STATE_DIR/.tmp.lock.$$
	printf '%s %s\n' "$$" "$start" >"$claim" 2>/dev/null || {
		rm -f "$claim"
		return 1
	}
	chmod 600 "$claim" 2>/dev/null || true
	if ln "$claim" "$LOCK_FILE" 2>/dev/null; then
		rm -f "$claim" 2>/dev/null || true
		return 0
	fi
	rm -f "$claim" 2>/dev/null || true
	return 1
}

# steal_stale_lock takes <observed claim content>. It re-verifies the moved
# claim: the content must equal what the caller observed AND its owner must be
# dead (or unprovable). Anything else was replaced in the window and is
# restored when the path is free, never discarded.
steal_stale_lock() {
	stale=$STATE_DIR/.tmp.stale.$$
	mv "$LOCK_FILE" "$stale" 2>/dev/null || return 1
	moved=$(cat "$stale" 2>/dev/null || true)
	moved_pid=${moved%% *}
	provably_dead=true
	case $moved_pid in
	'' | *[!0-9]*) ;;
	*)
		if kill -0 "$moved_pid" 2>/dev/null; then
			provably_dead=false
		fi
		;;
	esac
	if [ "$moved" = "$1" ] && [ "$provably_dead" = true ]; then
		rm -f "$stale" 2>/dev/null || true
		return 0
	fi
	if [ ! -e "$LOCK_FILE" ] && mv "$stale" "$LOCK_FILE" 2>/dev/null; then
		return 1
	fi
	rm -f "$stale" 2>/dev/null || true
	return 1
}

release_lock() {
	# Never remove a lock this invocation does not own: an unconditional rm
	# could delete a lock a later owner acquired after ours was stolen.
	observed=$(cat "$LOCK_FILE" 2>/dev/null || true)
	pid=${observed%% *}
	[ "$pid" = "$$" ] || return 0
	rm -f "$LOCK_FILE" 2>/dev/null || true
}

# --- guard file --------------------------------------------------------------

# guard_field and guard_file_valid parse one snapshot of the guard file. Every
# reader takes the snapshot with a single cat first: the file is replaced by
# rename, so one open fd sees one version, while reading field-by-field across
# opens could mix two versions into a combination no writer ever produced.
guard_field() { # <snapshot> <key>
	printf '%s\n' "$1" | awk -F"$TAB" -v key="$2" '$1 == key { print $2; exit }'
}

guard_file_valid() { # <snapshot>
	printf '%s\n' "$1" | awk -F"$TAB" '
		BEGIN {
			split("version guardEpoch epochBootId epochOpSeq supersededBootId supersededOpSeq fenceBootId fenceOpSeq fenceSupersededBootId fenceSupersededOpSeq fenceGuardEpoch", keys, " ")
			for (i in keys) { fixed[keys[i]] = 1 }
		}
		NF != 2 { bad = 1 }
		$1 ~ /^boot\./ {
			if ($1 !~ /^boot\.[A-Za-z0-9._-]+$/ || $2 !~ /^[0-9]+$/ || $2 + 0 < 1) { bad = 1 }
		}
		{ seen[$1]++ }
		END {
			for (key in seen) {
				if (seen[key] != 1) { bad = 1 }
				if (!(key in fixed) && key !~ /^boot\./) { bad = 1 }
			}
			for (i in keys) { if (!(keys[i] in seen)) { bad = 1 } }
			exit bad ? 1 : 0
		}' >/dev/null 2>&1
}

# read_guard loads the guard state into GUARD_* globals. A missing file is the
# empty state (a host no fencing has touched); a present file outside the schema
# is corrupt and fails closed.
read_guard() {
	GUARD_EPOCH=0
	GUARD_SNAPSHOT=""
	EPOCH_BOOT=-
	EPOCH_SEQ=0
	SUP_BOOT=-
	SUP_SEQ=0
	FENCE_BOOT=-
	FENCE_SEQ=0
	FENCE_SUP_BOOT=-
	FENCE_SUP_SEQ=0
	FENCE_GUARD=0
	[ -f "$GUARD_FILE" ] || return 0
	GUARD_SNAPSHOT=$(cat "$GUARD_FILE" 2>/dev/null) || return 1
	guard_file_valid "$GUARD_SNAPSHOT" || return 1
	[ "$(guard_field "$GUARD_SNAPSHOT" version)" = "$PROTOCOL" ] || return 1
	GUARD_EPOCH=$(guard_field "$GUARD_SNAPSHOT" guardEpoch)
	EPOCH_BOOT=$(guard_field "$GUARD_SNAPSHOT" epochBootId)
	EPOCH_SEQ=$(guard_field "$GUARD_SNAPSHOT" epochOpSeq)
	SUP_BOOT=$(guard_field "$GUARD_SNAPSHOT" supersededBootId)
	SUP_SEQ=$(guard_field "$GUARD_SNAPSHOT" supersededOpSeq)
	FENCE_BOOT=$(guard_field "$GUARD_SNAPSHOT" fenceBootId)
	FENCE_SEQ=$(guard_field "$GUARD_SNAPSHOT" fenceOpSeq)
	FENCE_SUP_BOOT=$(guard_field "$GUARD_SNAPSHOT" fenceSupersededBootId)
	FENCE_SUP_SEQ=$(guard_field "$GUARD_SNAPSHOT" fenceSupersededOpSeq)
	FENCE_GUARD=$(guard_field "$GUARD_SNAPSHOT" fenceGuardEpoch)
	is_uint "$GUARD_EPOCH" || return 1
	is_uint "$FENCE_GUARD" || return 1
	check_boot_pair "$EPOCH_BOOT" "$EPOCH_SEQ" || return 1
	check_boot_pair "$SUP_BOOT" "$SUP_SEQ" || return 1
	check_boot_pair "$FENCE_BOOT" "$FENCE_SEQ" || return 1
	check_boot_pair "$FENCE_SUP_BOOT" "$FENCE_SUP_SEQ" || return 1
	if [ "$FENCE_BOOT" = "-" ]; then
		[ "$FENCE_GUARD" = 0 ] || return 1
	else
		# The fence's sequence sits inside the guard's own, exactly as the Go
		# validator decides: a fence ahead of the guard is corrupt.
		[ "$FENCE_GUARD" -ge 1 ] && [ "$FENCE_GUARD" -le "$GUARD_EPOCH" ] || return 1
	fi
	return 0
}

# check_boot_pair validates one (bootId, opSeq) field pair: "-" is the absent
# form with op sequence 0, and anything else must be a non-empty token-safe boot
# id with a positive sequence. An empty boot id is corrupt, never absent.
check_boot_pair() { # <bootId> <opSeq>
	case $1 in
	-) [ "$2" = 0 ] || return 1 ;;
	'') return 1 ;;
	*[!A-Za-z0-9._-]*) return 1 ;;
	*) is_uint "$2" && [ "$2" -ge 1 ] || return 1 ;;
	esac
	return 0
}

write_guard() { # [<bootId> <opSeq>] — records this boot's admitted high-water
	ensure_state
	tmp=$STATE_DIR/.tmp.guard.$$
	{
		printf 'version\t%s\n' "$PROTOCOL"
		printf 'guardEpoch\t%s\n' "$GUARD_EPOCH"
		printf 'epochBootId\t%s\n' "$EPOCH_BOOT"
		printf 'epochOpSeq\t%s\n' "$EPOCH_SEQ"
		printf 'supersededBootId\t%s\n' "$SUP_BOOT"
		printf 'supersededOpSeq\t%s\n' "$SUP_SEQ"
		printf 'fenceBootId\t%s\n' "$FENCE_BOOT"
		printf 'fenceOpSeq\t%s\n' "$FENCE_SEQ"
		printf 'fenceSupersededBootId\t%s\n' "$FENCE_SUP_BOOT"
		printf 'fenceSupersededOpSeq\t%s\n' "$FENCE_SUP_SEQ"
		printf 'fenceGuardEpoch\t%s\n' "$FENCE_GUARD"
		boot_records "${1:-}" "${2:-}"
	} >"$tmp" || refuse io-error "cannot write the guard file" 69
	chmod 600 "$tmp" 2>/dev/null || true
	sync_path "$tmp"
	mv "$tmp" "$GUARD_FILE" || {
		rm -f "$tmp"
		refuse io-error "cannot replace the guard file" 69
	}
	sync_path "$STATE_DIR"
	# Refresh the in-memory snapshot: a caller's own report (a takeover's
	# status) must carry the record this write just landed.
	GUARD_SNAPSHOT=$(cat "$GUARD_FILE" 2>/dev/null || true)
}

# boot_records re-emits every durable per-boot high-water record, replacing the
# one for the boot a takeover is admitting. A boot's high-water is the highest
# op sequence the guard has ever admitted from it, so an epoch the guard already
# saw can never take over again even after later boots have settled.
boot_records() { # [<bootId> <opSeq>]
	printf '%s\n' "${GUARD_SNAPSHOT:-}" | awk -F"$TAB" -v boot="${1:-}" -v seq="${2:-}" '
		$1 ~ /^boot\./ {
			if (boot != "" && $1 == "boot." boot) { next }
			print $1 "\t" $2
			next
		}
		END { if (boot != "") { print "boot." boot "\t" seq } }'
}

# --- lease holder and entries ------------------------------------------------

read_holder() { # sets HOLDER_BOOT and HOLDER_SEQ
	HOLDER_BOOT=-
	HOLDER_SEQ=0
	[ -f "$HOLDER_FILE" ] || return 0
	# One snapshot, split on the single space the writer emits: a file with no
	# space, more than one space-separated token, or a trailing space is not a
	# holder the writer produced and refuses as corrupt rather than tripping
	# the shell's unset-variable handling.
	line=$(cat "$HOLDER_FILE" 2>/dev/null) || return 1
	case $line in
	'') return 1 ;;
	*" "*) ;;
	*) return 1 ;;
	esac
	HOLDER_BOOT=${line%% *}
	HOLDER_SEQ=${line#* }
	case $HOLDER_BOOT in
	-) HOLDER_SEQ=0 ;;
	*[!A-Za-z0-9._-]*) return 1 ;;
	*) is_uint "$HOLDER_SEQ" && [ "$HOLDER_SEQ" -ge 1 ] || return 1 ;;
	esac
	return 0
}

write_holder() { # <bootId> <opSeq>
	ensure_state
	tmp=$LEASE_DIR/.tmp.holder.$$
	printf '%s %s\n' "$1" "$2" >"$tmp" || refuse io-error "cannot write the lease holder" 69
	chmod 600 "$tmp" 2>/dev/null || true
	sync_path "$tmp"
	mv "$tmp" "$HOLDER_FILE" || {
		rm -f "$tmp"
		refuse io-error "cannot replace the lease holder" 69
	}
	sync_path "$LEASE_DIR"
}

mint_nonce() {
	nonce=$(od -An -N16 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n' || true)
	case $nonce in
	'' | *[!0-9a-f]*) nonce=$(printf '%s%s' "$$" "$(date +%s 2>/dev/null || printf 0)") ;;
	*) ;;
	esac
	printf '%s' "$nonce"
}

# entry_field and entry_file_valid parse one snapshot of an entry file, taken
# by load_entry with a single cat, for the same reason as the guard snapshot:
# the wrapper replaces an entry atomically (registering -> running -> exited),
# and a field-by-field read across opens could mix two versions.
entry_field() { # <snapshot> <key>
	printf '%s\n' "$1" | awk -F"$TAB" -v key="$2" '$1 == key { print $2; exit }'
}

entry_file_valid() { # <snapshot>
	printf '%s\n' "$1" | awk -F"$TAB" '
		BEGIN {
			split("id command registeredAt state ownershipKind pid pidStartTime nonce cgroupId exit exitedAt descendants", keys, " ")
			for (i in keys) { allowed[keys[i]] = 1 }
		}
		NF != 2 { bad = 1 }
		{ seen[$1]++ }
		END {
			for (key in seen) {
				if (seen[key] != 1) { bad = 1 }
				if (!(key in allowed)) { bad = 1 }
			}
			for (i in keys) { if (!(keys[i] in seen)) { bad = 1 } }
			exit bad ? 1 : 0
		}' >/dev/null 2>&1
}

# write_entry persists one entry atomically. The signature is the field set the
# entry schema requires; empty values are written as empty fields.
write_entry() { # id command registeredAt state kind pid start nonce cgroupId exit exitedAt descendants
	ensure_state
	id=$1
	# The temp lives outside the enumerated lease directory so no reader can
	# ever glob a half-written record; the rename into place stays atomic.
	tmp=$STATE_DIR/.tmp.entry.$id.$$
	{
		printf 'id\t%s\n' "$1"
		printf 'command\t%s\n' "$2"
		printf 'registeredAt\t%s\n' "$3"
		printf 'state\t%s\n' "$4"
		printf 'ownershipKind\t%s\n' "$5"
		printf 'pid\t%s\n' "$6"
		printf 'pidStartTime\t%s\n' "$7"
		printf 'nonce\t%s\n' "$8"
		printf 'cgroupId\t%s\n' "$9"
		printf 'exit\t%s\n' "${10}"
		printf 'exitedAt\t%s\n' "${11}"
		printf 'descendants\t%s\n' "${12}"
	} >"$tmp" 2>/dev/null || {
		rm -f "$tmp"
		return 1
	}
	chmod 600 "$tmp" 2>/dev/null || true
	sync_path "$tmp"
	mv "$tmp" "$LEASE_DIR/$id" 2>/dev/null || {
		rm -f "$tmp"
		return 1
	}
	sync_path "$LEASE_DIR"
	return 0
}

entry_ownership_json() { # kind pid start nonce cgroup
	case $1 in
	pid) printf '{"pid":%s,"pidStartTime":"%s"}' "$2" "$(json_escape "$3")" ;;
	nonce) printf '{"nonce":"%s"}' "$(json_escape "$4")" ;;
	cgroup) printf '{"cgroupId":"%s"}' "$(json_escape "$5")" ;;
	*) refuse_corrupt "a lease entry carries ownership kind $1" ;;
	esac
}

# load_entry validates one entry file into ENTRY_* globals.
load_entry() {
	path=$1
	ENTRY_SNAPSHOT=$(cat "$path" 2>/dev/null) || return 1
	entry_file_valid "$ENTRY_SNAPSHOT" || return 1
	ENTRY_ID=$(entry_field "$ENTRY_SNAPSHOT" id)
	ENTRY_COMMAND=$(entry_field "$ENTRY_SNAPSHOT" command)
	ENTRY_REGISTERED=$(entry_field "$ENTRY_SNAPSHOT" registeredAt)
	ENTRY_STATE=$(entry_field "$ENTRY_SNAPSHOT" state)
	ENTRY_KIND=$(entry_field "$ENTRY_SNAPSHOT" ownershipKind)
	ENTRY_PID=$(entry_field "$ENTRY_SNAPSHOT" pid)
	ENTRY_START=$(entry_field "$ENTRY_SNAPSHOT" pidStartTime)
	ENTRY_NONCE=$(entry_field "$ENTRY_SNAPSHOT" nonce)
	ENTRY_CGROUP=$(entry_field "$ENTRY_SNAPSHOT" cgroupId)
	ENTRY_EXIT=$(entry_field "$ENTRY_SNAPSHOT" exit)
	ENTRY_EXITED=$(entry_field "$ENTRY_SNAPSHOT" exitedAt)
	ENTRY_DESCENDANTS=$(entry_field "$ENTRY_SNAPSHOT" descendants)
	case $ENTRY_ID in '' | *[!A-Za-z0-9]*) return 1 ;; esac
	[ "$ENTRY_ID" = "$(basename "$path")" ] || return 1
	case $ENTRY_COMMAND in '') return 1 ;; *) ;; esac
	case $ENTRY_REGISTERED in '') return 1 ;; *) ;; esac
	case $ENTRY_STATE in
	registering | running)
		[ -z "$ENTRY_EXIT" ] && [ -z "$ENTRY_EXITED" ] || return 1
		;;
	exited | killed)
		# A terminal entry carries its numeric exit and no descendants: the
		# command's own children must be gone before the entry reads settled.
		is_uint "$ENTRY_EXIT" || return 1
		[ -z "$ENTRY_DESCENDANTS" ] || return 1
		;;
	*) return 1 ;;
	esac
	for descendant in $ENTRY_DESCENDANTS; do
		is_uint "$descendant" && [ "$descendant" -ge 1 ] || return 1
	done
	case $ENTRY_KIND in
	pid)
		is_uint "$ENTRY_PID" || return 1
		[ "$ENTRY_PID" -ge 1 ] || return 1
		[ -n "$ENTRY_START" ] || return 1
		[ -z "$ENTRY_NONCE" ] && [ -z "$ENTRY_CGROUP" ] || return 1
		;;
	nonce)
		[ -n "$ENTRY_NONCE" ] || return 1
		[ -z "$ENTRY_CGROUP" ] || return 1
		[ -z "$ENTRY_START" ] || return 1
		;;
	cgroup)
		[ -n "$ENTRY_CGROUP" ] || return 1
		[ -z "$ENTRY_NONCE" ] && [ -z "$ENTRY_START" ] || return 1
		;;
	*) return 1 ;;
	esac
	return 0
}

count_entries() {
	count=0
	for path in "$LEASE_DIR"/*; do
		[ -f "$path" ] || continue
		case $(basename "$path") in
		holder | .tmp.*) continue ;;
		esac
		count=$((count + 1))
	done
	printf '%s' "$count"
}

emit_entries() {
	printf '{"version":%s,"entries":[' "$PROTOCOL"
	first=1
	for path in "$LEASE_DIR"/*; do
		[ -f "$path" ] || continue
		case $(basename "$path") in
		holder | .tmp.*) continue ;;
		esac
		load_entry "$path" || refuse_corrupt "lease entry $(basename "$path") is outside its schema"
		[ "$first" -eq 1 ] || printf ','
		first=0
		printf '{"id":"%s","command":"%s","registeredAt":"%s","ownership":%s,"state":"%s"' \
			"$ENTRY_ID" "$ENTRY_COMMAND" "$(json_escape "$ENTRY_REGISTERED")" \
			"$(entry_ownership_json "$ENTRY_KIND" "$ENTRY_PID" "$ENTRY_START" "$ENTRY_NONCE" "$ENTRY_CGROUP")" "$ENTRY_STATE"
		case $ENTRY_STATE in
		exited | killed)
			printf ',"exit":%s,"exitedAt":"%s"' "$ENTRY_EXIT" "$(json_escape "$ENTRY_EXITED")"
			;;
		*) ;;
		esac
		if [ -n "$ENTRY_DESCENDANTS" ]; then
			printf ',"descendants":['
			first_descendant=1
			for descendant in $ENTRY_DESCENDANTS; do
				[ "$first_descendant" -eq 1 ] || printf ','
				first_descendant=0
				printf '%s' "$descendant"
			done
			printf ']'
		fi
		printf '}'
	done
	printf ']}\n'
}

# --- status ------------------------------------------------------------------

epoch_json() { # <bootId> <opSeq>
	if [ "$1" = "-" ]; then
		printf 'null'
	else
		printf '{"bootId":"%s","opSeq":%s}' "$1" "$2"
	fi
}

fence_json() {
	if [ "$FENCE_BOOT" = "-" ]; then
		printf 'null'
	else
		printf '{"epoch":%s,"superseded":%s,"guardEpoch":%s}' \
			"$(epoch_json "$FENCE_BOOT" "$FENCE_SEQ")" \
			"$(epoch_json "$FENCE_SUP_BOOT" "$FENCE_SUP_SEQ")" "$FENCE_GUARD"
	fi
}

boot_high_water_json() {
	printf '%s\n' "${GUARD_SNAPSHOT:-}" | awk -F"$TAB" '
		BEGIN { printf "{"; first = 1 }
		$1 ~ /^boot\./ {
			if (!first) { printf "," }
			first = 0
			printf "\"%s\":%s", substr($1, 6), $2
		}
		END { printf "}" }'
}

emit_status() {
	read_holder || refuse_corrupt "the lease holder is outside its schema"
	printf '{"version":%s,"guardEpoch":%s,"epoch":%s,"fence":%s,"superseded":%s,"holder":%s,"entries":%s,"bootHighWater":%s}\n' \
		"$PROTOCOL" "$GUARD_EPOCH" "$(epoch_json "$EPOCH_BOOT" "$EPOCH_SEQ")" \
		"$(fence_json)" "$(epoch_json "$SUP_BOOT" "$SUP_SEQ")" \
		"$(epoch_json "$HOLDER_BOOT" "$HOLDER_SEQ")" "$(count_entries)" "$(boot_high_water_json)"
}

load_guard_or_refuse() {
	read_guard || refuse_corrupt "the guard file is outside its schema"
}

# --- epoch arguments ---------------------------------------------------------

parse_epoch() { # <bootId> <opSeq>
	E_BOOT=$1
	E_SEQ=$2
	case $E_BOOT in
	'' | - | *[!A-Za-z0-9._-]*) return 1 ;;
	*) ;;
	esac
	[ "${#E_BOOT}" -le 128 ] || return 1
	is_uint "$E_SEQ" || return 1
	[ "$E_SEQ" -ge 1 ] || return 1
	return 0
}

# --- operations --------------------------------------------------------------

# repair_holder reconciles an incomplete takeover: when the fence is already
# durable but the holder write was lost to a crash, the retry must repair it
# before reporting success.
repair_holder() { # <bootId> <opSeq>
	read_holder || refuse_corrupt "the lease holder is outside its schema"
	if [ "$HOLDER_BOOT" != "$1" ] || [ "$HOLDER_SEQ" != "$2" ]; then
		write_holder "$1" "$2"
	fi
}

do_takeover() {
	load_guard_or_refuse
	if [ "$SUP_BOOT" = "$E_BOOT" ] && [ "$SUP_SEQ" = "$E_SEQ" ]; then
		# An epoch the guard already superseded is stale whatever else is
		# pending: it must never be reinstalled, so this refuses before the
		# pending-fence answer below.
		refuse_stale "epoch $E_BOOT/$E_SEQ was superseded by the guard"
	fi
	if [ "$FENCE_BOOT" != "-" ]; then
		if [ "$FENCE_BOOT" = "$E_BOOT" ] && [ "$FENCE_SEQ" = "$E_SEQ" ]; then
			repair_holder "$E_BOOT" "$E_SEQ"
			emit_status
			return 0
		fi
		refuse_fenced "a fence for epoch $FENCE_BOOT/$FENCE_SEQ is still pending"
	fi
	if [ "$EPOCH_BOOT" = "$E_BOOT" ] && [ "$EPOCH_SEQ" = "$E_SEQ" ]; then
		repair_holder "$E_BOOT" "$E_SEQ"
		emit_status
		return 0
	fi
	highwater=$(guard_field "$GUARD_SNAPSHOT" "boot.$E_BOOT")
	if [ -n "$highwater" ] && is_uint "$highwater" && [ "$E_SEQ" -le "$highwater" ]; then
		# The durable per-boot high-water: an epoch this boot already admitted
		# never takes over again, even after later boots have settled.
		refuse_stale "epoch $E_BOOT/$E_SEQ is at or below boot $E_BOOT's high-water $highwater"
	fi
	read_holder || refuse_corrupt "the lease holder is outside its schema"
	previous_boot=$HOLDER_BOOT
	previous_seq=$HOLDER_SEQ
	if [ "$previous_boot" = "-" ]; then
		previous_boot=$EPOCH_BOOT
		previous_seq=$EPOCH_SEQ
	fi
	if [ "$previous_boot" != "-" ] && [ "$previous_boot" = "$E_BOOT" ] && [ "$E_SEQ" -le "$previous_seq" ]; then
		refuse_stale "epoch $E_BOOT/$E_SEQ is no newer than the guard's holder $previous_boot/$previous_seq"
	fi
	GUARD_EPOCH=$((GUARD_EPOCH + 1))
	FENCE_BOOT=$E_BOOT
	FENCE_SEQ=$E_SEQ
	FENCE_SUP_BOOT=$previous_boot
	FENCE_SUP_SEQ=$previous_seq
	FENCE_GUARD=$GUARD_EPOCH
	if [ "$previous_boot" != "-" ]; then
		SUP_BOOT=$previous_boot
		SUP_SEQ=$previous_seq
	fi
	write_guard "$E_BOOT" "$E_SEQ"
	if [ "${EVENER_FENCE_FAULT_AFTER_GUARD:-0}" = 1 ]; then
		# Test-only fault injection: die between the guard write and the holder
		# write, modelling the crash window the replay reconciliation repairs.
		exit 70
	fi
	write_holder "$E_BOOT" "$E_SEQ"
	emit_status
}

do_advance() {
	load_guard_or_refuse
	if [ "$FENCE_BOOT" = "-" ]; then
		if [ "$EPOCH_BOOT" = "$E_BOOT" ] && [ "$EPOCH_SEQ" = "$E_SEQ" ]; then
			emit_status
			return 0
		fi
		refuse_stale "epoch $E_BOOT/$E_SEQ is not the guard's $EPOCH_BOOT/$EPOCH_SEQ"
	fi
	if [ "$FENCE_BOOT" != "$E_BOOT" ] || [ "$FENCE_SEQ" != "$E_SEQ" ]; then
		refuse_stale "the fence names $FENCE_BOOT/$FENCE_SEQ, not $E_BOOT/$E_SEQ"
	fi
	EPOCH_BOOT=$E_BOOT
	EPOCH_SEQ=$E_SEQ
	GUARD_EPOCH=$((GUARD_EPOCH + 1))
	SUP_BOOT=$FENCE_SUP_BOOT
	SUP_SEQ=$FENCE_SUP_SEQ
	FENCE_BOOT=-
	FENCE_SEQ=0
	FENCE_SUP_BOOT=-
	FENCE_SUP_SEQ=0
	FENCE_GUARD=0
	write_guard
	emit_status
}

pid_start_time() { # <pid>
	pid=$1
	if [ -r "/proc/$pid/stat" ]; then
		start=$(awk '{ sub(/^[^)]*\) /, ""); print $20 }' "/proc/$pid/stat" 2>/dev/null || true)
		case $start in
		'' | *[!0-9]*) ;;
		*)
			printf '%s' "$start"
			return 0
			;;
		esac
	fi
	start=$(ps -o lstart= -p "$pid" 2>/dev/null | tr -d '\n\r' || true)
	case $start in
	'') return 1 ;;
	*)
		printf '%s' "$start"
		;;
	esac
}

# current_start_token reads a live process's kernel-owned start token for
# verification. EVENER_FENCE_FAULT_UNREADABLE_START models a platform that cannot
# read it, so the fail-closed arm is exercised.
current_start_token() { # <pid>
	if [ "${EVENER_FENCE_FAULT_UNREADABLE_START:-0}" = 1 ]; then
		return 1
	fi
	pid_start_time "$1"
}

# descendants_of prints the PIDs still carrying the command's per-spawn nonce.
# A command's children inherit the nonce in their environment, so a survivor —
# however it detached — is found and the entry never reads settled while it
# lives. A platform without /proc cannot enumerate; the entry then records no
# descendants and the pid/start-time identity remains the whole proof.
descendants_of() { # <nonce>
	[ -d /proc/self ] || return 0
	# One grep pass decides whether any survivor exists at all; only then is the
	# per-pid pass worth its forks. The environ file is NUL-separated, so the
	# pattern needs no trailing NUL to match. The pass reads grep's output, never
	# its exit status: unreadable environ files make grep exit 2 even after a
	# match, and a nonzero status must never read as "no survivors".
	candidates=$(grep -als "EVENER_FENCE_NONCE=$1" /proc/[0-9]*/environ 2>/dev/null || true)
	[ -n "$candidates" ] || return 0
	for envfile in /proc/[0-9]*/environ; do
		[ -r "$envfile" ] || continue
		pid=${envfile#/proc/}
		pid=${pid%/environ}
		case $pid in '' | *[!0-9]*) continue ;; esac
		[ "$pid" = "$$" ] && continue
		if [ -n "$(grep -al "EVENER_FENCE_NONCE=$1" "$envfile" 2>/dev/null || true)" ]; then
			printf '%s ' "$pid"
		fi
	done
}

do_perform() { # <bootId> <opSeq> <command>
	load_guard_or_refuse
	if [ "$FENCE_BOOT" != "-" ]; then
		refuse_fenced "a fence for epoch $FENCE_BOOT/$FENCE_SEQ is pending; no mutating step runs before the guard advance"
	fi
	if [ "$EPOCH_BOOT" != "$1" ] || [ "$EPOCH_SEQ" != "$2" ]; then
		refuse_stale "epoch $1/$2 no longer equals the guard's $EPOCH_BOOT/$EPOCH_SEQ"
	fi
	command=$3
	nonce=$(mint_nonce)
	registered=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf 0)
	# Register before the side effects start: the entry exists while the command
	# runs, and a crash between registration and spawn leaves it registered (fail
	# closed) rather than invisible.
	write_entry "$nonce" "$(json_escape "$command")" "$registered" registering nonce '' '' "$nonce" '' '' '' '' ||
		refuse io-error "cannot register the lease entry" 69
	# The child sees the nonce (so its descendants can be found) but never the
	# invocation's refusal token.
	EVENER_FENCE_NONCE=$nonce EVENER_FENCE_STATE=$STATE_DIR EVENER_FENCE_TOKEN= sh -c "$command" &
	child=$!
	start=$(pid_start_time "$child" || true)
	ownership=${EVENER_FENCE_OWNERSHIP:-pid}
	# One ownership identity per entry: the kernel-owned (pid, start time) pair
	# when this platform exposes it, else the per-spawn nonce — never both.
	own_kind=nonce
	own_pid=''
	own_start=''
	own_nonce=$nonce
	if [ "$ownership" = pid ] && [ -n "$start" ]; then
		own_kind=pid
		own_pid=$child
		own_start=$start
		own_nonce=''
	fi
	if [ "${EVENER_FENCE_FAULT_AFTER_SPAWN:-0}" = 1 ]; then
		post_spawn_failure "cannot record the lease entry (injected fault)"
	fi
	write_entry "$nonce" "$(json_escape "$command")" "$registered" running "$own_kind" "$own_pid" "$own_start" "$own_nonce" '' '' '' '' ||
		post_spawn_failure "cannot record the lease entry"
	status=0
	wait "$child" || status=$?
	exited=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf 0)
	survivors=$(descendants_of "$nonce")
	if [ -n "$survivors" ]; then
		# The command's own children outlive it: the entry stays running with
		# them recorded, so a verifier reads it live and a fencing takeover
		# treats the surviving work as the superseded epoch's, never as clean.
		write_entry "$nonce" "$(json_escape "$command")" "$registered" running "$own_kind" "$own_pid" "$own_start" "$own_nonce" '' '' '' "$survivors" ||
			refuse io-error "cannot record the lease descendants" 69
		exit "$status"
	fi
	# A command that exited, however it exited, is recorded exited: the lease
	# file's exit state is what a verifier enumerates. A killed orphan is marked
	# by the fencing worker's kill path (S18) through the same entry file.
	write_entry "$nonce" "$(json_escape "$command")" "$registered" exited "$own_kind" "$own_pid" "$own_start" "$own_nonce" '' "$status" "$exited" '' ||
		refuse io-error "cannot record the lease exit" 69
	exit "$status"
}

# post_spawn_failure kills the command the wrapper just started, reaps it, and
# refuses: a tracking write that fails after the spawn must never leave a
# side-effect process running untracked.
post_spawn_failure() {
	kill "$child" 2>/dev/null || true
	wait "$child" 2>/dev/null || true
	refuse io-error "$1" 69
}

do_recheck() { # <id>
	id=$1
	case $id in
	'' | *[!A-Za-z0-9]*) refuse_malformed "a lease entry id is required" ;;
	*) ;;
	esac
	path=$LEASE_DIR/$id
	if [ ! -f "$path" ]; then
		printf '{"version":%s,"id":"%s","live":false,"state":"","ownership":{"nonce":"%s"}}\n' "$PROTOCOL" "$id" "$id"
		return 0
	fi
	load_entry "$path" || refuse_corrupt "lease entry $id is outside its schema"
	live=false
	case $ENTRY_STATE in
	running | registering)
		case $ENTRY_KIND in
		pid)
			if kill -0 "$ENTRY_PID" 2>/dev/null; then
				current=$(current_start_token "$ENTRY_PID" || true)
				if [ -z "$current" ]; then
					# The process is there but its identity cannot be read: live,
					# never clean.
					live=true
				elif [ "$current" = "$ENTRY_START" ]; then
					# The kernel-owned start time proves this instance, mirroring
					# the local boundary-plus-nonce rule: a reused pid names a
					# different process and reads as already clean.
					live=true
				fi
			elif ps -p "$ENTRY_PID" >/dev/null 2>&1; then
				# Present but not signalable: the identity cannot be proven.
				live=true
			fi
			;;
		nonce)
			# A registered nonce entry whose liveness cannot be disproven stays
			# live: fail closed, never a clean read.
			live=true
			;;
		cgroup)
			live=true
			;;
		esac
		for descendant in $ENTRY_DESCENDANTS; do
			# A recorded descendant that is still signalable keeps the entry
			# live; one demonstrably gone no longer does.
			if kill -0 "$descendant" 2>/dev/null; then
				live=true
				break
			fi
		done
		;;
	exited | killed) live=false ;;
	esac
	printf '{"version":%s,"id":"%s","live":%s,"state":"%s","ownership":%s' \
		"$PROTOCOL" "$id" "$live" "$ENTRY_STATE" \
		"$(entry_ownership_json "$ENTRY_KIND" "$ENTRY_PID" "$ENTRY_START" "$ENTRY_NONCE" "$ENTRY_CGROUP")"
	if [ -n "$ENTRY_DESCENDANTS" ]; then
		printf ',"descendants":['
		first_descendant=1
		for descendant in $ENTRY_DESCENDANTS; do
			[ "$first_descendant" -eq 1 ] || printf ','
			first_descendant=0
			printf '%s' "$descendant"
		done
		printf ']'
	fi
	printf '}\n'
}

# --- dispatch ----------------------------------------------------------------

op=${1:-}
[ -n "$op" ] || refuse_malformed "no operation given"
shift

case $op in
version)
	[ $# -eq 0 ] || refuse_malformed "version takes no arguments"
	printf '%s\n' "$VERSION"
	;;
status)
	[ $# -eq 0 ] || refuse_malformed "status takes no arguments"
	load_guard_or_refuse
	emit_status
	;;
entries)
	[ $# -eq 0 ] || refuse_malformed "entries takes no arguments"
	emit_entries
	;;
takeover)
	[ $# -eq 2 ] || refuse_malformed "takeover needs an epoch"
	parse_epoch "$1" "$2" || refuse_malformed "malformed fencing epoch"
	acquire_lock || refuse busy "the remote lease is held" 75
	trap 'release_lock' EXIT HUP INT TERM
	do_takeover
	;;
advance)
	[ $# -eq 2 ] || refuse_malformed "advance needs an epoch"
	parse_epoch "$1" "$2" || refuse_malformed "malformed fencing epoch"
	acquire_lock || refuse busy "the remote lease is held" 75
	trap 'release_lock' EXIT HUP INT TERM
	do_advance
	;;
perform)
	[ $# -eq 3 ] || refuse_malformed "perform needs an epoch and a command"
	parse_epoch "$1" "$2" || refuse_malformed "malformed fencing epoch"
	[ -n "$3" ] || refuse_malformed "perform needs a command"
	acquire_lock || refuse busy "the remote lease is held" 75
	trap 'release_lock' EXIT HUP INT TERM
	do_perform "$1" "$2" "$3"
	;;
recheck)
	[ $# -eq 1 ] || refuse_malformed "recheck needs a lease entry id"
	do_recheck "$1"
	;;
*)
	refuse_malformed "unknown operation $op"
	;;
esac
