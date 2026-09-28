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
#   kill <bootId> <opSeq> <id>    §4's remote kill of one superseded-epoch lease
#                                 entry: under this epoch's taken-over lease,
#                                 revalidate the entry's stored ownership
#                                 identity on the remote, signal the verified
#                                 instance, and mark the entry killed through the
#                                 same entry file once nothing matching that
#                                 identity is left. A process whose identity
#                                 cannot be verified is never signaled: the
#                                 fencing worker's bounded wait decides.
#
# status, entries, and recheck are read-only and take no lock: a verifier must
# be able to enumerate and re-present identities while a command holds the
# exclusive lease, so they read one cat snapshot of each file and never block
# behind a running command. Only takeover, advance, perform, and kill take the
# lock.
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
# marker belong to the fencing worker (S18), which enumerates the superseded
# epoch's work through `entries` and `recheck` and signals only through `kill`,
# where the stored ownership identity is revalidated remotely before any signal
# and the kill is recorded through the same entry file.
#
# Four environment seams exist for the tests' fault injection, mirroring the Go
# stores' fault seams: EVENER_FENCE_FAULT_AFTER_GUARD=1 exits right after a
# takeover's guard write (before the holder write, the crash window the replay
# reconciliation repairs), EVENER_FENCE_FAULT_AFTER_SPAWN=1 fails the post-spawn
# entry write so the kill-on-tracking-failure path is exercised, and
# EVENER_FENCE_FAULT_UNREADABLE_START=1 hides a live process's start token so
# the fail-closed recheck arm is exercised, and
# EVENER_FENCE_FAULT_UNREADABLE_CANDIDATE=1 makes a nonce candidate
# uninspectable so the fail-closed enumeration arm is exercised. None is set in
# production.
#
# EVENER_FENCE_TOKEN, when set, is echoed in every refusal: the caller mints it
# per invocation and clears it for the wrapped command, so a command's own
# stderr cannot accidentally pass for a wrapper refusal. A same-uid command can
# still read its parent's environment on platforms that expose it, so this is a
# confusion barrier, not a cryptographic boundary against a hostile command. EVENER_FENCE_LOCK_ATTEMPTS bounds
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

# is_canonical_uint additionally refuses a leading zero: a value like 001 is
# stored and emitted verbatim into JSON, where 001 is not a number, so the
# controller would read the state as corrupt.
is_canonical_uint() {
	case $1 in
	'' | *[!0-9]* | 0[0-9]*) return 1 ;;
	*) return 0 ;;
	esac
}

# is_uint64 is the schema bound the Go decoders apply: a value the helper
# persists is unmarshalled into a uint64, so anything above the maximum would
# wedge every later status/takeover/advance controller-side. A decimal string
# is bounded by length, then lexically against the maximum when it is 20 digits.
is_uint64() {
	is_canonical_uint "$1" || return 1
	[ "${#1}" -le 20 ] || return 1
	if [ "${#1}" -eq 20 ] && [ "$1" \> "18446744073709551615" ]; then
		return 1
	fi
	return 0
}

# seq_at_or_below reports whether canonical uint <a> is at or below canonical
# uint <b>. Both callers hold is_uint64-bounded values, which may exceed the
# shell's integer width, so the comparison is by length then lexically — the
# same bound is_uint64 applies. `[ "$a" -le "$b" ]` errors on such a value
# (dash: "Illegal number"), and an errored test silently skips the refusal it
# guards, which is the stale-epoch bypass this shape closes.
seq_at_or_below() { # <a> <b>
	[ "${#1}" -lt "${#2}" ] && return 0
	[ "${#1}" -gt "${#2}" ] && return 1
	[ "$1" = "$2" ] || [ "$1" \< "$2" ]
}

# seq_exhausted reports whether a canonical uint is the schema maximum: the
# guard's sequence then has no next value, and incrementing it would wrap (or
# abort the shell) rather than write a guard the Go decoder accepts.
seq_exhausted() {
	[ "${#1}" -eq 20 ] && [ "$1" = "18446744073709551615" ]
}

