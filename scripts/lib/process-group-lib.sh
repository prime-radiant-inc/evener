# process-group-lib.sh — stop a spawned job and prove nothing of it is left.
#
# Source this from a script that spawns a child through perl's setpgrp(0, 0) and
# has to be able to stop it. The gate's bounded `go list` attempts and the
# golangci-lint installer's `curl | sh` both do, and both were growing their own
# copy of the same probes and stop loops. The rules are the expensive part, not
# the code: a zombie is not a survivor, a listing that will not run is not an
# empty group, and a pid is a group only while the kernel says it leads one.
#
# Each stop takes its grace in seconds, because how long a caller will spend
# proving a job is gone is the caller's business.
#
# What ownership of a group can be shown to be, and what it cannot. A marker only
# the wrapper carries cannot identify a member: a job spawns children, and a child
# outlives the process that carried the marker. Reading a member's environment
# would identify every one of them, and macOS will not do it — `ps -E` prints no
# environment for another process of this user, `/proc` does not exist, and
# `ps -o sess=` is 0 for everything. So the rule is: anything alive under a
# recorded number is stopped, unless the number has plainly been handed on, which
# is the one thing that can be read — a leader whose pid is the number and which
# started after the record was written. The residual, stated exactly: a number
# reused after our group emptied, whose new leader has since exited too, leaving
# descendants; those read as ours. Everything else is kept safe structurally by
# pgroup_signalable, which refuses 0, 1, a non-number and the group this caller is
# in. Its session check does nothing on macOS, where `ps -o sess=` is 0 for every
# process; the other refusals are what it enforces.

# pgroup_marker PREFIX — a token for one job, unique on the host.
#
# A shared word like `go` is no identity at all: two waves of this gate run `go`
# at once, and a record naming one of them matches the other. PREFIX says which
# script the job belongs to and the rest says which job, so a record can only
# ever match the attempt that wrote it.
pgroup_marker() {
	printf '%s-%s-%s%s' "$1" "$$" "$RANDOM" "$RANDOM"
}

# PGROUP_SPAWN_PERL — the program to hand `perl -e` when spawning a job that has
# to be stoppable. Its first argument is the record path, the rest is the command
# to run:
#
#   perl -e "$PGROUP_SPAWN_PERL" -- "$record" "$marker" cmd arg...  &
#
# The wrapper does not exec the job: it forks it, keeps its own argv — which
# carries the marker — and waits, so the group has a member identifiable as this
# job for as long as the job runs, and the wrapper's exit status is the job's
# (128 plus the signal number when the job was signalled, as a shell reports it).
# Exec-ing would have left the marker nowhere: `go list ./...` has no room for a
# token of ours on its command line.
#
# The caller creates the record, empty, before the fork; this fills it in. The
# marker is a token unique to this job, and it is what makes the record
# safe to act on later: a pid and the group named after it both outlive the job
# that held them, so a reader has to ask what is in that group now, not only
# whether something is.
#
# The record's states, which every reader here shares:
#
#   (no file)        no job is live for this caller
#   (empty)          the file exists and nothing has been spawned yet, or the
#                    spawn died before it could write
#   pid:N:MARKER     N is a pid still in the caller's own process group: it has
#                    not split yet, so it is stopped by pid and never by -N
#   pgid:N:MARKER    N is a process group of the job's own, written only once
#                    setpgrp said it made one
#   survivor:N       a group that took SIGTERM and SIGKILL and was still there:
#                    kept for whoever runs next, deliberately not signalled again
#
# Each write goes to a temporary file and is renamed into place, so a reader sees
# a whole record or no file at all. A record that cannot be written fails the
# spawn rather than leaving a job nothing can name.
PGROUP_SPAWN_PERL='
	my $record_path = shift @ARGV;
	my $marker = shift @ARGV;
	sub record {
		my ($path, $line) = @_;
		open my $fh, ">", "$path.tmp" or die "process group record $path: $!\n";
		print $fh $line or die "process group record $path: $!\n";
		close $fh or die "process group record $path: $!\n";
		rename "$path.tmp", $path or die "process group record $path: $!\n";
	}
	record($record_path, "pid:$$:$marker");
	setpgrp(0, 0) or die "setpgrp: $!\n";
	record($record_path, "pgid:$$:$marker");
	my $child = fork();
	die "fork: $!\n" unless defined $child;
	if ($child == 0) {
		exec @ARGV or die "exec: $!\n";
	}
	waitpid($child, 0);
	my $status = $?;
	exit(($status & 127) ? 128 + ($status & 127) : $status >> 8);
