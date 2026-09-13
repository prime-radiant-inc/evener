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

# PGROUP_SPAWN_PERL — the program to hand `perl -e` when spawning a job that has
# to be stoppable. Its first argument is the record path, the rest is the command
# to run:
#
#   perl -e "$PGROUP_SPAWN_PERL" -- "$record" "$marker" cmd arg...  &
#
# The caller creates the record, empty, before the fork; this fills it in. The
# marker is the argv0 the job will run under, and it is what makes the record
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
	exec @ARGV or die "exec: $!\n";
'

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

# pid_leads_pgroup PID — 0 when the kernel says PID leads the group numbered
# PID, 1 otherwise, including when that cannot be read.
#
# The one question that makes `kill -- -PID` safe. Before its setpgrp runs a
# spawned child is still in its parent's group, and once it has gone the number
# belongs to whoever the kernel hands it to next: a group signal aimed at it in
# either state names a group the caller has no business touching.
pid_leads_pgroup() {
	local pid="$1" pgid
	pgid="$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d '[:space:]')"
	[ -n "$pgid" ] && [ "$pgid" = "$pid" ]
}

# pgroup_owned_by PGID MARKER — 0 when PGID holds a live member running MARKER,
# 1 when it holds none, 2 when the process listing would not run.
#
# The question every recorded number has to answer before it is signalled. A pid
# and the group named after it outlive the job that held them, and the kernel
# hands both to somebody else in time, so "is anything in group N" is not the
# same as "is the job I recorded still in group N". MARKER is the argv0 the job
# was spawned under; a group whose live members are all somebody else's is gone
# as far as this caller is concerned, and gets no signal.
pgroup_owned_by() {
	local pgid="$1" marker="$2" listing
	if ! listing="$(ps -axo pid=,pgid=,state=,command= 2>/dev/null)" || [ -z "$listing" ]; then
		return 2
	fi
	printf '%s\n' "$listing" |
		awk -v pgid="$pgid" -v marker="$marker" '
			$2 == pgid && $3 !~ /^[Zz]/ {
				n = split($4, parts, "/")
				if (parts[n] == marker) { found = 1 }
			}
			END { exit(found ? 0 : 1) }
		'
}

# pid_owned_by PID MARKER — the same question about a single process, for a job
# that has not split into a group of its own yet. 0 when PID is running MARKER,
# 1 when it is not — gone, a zombie, or a number somebody else now holds — and 2
# when the listing would not run.
pid_owned_by() {
	local pid="$1" marker="$2" command state
	command="$(ps -o command= -p "$pid" 2>/dev/null)"
	if [ -z "$command" ]; then
		kill -0 "$pid" 2>/dev/null || return 1
		return 2
	fi
	state="$(ps -o state= -p "$pid" 2>/dev/null | tr -d '[:space:]')"
	case "$state" in
	[Zz]*) return 1 ;;
	esac
	command="${command%% *}"
	[ "${command##*/}" = "$marker" ]
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
	local pid="$1" grace="$2"
	kill -TERM -- -"$pid" 2>/dev/null || :
	kill -TERM "$pid" 2>/dev/null || :
	sleep "$grace"
	kill -KILL -- -"$pid" 2>/dev/null || :
	kill -KILL "$pid" 2>/dev/null || :
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
