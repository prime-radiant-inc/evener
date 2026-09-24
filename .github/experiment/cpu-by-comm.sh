#!/usr/bin/env bash
# cpu-by-comm.sh ROOTPID OUT — every 5s, record cumulative CPU seconds of every
# descendant of ROOTPID (pid, comm, cputime) to OUT until ROOTPID exits.
root=$1 out=$2
: > "$out"
while kill -0 "$root" 2>/dev/null; do
	ps -e -o pid=,ppid=,cputimes=,comm= >"$out.snap"
	awk -v root="$root" '{pp[$1]=$2; c[$1]=$3; n[$1]=$4} END{for(p in pp){q=p; while(q && q!=root && q!=1) q=pp[q]; if(q==root) print p, n[p], c[p]}}' "$out.snap" >> "$out"
	sleep 5
done