'

# pgroup_record_spawned PATH PID MARKER — the parent's own note of what it has
# just forked, written the moment `$!` is known.
#
# The child writes the record proper, but it cannot write it before it exists:
# between the fork and the child's first write there is a job running that the
# record does not name, and a cleanup arriving there finds an empty file and
# nothing it can signal. The parent knows the pid one command after the fork, so
# it says so too.
#
# It writes a file of its own, `PATH.spawned`, and never PATH. Sharing PATH
# meant reading it and then replacing it, and a check followed by a rename is not
# one step: the child can fill the record in between the two, and the parent then
# overwrites a `pgid:` with its older `pid:`. With two names there is nothing to
# clobber, and the reader prefers the record and falls back to this.
pgroup_record_spawned() {
	local path="$1" pid="$2" marker="$3"
	printf 'pid:%s:%s' "$pid" "$marker" >"$path.spawned.tmp" || return 1
	mv "$path.spawned.tmp" "$path.spawned"
}

# pgroup_record_clear PATH — drop a record and everything written beside it.
pgroup_record_clear() {
	rm -f "$1" "$1.spawned" "$1.spawned.tmp" "$1.survivor.tmp" "$1.tmp"
}

# pgroup_record_value PATH GRACE — what the spawn recorded at PATH, waiting up to
# GRACE seconds for a record that exists but has not been filled in yet. Prints
# `pid:N` or `pgid:N`; returns 1 when it never fills in, which means the job
# never got as far as naming itself and nothing here can aim at it.
pgroup_record_value() {
	local path="$1" grace="$2" value waited=0
	value="$(cat "$path" 2>/dev/null)"
	while [ -z "$value" ] && [ "$waited" -lt "$((grace * 10))" ]; do
		sleep 0.1
		value="$(cat "$path" 2>/dev/null)"
		waited=$((waited + 1))
	done
	if [ -z "$value" ]; then
		# Nothing from the job itself; what the parent wrote beside it is the
		# only name left, and it is a pid, which the reader knows how to use.
		value="$(cat "$path.spawned" 2>/dev/null)"
	fi
	[ -n "$value" ] || return 1
	printf '%s' "$value"
}

# pgroup_survivors PGID — print `pid(state)` for every live
# member of PGID, zombies excluded. Exit status 0 means the printed answer is
# trustworthy; 2 means the process listing itself failed, so nothing is known
# about the group and an empty answer must NOT be read as "gone".
#
# Zombies have to be excluded, and `kill -0 -- -PGID` cannot do it. A process
# the kernel has finished with stays a member of its own group until its parent
# reaps it, and a group signal is reported as delivered to it, so the leader
# this function is asked about would read as "still running" for as long as the
# shell had not got round to reaping it — a successful kill reported as a
# survivor, purely on reap timing. Asking `ps` for the state instead makes the
# answer independent of when anyone reaps.
#
# The status is separate from the output because the two failures look
# identical otherwise. A `ps` that cannot run prints nothing, and a caller
# reading that as "no live members" starts beside a job that is still running and holding whatever it holds — the
# exact race the stop exists to prevent.
pgroup_survivors() {
	local pgid="$1" listing
	if ! listing="$(ps -axo pid=,pgid=,state= 2>/dev/null)" || [ -z "$listing" ]; then
		return 2
	fi
	printf '%s\n' "$listing" |
		awk -v pgid="$pgid" '$2 == pgid && $3 !~ /^[Zz]/ { printf "%s(%s) ", $1, $3 }'
}

# pgroup_survivor_report PGID — what is left of PGID, as a phrase a
# diagnostic can print. "Nothing was there" and "nobody could look" are
# different answers, and collapsing the second into the first tells the reader
# the group was confirmed empty when in fact the process listing would not run.
pgroup_survivor_report() {
	local report
	if ! report="$(pgroup_survivors "$1")"; then
		printf '<unknown: the process listing would not run>'
		return 0
	fi
	printf '%s' "${report:-<none at the final probe>}"
}

