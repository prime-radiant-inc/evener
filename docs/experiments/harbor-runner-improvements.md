## harbor-runner fixes (2026-03-25)

Repo: ~/prime-radiant/harbor-runner (committed to main)

### --run-id flag
Override auto-generated run ID. Allows parallel launches without 65s sleep.
All instances share same S3 prefix for easy collection.

### --rep flag
Set explicit rep number per instance. Required with --run-id to avoid S3 path collisions.
Each task gets a unique rep number under the shared run ID.

### __AGENT_KWARGS__ fix
Always replace the placeholder (even when empty). Previously, launching without
--agent-kwargs left the literal `__AGENT_KWARGS__` in userdata, crashing cloud-init.

### Agent tarball upload skip
`aws s3 ls` check before upload — skips if tarball already exists.
Prevents race conditions when launching in parallel.

### Launch pattern
```bash
# Upload once (dry-run triggers upload without launching)
./launch.sh --run-id NAME --rep 1 --agent-dir DIR ... --dry-run

# Then launch sequentially (upload is skipped)
REP=0
for task in task1 task2 task3; do
  REP=$((REP + 1))
  ./launch.sh --run-id NAME --rep $REP --task-names "$task" ...
done
```

**Why:** 128 vCPU spot quota = 32 c6i.xlarge max. Launch in waves of ~30.
Parallel backgrounded launches race on tarball upload — do it sequentially.
