## Long Runs (Detached)

For long `attractor run`/`resume` jobs, launch detached so the parent shell/session ending does not kill Kilroy:

```bash
RUN_ROOT=/path/to/run_root
setsid -f bash -lc 'cd /home/user/code/kilroy-wt-state-isolation-watchdog && ./kilroy attractor resume --logs-root "$RUN_ROOT/logs" >> "$RUN_ROOT/resume.out" 2>&1'
```