# pgroup_signalable PGID — 0 when PGID is a group this caller may signal, 1 when
# it is not, with the reason on stderr.
#
# `kill -- -N` is the most dangerous thing in this library and the numbers it is
# given come out of files and `ps` output. Four of them must never be signalled:
# 0, which means "my own process group" and would take the caller and everything
# it is running down with it; the group and the session the caller is in, for the
# same reason by another spelling; and 1, which is not a group any job here
# makes. A number that fails this is a bug or a stale record, and either way the
# refusal is said out loud rather than swallowed.
pgroup_signalable() {
	local pgid="$1" own_pgid own_sid
	case "$pgid" in
	'' | *[!0-9]*)
		printf 'process-group-lib: refusing to signal process group %q: not a number.\n' "$pgid" >&2
		return 1
		;;
	0 | 1)
		printf 'process-group-lib: refusing to signal process group %s: 0 is the caller itself, and 1 is not a group any job here makes.\n' "$pgid" >&2
		return 1
		;;
	esac
	own_pgid="$(ps -o pgid= -p $$ 2>/dev/null | tr -d '[:space:]')"
	if [ -n "$own_pgid" ] && [ "$pgid" = "$own_pgid" ]; then
		printf 'process-group-lib: refusing to signal process group %s: it is the group this caller is in.\n' "$pgid" >&2
		return 1
	fi
	own_sid="$(ps -o sess= -p $$ 2>/dev/null | tr -d '[:space:]')"
	if [ -n "$own_sid" ] && [ "$pgid" = "$own_sid" ]; then
		printf 'process-group-lib: refusing to signal process group %s: it is the session this caller is in.\n' "$pgid" >&2
		return 1
	fi
	return 0
}

# pid_signalable PID — 0 when PID is a process this caller may signal, 1 when it
# is not, with the reason on stderr.
#
# The single-process counterpart of pgroup_signalable, and refused for the same
# reasons: 0 and 1 are not jobs anyone here spawned, a non-number is a corrupt
# record, and this caller must not signal itself.
pid_signalable() {
	local pid="$1"
	case "$pid" in
	'' | *[!0-9]*)
		printf 'process-group-lib: refusing to signal pid %q: not a number.\n' "$pid" >&2
		return 1
		;;
	esac
	if [ "$pid" -le 1 ]; then
		printf 'process-group-lib: refusing to signal pid %s: 0 and 1 are not jobs spawned here.\n' "$pid" >&2
		return 1
	fi
	if [ "$pid" = "$$" ]; then
		printf 'process-group-lib: refusing to signal pid %s: it is this caller itself.\n' "$pid" >&2
		return 1
	fi
	return 0
}