# seq_increment prints the canonical uint one above <value>, by string
# arithmetic: the shell's signed integer width cannot represent the schema's
# full uint64 range, so `$((value + 1))` wraps or aborts for values above
# MaxInt64. <value> must be is_uint64-validated and below the schema maximum;
# seq_exhausted refuses that case before this is called.
seq_increment() {
	rest=$1
	out=''
	carry=1
	while [ -n "$rest" ]; do
		prefix=${rest%?}
		digit=${rest#"$prefix"}
		rest=$prefix
		if [ "$carry" -eq 1 ]; then
			if [ "$digit" = 9 ]; then
				digit=0
			else
				digit=$((digit + 1))
				carry=0
			fi
		fi
		out=$digit$out
	done
	if [ "$carry" -eq 1 ]; then
		out=1$out
	fi
	printf '%s' "$out"
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
			steal_stale_lock "$observed" || true
			;;
		*)
			if ! kill -0 "$pid" 2>/dev/null; then
				# A failed steal (lost rename, replaced claim, live owner) is
				# not an error: the bounded loop decides the outcome, so its
				# nonzero return never aborts the helper under set -e.
				steal_stale_lock "$observed" || true
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

# steal_stale_lock takes <observed claim content> and removes the observed path
# only when it still names the claim the caller proved stale. It never unlinks
# or renames the path to inspect it: it adds one hard link, inspects the LINK
# (content equal to the observed claim AND the owner dead or unprovable), and
# removes the path only when the path and the link still name the same inode.
# A live replacement claim is therefore never detached from its path; the worst
# an unlucky interleaving can do is drop the extra link.
#
# Residual, stated explicitly: the inode comparison and the rm are two steps,
# so a replacement published in the (single fork-sized) window between them
# could still be removed. Closing that window needs a kernel lock (flock) that
# no POSIX shell guarantees; this shape never *moves* a live claim, which is
# the failure the lease cannot survive.
steal_stale_lock() {
	stale=$STATE_DIR/.tmp.stale.$$
	rm -f "$stale" 2>/dev/null || true
	ln "$LOCK_FILE" "$stale" 2>/dev/null || return 1
	link_ino=$(ls -di "$stale" 2>/dev/null | awk '{print $1}')
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
	if [ "$moved" != "$1" ] || [ "$provably_dead" != true ]; then
		# Not the claim we observed, or its owner is alive: drop our extra link
		# only, and leave the live path exactly as it is.
		rm -f "$stale" 2>/dev/null || true
		return 1
	fi
	if [ "${EVENER_FENCE_FAULT_STEAL_PARK:-0}" = 1 ]; then
		# Test-only fault injection: park between proving the claim stale and
		# removing it, so a test can publish a replacement in that window.
		: >"$STATE_DIR/.tmp.steal.parked" 2>/dev/null || true
		sleep 1
	fi
	now_ino=$(ls -di "$LOCK_FILE" 2>/dev/null | awk '{print $1}')
	if [ -n "$link_ino" ] && [ "$link_ino" = "$now_ino" ]; then
		# The path still names the stale claim we hold the link to: remove it.
		rm -f "$LOCK_FILE" 2>/dev/null || true
		rm -f "$stale" 2>/dev/null || true
		return 0
	fi
	# A replacement claim took the path: leave it alone and drop only our link.
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
			if ($1 !~ /^boot\.[A-Za-z0-9._-]+$/ || $1 == "boot.-" || $2 !~ /^[1-9][0-9]*$/) { bad = 1 }
			else if (length($2) > 20 || (length($2) == 20 && ($2 "") > ("18446744073709551615" ""))) { bad = 1 }
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

# guard_boot_high_water_valid bounds every per-boot high-water to a uint64, the
# schema the Go decoder unmarshals it into. guard_file_valid's coarse check
# (digits, at most 20) lets a value above the maximum through, and such a value
# both wedges every later status/takeover/advance controller-side (the emitted
# JSON is not a uint64) and overflows the shell's own comparisons, silently
# skipping the stale-epoch refusal. is_uint64 is the exact bound the Go side
# applies, so an out-of-range value refuses as state-corrupt — never a bypass,
# and never a value this helper emits.
guard_boot_high_water_valid() { # <snapshot>
	printf '%s\n' "$1" | while IFS="$TAB" read -r key value; do
		case $key in
		boot.*) is_uint64 "$value" || exit 1 ;;
		esac
	done
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
	guard_boot_high_water_valid "$GUARD_SNAPSHOT" || return 1
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
	is_uint64 "$GUARD_EPOCH" || return 1
	is_uint64 "$FENCE_GUARD" || return 1
	check_boot_pair "$EPOCH_BOOT" "$EPOCH_SEQ" || return 1
	check_boot_pair "$SUP_BOOT" "$SUP_SEQ" || return 1
	check_boot_pair "$FENCE_BOOT" "$FENCE_SEQ" || return 1
	check_boot_pair "$FENCE_SUP_BOOT" "$FENCE_SUP_SEQ" || return 1
	if [ "$FENCE_BOOT" = "-" ]; then
		[ "$FENCE_GUARD" = 0 ] || return 1
	else
		# The fence's sequence sits inside the guard's own, exactly as the Go
		# validator decides: a fence ahead of the guard is corrupt. Both values
		# are is_uint64-bounded, so the compare is length-then-lexical: an
		# in-range value above the shell's integer width must not read corrupt.
		[ "$FENCE_GUARD" != 0 ] && seq_at_or_below "$FENCE_GUARD" "$GUARD_EPOCH" || return 1
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
	*) is_uint64 "$2" && [ "$2" != 0 ] || return 1 ;;
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
	'') return 1 ;;
	-) [ "$HOLDER_SEQ" = 0 ] || return 1 ;;
	*[!A-Za-z0-9._-]*) return 1 ;;
	*) is_uint64 "$HOLDER_SEQ" && [ "$HOLDER_SEQ" != 0 ] || return 1 ;;
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
	'' | *[!0-9a-f]*) nonce=$(printf '%016x%016x' "$$" "$(date +%s 2>/dev/null || printf 0)") ;;
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
		is_uint64 "$ENTRY_EXIT" || return 1
		[ -z "$ENTRY_DESCENDANTS" ] || return 1
		;;
	*) return 1 ;;
	esac
	for descendant in $ENTRY_DESCENDANTS; do
		case $descendant in
		*:*)
			descendant_pid=${descendant%%:*}
			descendant_start=${descendant#*:}
			is_uint64 "$descendant_pid" && [ "$descendant_pid" -ge 1 ] && [ -n "$descendant_start" ] || return 1
			;;
		*) return 1 ;;
		esac
	done
	case $ENTRY_KIND in
	pid)
		is_uint64 "$ENTRY_PID" || return 1
		[ "$ENTRY_PID" -ge 1 ] || return 1
		[ -n "$ENTRY_START" ] || return 1
		[ -z "$ENTRY_NONCE" ] && [ -z "$ENTRY_CGROUP" ] || return 1
		;;
	nonce)
		[ -n "$ENTRY_NONCE" ] || return 1
		[ -z "$ENTRY_PID" ] || return 1
		[ -z "$ENTRY_CGROUP" ] || return 1
		[ -z "$ENTRY_START" ] || return 1
		;;
	cgroup)
		[ -n "$ENTRY_CGROUP" ] || return 1
		[ -z "$ENTRY_PID" ] || return 1
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
				printf '{"pid":%s,"startToken":"%s"}' "${descendant%%:*}" "$(json_escape "${descendant#*:}")"
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
	is_uint64 "$E_SEQ" || return 1
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
		# takeover below can supersede anything.
		refuse_stale "epoch $E_BOOT/$E_SEQ was superseded by the guard"
	fi
	if [ "$FENCE_BOOT" = "$E_BOOT" ] && [ "$FENCE_SEQ" = "$E_SEQ" ]; then
		# A replay of this epoch's own pending fence repairs a lost holder write
		# and reports the same fence: the sequence never advances twice for one
		# fencing.
		repair_holder "$E_BOOT" "$E_SEQ"
		emit_status
		return 0
	fi
	# A pending fence for a DIFFERENT epoch does not block this takeover: the
	# new epoch supersedes the epoch that fence names — the crashed incarnation
	# whose work the new worker kills under this lease (§4:107's "The next
	# `deploy`/`restart` past the cleared marker runs its kill/wait plus guard
	# advance under a fresh epoch"). The superseded check above and the
	# holder/high-water checks below refuse an epoch the guard has already
	# retired, so a late orphan can never move the guard backward.
	if [ "$EPOCH_BOOT" = "$E_BOOT" ] && [ "$EPOCH_SEQ" = "$E_SEQ" ]; then
		repair_holder "$E_BOOT" "$E_SEQ"
		emit_status
		return 0
	fi
	highwater=$(guard_field "$GUARD_SNAPSHOT" "boot.$E_BOOT")
	if [ -n "$highwater" ] && seq_at_or_below "$E_SEQ" "$highwater"; then
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
	if [ "$previous_boot" != "-" ] && [ "$previous_boot" = "$E_BOOT" ] && seq_at_or_below "$E_SEQ" "$previous_seq"; then
		refuse_stale "epoch $E_BOOT/$E_SEQ is no newer than the guard's holder $previous_boot/$previous_seq"
	fi
	if seq_exhausted "$GUARD_EPOCH"; then
		# The guard's sequence has no next value: the takeover cannot advance
		# it, and wrapping would write a guard the Go decoder rejects.
		refuse_corrupt "the guard's fencing sequence is exhausted"
	fi
	GUARD_EPOCH=$(seq_increment "$GUARD_EPOCH")
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
	# The lease holder is part of the fencing state: a takeover whose holder
	# write was lost is repaired by its replay, never advanced past.
	read_holder || refuse_corrupt "the lease holder is outside its schema"
	if [ "$HOLDER_BOOT" != "$E_BOOT" ] || [ "$HOLDER_SEQ" != "$E_SEQ" ]; then
		refuse_corrupt "the lease holder $HOLDER_BOOT/$HOLDER_SEQ does not name the fenced epoch $E_BOOT/$E_SEQ; replay the takeover"
	fi
	if seq_exhausted "$GUARD_EPOCH"; then
		refuse_corrupt "the guard's fencing sequence is exhausted"
	fi
	EPOCH_BOOT=$E_BOOT
	EPOCH_SEQ=$E_SEQ
	GUARD_EPOCH=$(seq_increment "$GUARD_EPOCH")
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

# nonce_value prints the exact per-spawn nonce a process's environment carries
# (empty when it carries none), and returns nonzero when the environment cannot
# be read: an uninspectable environment is never a nonce mismatch, and its
# caller must read it as "cannot disprove".
nonce_value() { # <environ path>
	value=$(tr '\0' '\n' <"$1" 2>/dev/null) || return 1
	printf '%s\n' "$value" | sed -n 's/^EVENER_FENCE_NONCE=//p'
}

# nonce_holds reports whether one process's environment carries exactly this
# nonce: the value is extracted and compared whole, never substring-matched, so
# a process whose nonce merely starts with the searched value is not ours.
nonce_holds() { # <environ path> <nonce>
	value=$(nonce_value "$1") || return 1
	[ "$value" = "$2" ]
}

# descendants_of prints the PIDs still carrying the command's per-spawn nonce.
# A command's children inherit the nonce in their environment, so a survivor —
# however it detached — is found and the entry never reads settled while it
# lives. Its exit status is part of the contract: when the scan cannot run (no
# /proc, the helper's own environment unreadable, or grep missing) it returns
# nonzero, and callers must read that as "cannot disprove", never as "no
# survivors". On a platform where it cannot run, a running command records no
# descendants, and the fencing worker's nonce arms read the member live.
descendants_of() { # <nonce>
	nonce_scan_available || return 1
	# One substring grep decides whether any candidate exists at all; only then
	# is the per-pid pass worth its forks. A substring hit is a candidate, never
	# an answer: the extracted value is compared exactly, so a process whose
	# nonce merely starts with the searched one is not ours. The pass reads
	# grep's candidate list, never its exit status: unreadable environ files
	# make grep exit 2 even after a match, and a nonzero status must never read
	# as "no survivors". A candidate that can no longer be inspected returns
	# nonzero — the caller must read that as "cannot disprove", never as empty.
	candidates=$(grep -als "EVENER_FENCE_NONCE=$1" /proc/[0-9]*/environ 2>/dev/null || true)
	[ -n "$candidates" ] || return 0
	for envfile in $candidates; do
		if [ "${EVENER_FENCE_FAULT_UNREADABLE_CANDIDATE:-0}" = 1 ]; then
			# Test-only fault injection: models a candidate that cannot be
			# inspected at the read, so the fail-closed arm is exercised.
			return 2
		fi
		value=$(nonce_value "$envfile") || return 2
		[ "$value" = "$1" ] || continue
		pid=${envfile#/proc/}
		pid=${pid%/environ}
		case $pid in '' | *[!0-9]*) continue ;; esac
		[ "$pid" = "$$" ] && continue
		start=$(pid_start_time "$pid" 2>/dev/null || true)
		[ -n "$start" ] || start=unknown
		printf '%s:%s ' "$pid" "$start"
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
	read_holder || refuse_corrupt "the lease holder is outside its schema"
	if [ "$HOLDER_BOOT" != "$EPOCH_BOOT" ] || [ "$HOLDER_SEQ" != "$EPOCH_SEQ" ]; then
		refuse_corrupt "the lease holder $HOLDER_BOOT/$HOLDER_SEQ does not name the guard epoch $EPOCH_BOOT/$EPOCH_SEQ"
	fi
	command=$3
	nonce=$(mint_nonce)
	child_nonce=$nonce
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
	# The survivor enumeration is the proof the command's own children are gone,
	# and it is read strictly: a scan that cannot run proves nothing, so the
	# entry stays running rather than recording an exit that would exclude
	# surviving work from every later fencing's live-entry set. The command's
	# own exit status still rides this helper's exit code.
	if survivors=$(descendants_of "$nonce"); then
		if [ -n "$survivors" ]; then
			# The command's own children outlive it: the entry stays running with
			# them recorded, so a verifier reads it live and a fencing takeover
			# treats the surviving work as the superseded epoch's, never as clean.
			write_entry "$nonce" "$(json_escape "$command")" "$registered" running "$own_kind" "$own_pid" "$own_start" "$own_nonce" '' '' '' "$survivors" ||
				refuse io-error "cannot record the lease descendants" 69
			exit "$status"
		fi
		# A command that exited, however it exited, is recorded exited: the lease
		# file's exit state is what a verifier enumerates. A killed orphan is
		# marked by the fencing worker's kill path (S18) through the same entry
		# file.
		write_entry "$nonce" "$(json_escape "$command")" "$registered" exited "$own_kind" "$own_pid" "$own_start" "$own_nonce" '' "$status" "$exited" '' ||
			refuse io-error "cannot record the lease exit" 69
		exit "$status"
	fi
	write_entry "$nonce" "$(json_escape "$command")" "$registered" running "$own_kind" "$own_pid" "$own_start" "$own_nonce" '' '' '' '' ||
		refuse io-error "cannot record the unverified lease exit" 69
	exit "$status"
}

# post_spawn_failure kills the command the wrapper just started, reaps it, and
# refuses: a tracking write that fails after the spawn must never leave a
# side-effect process running untracked.
post_spawn_failure() {
	# Kill the command and every descendant still carrying its nonce, and give
	# them a bounded chance to leave, before refusing: a tracking failure must
	# not leave side-effect work running untracked.
	kill "$child" 2>/dev/null || true
	attempt=0
	while [ "$attempt" -lt 20 ]; do
		if remaining=$(descendants_of "${child_nonce:-}"); then
			[ -z "$remaining" ] && break
		else
			# The enumeration could not run: nothing is proven gone, so this
			# attempt keeps the loop's remaining window instead of reading the
			# failure as a clean reap.
			remaining=''
		fi
		for descendant in $remaining; do
			descendant_pid=${descendant%%:*}
			descendant_start=${descendant#*:}
			# Signal only an instance we can still identify: the nonce matched
			# at collection, and the start token must still match here.
			kill -0 "$descendant_pid" 2>/dev/null || continue
			current=$(current_start_token "$descendant_pid" || true)
			if [ -z "$current" ] || [ "$current" = "$descendant_start" ]; then
				kill "$descendant_pid" 2>/dev/null || true
			fi
		done
		attempt=$((attempt + 1))
		sleep 0.1
	done
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
			# The nonce is the entry's stored ownership identity; the carrier
			# scan below is its check.
			;;
		cgroup)
			live=true
			;;
		esac
		# The command's children inherit the wrapper's per-spawn nonce (the entry
		# id), and a running command's descendants are not recorded yet: while
		# any process carries the nonce exactly the member is live. An
		# enumeration that cannot run, or that fails, can never read clean.
		if nonce_scan_available; then
			if ! carriers=$(entry_nonce_scan); then
				live=true
			elif [ -n "$carriers" ]; then
				live=true
			fi
		else
			live=true
		fi
		for descendant in $ENTRY_DESCENDANTS; do
			# A recorded descendant counts only while it still carries this
			# invocation's exact nonce AND the kernel-owned start token recorded
			# beside it: a reused pid without both is not the wrapper's work and
			# must never be treated — or signaled — as it. Every path that
			# cannot disprove the identity reads live, never clean.
			descendant_pid=${descendant%%:*}
			descendant_start=${descendant#*:}
			kill -0 "$descendant_pid" 2>/dev/null || continue
			if [ "$descendant_start" = unknown ]; then
				# No start token was recorded: the identity cannot be disproven.
				live=true
				break
			fi
			if ! value=$(nonce_value "/proc/$descendant_pid/environ"); then
				# The environment cannot be read now: cannot be disproven.
				live=true
				break
			fi
			if [ "$value" != "$id" ]; then
				# The exact nonce is absent: this is not the wrapper's process.
				continue
			fi
			current=$(current_start_token "$descendant_pid" || true)
			if [ -z "$current" ] || [ "$current" = "$descendant_start" ]; then
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
			printf '{"pid":%s,"startToken":"%s"}' "${descendant%%:*}" "$(json_escape "${descendant#*:}")"
		done
		printf ']'
	fi
	printf '}\n'
}

# --- kill --------------------------------------------------------------------

# nonce_scan_available reports whether the nonce enumeration can run at all: it
# needs /proc with the wrapper's own environment readable, and grep. A scan that
# cannot run must never read as "no process carries the nonce" — callers fall
# back to the stored ownership identity (and, for a nonce-owned entry, to live).
nonce_scan_available() {
	[ -d /proc/self ] || return 1
	[ -r /proc/self/environ ] || return 1
	command -v grep >/dev/null 2>&1 || return 1
	# At least one numeric process directory must be visible: an unmatched glob
	# would hand the scan a literal path, and its failure must not read as an
	# empty (proven-clean) result.
	found=false
	for entry in /proc/[0-9]*; do
		if [ -e "$entry" ]; then
			found=true
			break
		fi
	done
	[ "$found" = true ] || return 1
	return 0
}

# entry_nonce_scan prints one "pid:startToken" token per process carrying one of
# the entry's nonces exactly. The wrapper mints the per-spawn nonce as the entry
# id; a stored nonce field names the same value on nonce-owned entries. Its exit
# status is nonzero when any scan could not run: the caller must then read the
# member live, never settled. The scan's per-file semantics are descendants_of's
# (an unreadable environment file is not this work; see its comment and
# TestScriptNonceMatchIsExact).
entry_nonce_scan() {
	status=0
	for nonce in "$ENTRY_ID" "$ENTRY_NONCE"; do
		[ -n "$nonce" ] || continue
		descendants_of "$nonce" || status=1
	done
	return "$status"
}

# emit_target prints one live target once. The identity is the (pid, token)
# pair, so the ownership arm's answer and the scan's answer for one process
# collapse to one target.
emit_target() { # <pid> <startToken>
	case $target_seen in
	*"|$1:$2|"*) return 0 ;;
	esac
	target_seen="$target_seen|$1:$2|"
	printf '%s:%s ' "$1" "$2"
}

# entry_live_targets prints one "pid:startToken" token per process still
# matching the entry's stored ownership identity. An empty result is the only
# proof the member is gone; a process whose identity cannot be read prints as
# "<pid>:unknown", which reads live and is never signaled. §9's verifier rules
# apply: a pid is checked against its stored start time, a nonce is
# re-presented to the wrapper, and a cgroup membership this helper cannot
# attest fails closed.
#
# The nonce scan runs for every kind, not only nonce-owned entries: a command's
# children inherit the wrapper's per-spawn nonce (the entry id), and a running
# command never has recorded descendants — do_perform records them after the
# command exits — so without this scan a signaled primary's surviving children
# would be invisible and the entry would mark settled over a live orphan.
entry_live_targets() { # (uses ENTRY_*)
	target_seen=''
	case $ENTRY_KIND in
	pid)
		if kill -0 "$ENTRY_PID" 2>/dev/null; then
			current=$(current_start_token "$ENTRY_PID" || true)
			if [ -z "$current" ]; then
				emit_target "$ENTRY_PID" unknown
			elif [ "$current" = "$ENTRY_START" ]; then
				emit_target "$ENTRY_PID" "$current"
			fi
		elif ps -p "$ENTRY_PID" >/dev/null 2>&1; then
			# Present but not signalable: the identity cannot be proven.
			emit_target "$ENTRY_PID" unknown
		fi
		;;
	nonce)
		;;
	cgroup)
		# This helper cannot attest a cgroup membership, so the member reads
		# live and is never signaled.
		emit_target '-' unknown
		;;
	esac
	if nonce_scan_available; then
		if ! carriers=$(entry_nonce_scan); then
			# The scan could not complete: liveness cannot be disproven.
			emit_target '-' unknown
		else
			for target in $carriers; do
				target_pid=${target%%:*}
				target_start=${target#*:}
				if [ "$target_start" = unknown ] || descendant_pair_conflicts "$target_pid" "$target_start"; then
					emit_target "$target_pid" unknown
				else
					emit_target "$target_pid" "$target_start"
				fi
			done
		fi
	else
		# A platform that cannot enumerate cannot disprove the member: an empty
		# result here would read settled over possibly-live children.
		emit_target '-' unknown
	fi
	# A recorded descendant counts only while it still carries this entry's
	# exact nonce (the entry id is the wrapper's per-spawn nonce) and its
	# recorded start token: a reused id is not the wrapper's work. This pass
	# also covers a descendant whose environment cannot be read (which reads
	# live), on platforms where the nonce scan cannot run at all.
	for descendant in $ENTRY_DESCENDANTS; do
		descendant_pid=${descendant%%:*}
		descendant_start=${descendant#*:}
		kill -0 "$descendant_pid" 2>/dev/null || continue
		if [ "$descendant_start" = unknown ]; then
			# No start token was recorded: the identity cannot be disproven.
			emit_target "$descendant_pid" unknown
			continue
		fi
		if ! value=$(nonce_value "/proc/$descendant_pid/environ"); then
			# The environment cannot be read now: the identity cannot be
			# disproven, so the member reads live and is never signaled.
			emit_target "$descendant_pid" unknown
			continue
		fi
		if [ "$value" != "$ENTRY_ID" ]; then
			continue
		fi
		current=$(current_start_token "$descendant_pid" || true)
		if [ -z "$current" ]; then
			emit_target "$descendant_pid" unknown
		elif [ "$current" = "$descendant_start" ]; then
			emit_target "$descendant_pid" "$current"
		fi
		# A differing start token is a reused id: already clean for that
		# member, never signaled.
	done
}

# descendant_pair_conflicts reports whether pid carries a recorded descendant
# pair whose start token differs: that pid is a reused id, not this work.
descendant_pair_conflicts() { # <pid> <start>
	for descendant in $ENTRY_DESCENDANTS; do
		descendant_pid=${descendant%%:*}
		descendant_start=${descendant#*:}
		if [ "$descendant_pid" = "$1" ] && [ "$descendant_start" != "$2" ]; then
			return 0
		fi
	done
	return 1
}

emit_kill_report() { # <id> <state> <signaled> <live> [<remaining tokens>]
	printf '{"version":%s,"id":"%s","signaled":%s,"live":%s,"state":"%s"' \
		"$PROTOCOL" "$1" "$3" "$4" "$2"
	# Only a member with a numeric pid is a process this report can name: an
	# opaque token ("-:unknown") still makes the answer live, but there is no
	# pid to emit.
	started=0
	for target in ${5:-}; do
		target_pid=${target%%:*}
		case $target_pid in '' | *[!0-9]*) continue ;; esac
		if [ "$started" -eq 1 ]; then
			printf ','
		else
			printf ',"remaining":['
			started=1
		fi
		printf '{"pid":%s,"startToken":"%s"}' "$target_pid" "$(json_escape "${target#*:}")"
	done
	if [ "$started" -eq 1 ]; then
		printf ']'
	fi
	printf '}\n'
}

# do_kill signals the superseded epoch's tracked work for one lease entry,
# under the presented epoch's taken-over lease: §4's "kill (bounded kill
# context)" step. The fencing worker's own bounded wait reads `recheck`; this
# call revalidates the stored ownership identity on the remote before any
# signal, gives the signaled work a bounded chance to leave, and records the
# kill through the same entry file — state killed, exit 143, the shell's
# SIGTERM convention — once nothing matching that identity is left. A member
# that cannot be verified is never signaled and reads live.
do_kill() { # <bootId> <opSeq> <id>
	E_BOOT=$1
	E_SEQ=$2
	load_guard_or_refuse
	# The caller must hold the taken-over lease: the guard carries this epoch's
	# pending fence and the lease holder names the same epoch. Anything else is
	# a superseded or foreign epoch, whose kill is dead.
	if [ "$FENCE_BOOT" = "-" ] || [ "$FENCE_BOOT" != "$E_BOOT" ] || [ "$FENCE_SEQ" != "$E_SEQ" ]; then
		refuse_stale "no fence for epoch $E_BOOT/$E_SEQ is pending"
	fi
	read_holder || refuse_corrupt "the lease holder is outside its schema"
	if [ "$HOLDER_BOOT" != "$E_BOOT" ] || [ "$HOLDER_SEQ" != "$E_SEQ" ]; then
		refuse_corrupt "the lease holder $HOLDER_BOOT/$HOLDER_SEQ does not name the fenced epoch $E_BOOT/$E_SEQ; replay the takeover"
	fi
	id=$3
	case $id in '' | *[!A-Za-z0-9]*) refuse_malformed "a lease entry id is required" ;; esac
	path=$LEASE_DIR/$id
	if [ ! -f "$path" ]; then
		# No entry was registered under this id: the member is already clean.
		emit_kill_report "$id" "" false false
		return 0
	fi
	load_entry "$path" || refuse_corrupt "lease entry $id is outside its schema"
	case $ENTRY_STATE in
	exited | killed)
		emit_kill_report "$ENTRY_ID" "$ENTRY_STATE" false false
		return 0
		;;
	esac
	signaled=false
	remaining=$(entry_live_targets)
	for target in $remaining; do
		target_pid=${target%%:*}
		target_start=${target#*:}
		# Signal only a fully verified identity: "unknown" is a member this
		# helper cannot prove is the wrapper's work.
		if [ "$target_start" = unknown ]; then
			continue
		fi
		# Re-read the kernel-owned start token immediately before signaling. It
		# narrows (never closes) the check-then-act window: the design's
		# 2026-09-26 decision withdraws the atomic pidfd requirement — no atomic
		# ownership-and-signal form is reachable through the helper's shell
		# interface — and accepts the residual window. An identity that cannot
		# be re-read now is skipped, never signaled.
		current=$(current_start_token "$target_pid" || true)
		if [ -z "$current" ] || [ "$current" != "$target_start" ]; then
			continue
		fi
		kill "$target_pid" 2>/dev/null || true
		signaled=true
	done
	state=$ENTRY_STATE
	if [ "$signaled" = true ]; then
		# Only a signaled process has something to wait for: an unverifiable
		# member is already the answer, and polling it would burn the worker's
		# kill budget for nothing.
		attempt=0
		while [ -n "$remaining" ] && [ "$attempt" -lt 20 ]; do
			attempt=$((attempt + 1))
			sleep 0.1
			remaining=$(entry_live_targets)
		done
	fi
	if [ -z "$remaining" ]; then
		# Settled: a successful enumeration found no process matching the stored
		# identity (an enumeration that cannot run, or that cannot verify a
		# member, prints an opaque target and keeps this branch out). The
		# fencing path marks the entry through the same entry file — exit 143,
		# the SIGTERM convention — whether this call signaled it or found it
		# already gone, so the lease file never keeps a live-looking entry with
		# no live holder. The command field is passed exactly as stored (already
		# JSON-escaped), matching do_perform's own write.
		exited=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf 0)
		write_entry "$ENTRY_ID" "$ENTRY_COMMAND" "$ENTRY_REGISTERED" killed \
			"$ENTRY_KIND" "$ENTRY_PID" "$ENTRY_START" "$ENTRY_NONCE" "$ENTRY_CGROUP" 143 "$exited" '' ||
			refuse io-error "cannot record the killed lease entry" 69
		state=killed
	fi
	if [ -z "$remaining" ]; then
		live=false
	else
		live=true
	fi
	emit_kill_report "$ENTRY_ID" "$state" "$signaled" "$live" "$remaining"
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
	trap 'release_lock' EXIT
	trap 'release_lock; exit 1' HUP INT TERM
	acquire_lock || refuse busy "the remote lease is held" 75
	do_takeover
	;;
advance)
	[ $# -eq 2 ] || refuse_malformed "advance needs an epoch"
	parse_epoch "$1" "$2" || refuse_malformed "malformed fencing epoch"
	trap 'release_lock' EXIT
	trap 'release_lock; exit 1' HUP INT TERM
	acquire_lock || refuse busy "the remote lease is held" 75
	do_advance
	;;
perform)
	[ $# -eq 3 ] || refuse_malformed "perform needs an epoch and a command"
	parse_epoch "$1" "$2" || refuse_malformed "malformed fencing epoch"
	[ -n "$3" ] || refuse_malformed "perform needs a command"
	trap 'release_lock' EXIT
	trap 'release_lock; exit 1' HUP INT TERM
	acquire_lock || refuse busy "the remote lease is held" 75
	do_perform "$1" "$2" "$3"
	;;
recheck)
	[ $# -eq 1 ] || refuse_malformed "recheck needs a lease entry id"
	do_recheck "$1"
	;;
kill)
	[ $# -eq 3 ] || refuse_malformed "kill needs an epoch and a lease entry id"
	parse_epoch "$1" "$2" || refuse_malformed "malformed fencing epoch"
	trap 'release_lock' EXIT
	trap 'release_lock; exit 1' HUP INT TERM
	acquire_lock || refuse busy "the remote lease is held" 75
	do_kill "$1" "$2" "$3"
	;;
*)
	refuse_malformed "unknown operation $op"
	;;
esac
