# Reproducing the CPU-efficiency audit numbers

`go tool pprof` never finished its cold tool build on the audit host (four
attempts, isolated `GOCACHE` and `GOPROXY` variants, killed after 5 minutes
each). These three stdlib-only readers read the profile instead. They are how
every percentage in `../SYNTHESIS.md` was produced. Re-derive the numbers with
real `pprof` on a quiet machine before publishing them.

## The profile

```bash
# linux/amd64, in a golang:1.27 container with the worktree mounted read-only
go build -o /tmp/evener ./cmd/evener
go run ./test/e2e/turncpu --evener /tmp/evener --rounds 200 --out /out/run --cpu-profile
```

`test/e2e/turncpu` is the repo's own harness and reports wall, process CPU, and
per-round mean CPU. It uses a scripted provider, so no network and no live
model behaviour is involved.

## The readers

```bash
./topfun.py <profile.pprof> [limit]        # flat and cumulative CPU by function
./topfun.py <profile.pprof> <limit> <name> # callers and callees of one function
./stacks.py <profile.pprof> [substring]    # weighted stacks, leaf first
./attr.py   <profile.pprof> <substring>    # CPU of stacks matching a substring,
                                           # attributed to the nearest evener frame
```

`attr.py` is the one that produced the fsync, GC, and JSON tables: it answers
"how much CPU does this syscall or package cost, and which function in our code
owns it".

## Two protobuf quirks that cost me an hour

- `Function.name` is an index into the profile's string table (field 6 of the
  profile message), not field 2 of the function message. Read it through the
  table.
- `Sample.value` is a repeated field, usually `[samples/count, cpu/nanoseconds]`,
  sometimes packed as wire type 2. The CPU nanoseconds are the **last** entry,
  not the first. Reading the first gives every function `0.0 ms`.

## Sanity checks

Cross-check any profile claim against a syscall count before believing it. The
fsync figures in the synthesis were confirmed two ways: 790 ms of `Fsync` in
this profile, and 150 `fsync` calls over 21 rounds from
`strace -f -c -e trace=fsync,fdatasync,openat,write,rename`. They agree to
within 15%.

Run `strace` against prebuilt binaries. An earlier run that used `go run`
reported fsync as 17% of syscall time because the compiler was writing into the
same trace; the true figure is 71%.