# pgroup_number_reused RECORD PGID — 0 when PGID now belongs to somebody else,
# 1 when nothing says it does, 2 when that could not be read.
#
# The one reuse question this platform can answer. A group number is handed on
# only after the group it named emptied, and a new group with that number needs a
# new leader whose pid is the number, created after ours was. The record is
# written when the group is made, so a leader that started later than the record
# is not the one the record was written for.
#
# Elapsed time, not the absolute start time: `ps -o etime=` has the same fixed
# [[dd-]hh:]mm:ss format on macOS and Linux, where `lstart=` is a locale-formatted
# date whose parsing differs between BSD and GNU `date`. Two seconds of slack,
# because both ends are whole seconds and the answer to prefer when they are close
# is "ours": that costs a signal to a group pgroup_signalable has already refused
# to aim anywhere dangerous, where the other way round abandons a live job holding
# its locks.
pgroup_number_reused() {
	local record="$1" pgid="$2" elapsed recorded_at started days rest hours minutes seconds
	elapsed="$(ps -o etime= -p "$pgid" 2>/dev/null | tr -d '[:space:]')"
	[ -n "$elapsed" ] || return 1
	recorded_at="$(pgroup_file_mtime "$record")"
	[ -n "$recorded_at" ] || return 2
	case "$elapsed" in
	*-*)
		days="${elapsed%%-*}"
		rest="${elapsed#*-}"
		;;
	*)
		days=0
		rest="$elapsed"
		;;
	esac
	seconds="${rest##*:}"
	minutes="${rest%:*}"
	case "$rest" in
	*:*:*)
		hours="${minutes%%:*}"
		minutes="${minutes#*:}"
		;;
	*)
		hours=0
		;;
	esac
	started=$(( $(date +%s) - (10#$days * 86400 + 10#$hours * 3600 + 10#$minutes * 60 + 10#$seconds) ))
	[ "$started" -gt $((recorded_at + 2)) ]
}

# pgroup_file_mtime PATH — the file's modification time in seconds since the
# epoch, in whichever spelling of `stat` the host has.
pgroup_file_mtime() {
	stat -f %m "$1" 2>/dev/null || stat -c %Y "$1" 2>/dev/null
}

# pid_owned_by PID MARKER — the same question about a single process, for a job
# that has not split into a group of its own yet. 0 when PID is running MARKER,
# 1 when it is not — gone, a zombie, or a number somebody else now holds — and 2
# when the listing would not run.
pid_owned_by() {
	local pid="$1" marker="$2" command state word
	command="$(ps -o command= -p "$pid" 2>/dev/null)"
	if [ -z "$command" ]; then
		kill -0 "$pid" 2>/dev/null || return 1
		return 2
	fi
	state="$(ps -o state= -p "$pid" 2>/dev/null | tr -d '[:space:]')"
	case "$state" in
	[Zz]*) return 1 ;;
	esac
	for word in $command; do
		[ "$word" = "$marker" ] && return 0
		[ "${word##*/}" = "$marker" ] && return 0
	done
	return 1
}

# pid_pgroup PID — print the process group PID is in, or nothing when that
# cannot be read. The one place this library parses `ps` for a group number.
pid_pgroup() {
	ps -o pgid= -p "$1" 2>/dev/null | tr -d '[:space:]'
}

# pgroup_record_survivor RECORD PGID — mark a record as a group that took
# SIGTERM and SIGKILL and was still there. Kept for whoever runs next, and never
# signalled again by this library.
pgroup_record_survivor() {
	printf 'survivor:%s' "$2" >"$1.survivor.tmp" || return 1
	mv "$1.survivor.tmp" "$1"
}

# stop_recorded_job RECORD GRACE MARKER_PREFIX — stop whatever RECORD names, and say
# what happened. The whole state table lives here, so no caller has to know it:
#
#   (no file)        nothing to do
#   (empty)          wait GRACE for the spawn to name itself, then decide
#   pid:N:MARKER     one process, in the caller own group: the marker can answer
#                    for it, so it decides, and then the group N may have become
#                    is stopped too
#   pgid:N:MARKER    a group of the job own: survivors decide — anything alive
#                    under N is stopped, because the marker cannot speak for a
#                    child whose parent carried it
#   survivor:N       already given SIGTERM and SIGKILL and still there: kept,
#                    and nothing is signalled again
#
# Exit status, which is also what happens to the record:
#   0  stopped, or nothing of it was running — the record is removed
#   1  the number is not this job any more — the record is removed
#   2  it cannot be shown to have stopped — the record is kept
#   3  the record already says the group survived both signals — kept, untouched
# The sentence to print is left in pgroup_stop_reason for statuses 1 to 3.
stop_recorded_job() {
	local record="$1" grace="$2" marker="$3"
	local value number recorded_marker members status=0
	pgroup_stop_reason=""
	[ -e "$record" ] || return 0
	if ! value="$(pgroup_record_value "$record" "$grace")"; then
		pgroup_stop_reason="the job never named itself, so nothing here can aim at it."
		return 2
	fi
	case "$value" in
	survivor:*)
		pgroup_stop_reason="$(printf 'process group %s was still there after SIGTERM and SIGKILL; it has not been signalled again.' "${value#survivor:}")"
		return 3
		;;
	pid:* | pgid:*) ;;
	*)
		pgroup_stop_reason="$(printf 'the record reads %s, which is not a state this library writes.' "$value")"
		return 2
		;;
	esac
	number="${value#*:}"
	number="${number%%:*}"
	recorded_marker="${value##*:}"
	# MARKER is the prefix this caller spawns under; the record carries the whole
	# token, which is unique to one job. A record under some other prefix was not
	# written by this caller and is none of its business.
	if [ -n "$marker" ] && [ "${recorded_marker#"$marker"}" = "$recorded_marker" ]; then
		pgroup_stop_reason="$(printf 'the record was written for %s, which is not this caller job.' "$recorded_marker")"
		pgroup_record_clear "$record"
		return 1
	fi
	if [ "${value#pid:}" != "$value" ]; then
		# A pid names one process, which is the only thing the marker can
		# answer for: it either carries it or the kernel has given the number
		# to somebody else since.
		pid_owned_by "$number" "$recorded_marker" || status=$?
		if [ "$status" -eq 1 ]; then
			pgroup_record_clear "$record"
			return 1
		fi
		if [ "$status" -ne 0 ]; then
			pgroup_stop_reason="$(printf 'the process listing that says whether pid %s is still this job would not run.' "$number")"
			return 2
		fi
		status=0
		stop_pid "$number" "$grace" || status=$?
		if [ "$status" -ne 0 ]; then
			pgroup_stop_reason="$(printf 'pid %s did not go after SIGTERM and SIGKILL with %ss of grace each.' "$number" "$grace")"
			return 2
		fi
	fi
	# Whatever the record said, the number may be a group by now: a job splits
	# the instant after it is forked, and a group outlives the process that made
	# it. Survivors decide from here.
	if ! members="$(pgroup_survivors "$number")"; then
		escalate_blind "$number" "$grace"
		pgroup_stop_reason="$(printf 'the process listing that says whether group %s is empty would not run; it has been signalled blind.' "$number")"
		return 2
	fi
	if [ -z "$members" ]; then
		pgroup_record_clear "$record"
		return 0
	fi
	# Members, then — but are they ours? Per member the platform will not say;
	# what it will say is whether the number has been handed on since the record
	# was written, which is the only way it can stop being ours.
	status=0
	pgroup_number_reused "$record" "$number" || status=$?
	if [ "$status" -eq 0 ]; then
		pgroup_stop_reason="$(printf 'process group %s is led by a process that started after this record was written, so the number has been handed on.' "$number")"
		pgroup_record_clear "$record"
		return 1
	fi
	if [ "$status" -eq 2 ]; then
		pgroup_stop_reason="$(printf 'whether process group %s is still this job could not be read.' "$number")"
		return 2
	fi
	status=0
	stop_pgroup "$number" "$grace" || status=$?
	case "$status" in
	0)
		pgroup_record_clear "$record"
		return 0
		;;
	1)
		pgroup_record_survivor "$record" "$number"
		pgroup_stop_reason="$(printf 'process group %s still holds %s after SIGTERM and SIGKILL with %ss of grace each.' "$number" "$(pgroup_survivor_report "$number")" "$grace")"
		return 2
		;;
	esac
	pgroup_stop_reason="$(printf 'process group %s cannot be shown to have stopped: the process listing would not run.' "$number")"
	return 2
}

