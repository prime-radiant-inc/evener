"""Diagnostic only (not for merge): sample the runner every 0.5s while the gate runs.

Writes two TSVs into the directory named by argv[1]:
  load.tsv   t  procs_running  procs_blocked  cpu_user  cpu_nice  cpu_sys  cpu_idle  cpu_iowait  cpu_steal (jiffies)
  procs.tsv  pid  argv0  cwd  first_t  last_t  utime  stime  cutime  cstime (jiffies, last sample)
for every process whose argv[0] ends in ".test" (a Go test binary).
"""
import os, sys, time, signal

out = sys.argv[1]
os.makedirs(out, exist_ok=True)
procs = {}
load = open(os.path.join(out, "load.tsv"), "w")
stop = False


def handle(*_):
    global stop
    stop = True


signal.signal(signal.SIGTERM, handle)
signal.signal(signal.SIGINT, handle)


def dump():
    with open(os.path.join(out, "procs.tsv"), "w") as fh:
        for pid, p in procs.items():
            fh.write("\t".join(str(x) for x in (pid, *p)) + "\n")


last_dump = time.time()
while not stop:
    t = time.time()
    running = blocked = 0
    cpu = []
    with open("/proc/stat") as fh:
        for line in fh:
            if line.startswith("cpu "):
                cpu = line.split()[1:9]
            elif line.startswith("procs_running"):
                running = int(line.split()[1])
            elif line.startswith("procs_blocked"):
                blocked = int(line.split()[1])
    load.write(f"{t:.2f}\t{running}\t{blocked}\t" + "\t".join(cpu[i] for i in (0, 1, 2, 3, 4, 7)) + "\n")
    for d in os.listdir("/proc"):
        if not d.isdigit():
            continue
        try:
            with open(f"/proc/{d}/cmdline", "rb") as fh:
                argv0 = fh.read().split(b"\0", 1)[0].decode(errors="replace")
            if not argv0.endswith(".test"):
                continue
            with open(f"/proc/{d}/stat") as fh:
                fields = fh.read().rsplit(")", 1)[1].split()
            ut, st, cut, cst = (int(fields[i]) for i in (11, 12, 13, 14))
            key = int(d)
            if key in procs:
                p = procs[key]
                procs[key] = (p[0], p[1], p[2], t, ut, st, cut, cst)
            else:
                cwd = os.readlink(f"/proc/{d}/cwd")
                procs[key] = (os.path.basename(argv0), cwd, t, t, ut, st, cut, cst)
        except (OSError, IndexError, ValueError):
            continue
    if t - last_dump > 10:
        load.flush()
        dump()
        last_dump = t
    time.sleep(max(0.0, 0.5 - (time.time() - t)))
load.close()
dump()