# escalate_blind PID GRACE — the last thing to do for a job nothing can see:
# SIGTERM, GRACE seconds, then SIGKILL, to the group the pid may lead and to the
# pid itself.
#
# It is sent blind, which is the point. When the process listing will not run,
# "cannot be shown to have stopped" is an honest answer but on its own it leaves
# the job running and holding whatever it holds, poisoning every later run on
# the host. The grace is blind too: nothing here can watch a handler run, so the
# wait is the only thing a SIGTERM can be given. Escalating is not confirming —
# the caller still owes its reader the fail-closed answer.
escalate_blind() {
	local pid="$1" grace="$2" group=1 single=1
	pgroup_signalable "$pid" || group=0
	pid_signalable "$pid" || single=0
	if [ "$group" -eq 0 ] && [ "$single" -eq 0 ]; then
		# Neither spelling of this number may be signalled, so nothing was
		# sent and the caller is owed the unknown it started with.
		return 2
	fi
	[ "$group" -eq 1 ] && { kill -TERM -- -"$pid" 2>/dev/null || :; }
	[ "$single" -eq 1 ] && { kill -TERM "$pid" 2>/dev/null || :; }
	sleep "$grace"
	[ "$group" -eq 1 ] && { kill -KILL -- -"$pid" 2>/dev/null || :; }
	[ "$single" -eq 1 ] && { kill -KILL "$pid" 2>/dev/null || :; }
	return 0
}

# stop_pgroup PGID GRACE — stop every member of PGID and prove
# nothing of it is still running. Returns 0 when the group is confirmed empty,
# 1 when it still has a live member after the escalation, and 2 when liveness
# could not be determined at all. Only 0 means the caller may treat the job as gone.
#
# By group, not by process tree: a snapshot of descendants plus a signal to
# what it showed misses a child forked after the snapshot, and one that
# outlives the leader survives into whatever comes next, still holding whatever it holds. A group signal reaches
# every member however late it appeared.
#
# The group is made by the spawn, not here: the job is exec'd through
# perl's setpgrp(0, 0), so the child becomes its own group leader and its pgid
# is its pid — which is why the caller can pass the pid it already holds.
# setsid(1) is the usual tool for that and is not present on macOS, which these scripts have to run on; perl is, and so is the CI image's.
stop_pgroup() {
	local pgid="$1" grace="$2" signal waited ticks alive
	pgroup_signalable "$pgid" || return 1
	ticks=$((grace * 10))
	for signal in TERM KILL; do
		kill -"$signal" -- -"$pgid" 2>/dev/null || :
		waited=0
		while [ "$waited" -lt "$ticks" ]; do
			if ! alive="$(pgroup_survivors "$pgid")"; then
				# A probe that cannot run says nothing about what survived, so
				# escalate before giving up. Returning here straight after
				# SIGTERM left a TERM-ignoring job running, holding Go's
				# build and module cache locks for every later run on this
				# host. Escalating is not confirming, so the caller still gets
				# the fail-closed status.
				kill -KILL -- -"$pgid" 2>/dev/null || :
				return 2
			fi
			if [ -z "$alive" ]; then
				return 0
			fi
			sleep 0.1
			waited=$((waited + 1))
		done
	done
	if ! alive="$(pgroup_survivors "$pgid")"; then
		# Same escalation as above. By here SIGKILL has already been sent once,
		# and sending it again to a group with nothing left in it is a no-op.
		kill -KILL -- -"$pgid" 2>/dev/null || :
		return 2
	fi
	if [ -z "$alive" ]; then
		return 0
	fi
	return 1
}

# pid_is_running PID — 0 when PID is a running process, 1 when it
# is gone or a zombie waiting to be reaped, 2 when that could not be determined.
#
# `kill -0` alone cannot answer it: a zombie accepts signals as far as the
# kernel is concerned, so a reaped-but-uncollected job would read as still
# running for as long as nobody had waited on it. The state comes from `ps`, and
# the pid is re-asked of the kernel when `ps` prints nothing, because a listing
# that will not run and a process that has just gone print the same nothing.
pid_is_running() {
	local pid="$1" state
	kill -0 "$pid" 2>/dev/null || return 1
	state="$(ps -o state= -p "$pid" 2>/dev/null | tr -d '[:space:]')"
	if [ -z "$state" ]; then
		kill -0 "$pid" 2>/dev/null || return 1
		return 2
	fi
	case "$state" in
	[Zz]*) return 1 ;;
	*) return 0 ;;
	esac
}

# stop_pid PID GRACE — stop a job that has not split into a group of its own yet, and prove it is gone. Same contract as
# stop_pgroup: 0 when confirmed gone, 1 when it is still there after
# SIGTERM and SIGKILL, 2 when liveness could not be determined.
#
# By pid, because there is no group of its own to name: such a job is still in its parent's
# group, which is the one group its parent may never signal. It has spawned
# nothing yet either — it becomes the program it was asked to run only at the
# exec that follows the split — so the pid is the whole of it.
stop_pid() {
	local pid="$1" grace="$2" signal waited ticks status
	pid_signalable "$pid" || return 1
	ticks=$((grace * 10))
	for signal in TERM KILL; do
		kill -"$signal" "$pid" 2>/dev/null || :
		waited=0
		while [ "$waited" -lt "$ticks" ]; do
			pid_is_running "$pid"
			status=$?
			[ "$status" -eq 1 ] && return 0
			if [ "$status" -eq 2 ]; then
				# Same escalation as the group stop: a probe that cannot run
				# says nothing about what survived, so SIGKILL goes out before
				# the fail-closed answer does.
				kill -KILL "$pid" 2>/dev/null || :
				return 2
			fi
			sleep 0.1
			waited=$((waited + 1))
		done
	done
	pid_is_running "$pid"
	status=$?
	[ "$status" -eq 1 ] && return 0
	if [ "$status" -eq 2 ]; then
		kill -KILL "$pid" 2>/dev/null || :
		return 2
	fi
	return 1
}
