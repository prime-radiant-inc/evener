# Task 3 — live hub endpoint discovery report

**Status: DONE.** Source commit: `724ff0fff252508febd2cb000e1c1592859b8e0f` (`feat(hub): report the live bound endpoint`). Reviewed base: `594c72e9ac96b8a9f39ab4b51b364aef6affb4f8`. This report is committed separately so it can name the source commit. No production hub or installed configuration was touched.

## Delivered

- `internal/hubendpoint/report.go`, `runtime*.go`, `verify.go`: schema-1 report/record and bounded decode; 0600 atomic record publication; OS-user, inode, regular-file, writable-mode, and no-follow checks; generation-conditional retirement; token-free direct health verification on the **same supplied connection**, with bounded response, drained body, synchronized cancellation, and cleared deadlines before reuse. Local targets require a numeric address assigned to this host.
- `cmd/evener-hub/endpoint.go`: `hub endpoint --json [--config PATH] [--addr HOST:PORT]` resolves the selected state root/config first, then inspects the existing lock/record and verifies the actual server. It never loads auth tokens or credentials. Output distinguishes `stopped`, `starting`, `running`, and `unavailable`; only verified running reports contain `running`. Edited listener configuration does not change an existing process's identity; selecting another state root changes ownership.
- `cmd/evener-hub/main.go`, `web_api.go`, `hubapi/types.go`: publish the bound address after binding and building the health handler, just before serving; report `boot_id` in health; retire the record before releasing the lock. The configured address remains requested, including `:0`.
- `cmd/evener-hub/internal/hostlock/hostlock*.go`: read-only `IsHeld` opens an existing regular same-user lock, checks its inode before/after and uses the real non-blocking flock; unsupported inspection returns an error rather than treating the lock as free.
- `cmd/evener-hub/endpoint_test.go`, `main_ephemeral_port_test.go`, `main_hublock_test.go`, `internal/hostlock/endpoint*_test.go`, `internal/hubendpoint/*_test.go`: cold start, real listener and process lifecycle, bind failure, concurrent starter, actual port after `:0`, crashed owner's stale record, port reuse with no Authorization header, corrupt metadata, ownership, record replacement/retirement, report bounds, source precedence, and same-connection authenticated request after token-free verification. `docs/evener-hub-endpoints.md` documents the operator command and state behavior.

## Red evidence

1. Before implementation, `go test ./cmd/evener-hub ./cmd/evener-hub/internal/hostlock ./internal/hubendpoint -run 'Test(Endpoint|RunMainAddrZero|.*HubLock)' -count=1` exited **1**: `endpoint_test.go:39:60: undefined: hubendpoint.Report`, `:42:10: undefined: runEndpoint`, followed by missing `Record`/`Publish`; `FAIL primeradiant.com/evener/cmd/evener-hub [build failed]`.
2. Before lock/record implementation, `go test ./cmd/evener-hub/internal/hostlock ./internal/hubendpoint -run 'TestEndpoint' -count=1` exited **1**: `endpoint_test.go:10:12: undefined: IsHeld`; `runtime_test.go:13:30: undefined: Record`, `:16:14: undefined: Publish`; both packages `build failed`.
3. Before allowing a DNS origin through the verified numeric socket, `go test ./internal/hubendpoint -run 'TestEndpointVerifyLocalConnReusableForAuthenticatedRequest' -count=1` exited **1**: `--- FAIL: TestEndpointVerifyLocalConnReusableForAuthenticatedRequest`, `verify_test.go:50: same connection verification: origin does not match connection`. The final test sends an authenticated request **on that exact connection** after the unauthenticated health probe.

## Green evidence

All commands below ran from this worktree with the pinned Go 1.27 toolchain. The final source commit was made after these checks.

```text
$ go test ./cmd/evener-hub ./cmd/evener-hub/internal/hostlock ./internal/hubendpoint -run 'Test(Endpoint|RunMainAddrZero|.*HubLock)' -count=1
ok  primeradiant.com/evener/cmd/evener-hub                    1.949s
ok  primeradiant.com/evener/cmd/evener-hub/internal/hostlock  0.004s
ok  primeradiant.com/evener/internal/hubendpoint             0.048s
[exit 0]
$ go test ./cmd/evener-hub/internal/hostlock ./internal/hubendpoint -count=1
ok  primeradiant.com/evener/cmd/evener-hub/internal/hostlock  0.004s
ok  primeradiant.com/evener/internal/hubendpoint             0.031s
[exit 0]
$ go test ./cmd/evener-hub/internal/hostlock ./internal/hubendpoint ./hubapi ./cmd/evener-hub/internal/hubcore -count=1
ok  primeradiant.com/evener/cmd/evener-hub/internal/hostlock  0.011s
ok  primeradiant.com/evener/internal/hubendpoint             0.064s
ok  primeradiant.com/evener/hubapi                            0.018s
ok  primeradiant.com/evener/cmd/evener-hub/internal/hubcore   24.180s
[exit 0]
$ go test -race ./internal/hubendpoint ./cmd/evener-hub/internal/hostlock -run '^TestEndpoint' -count=1
ok  primeradiant.com/evener/internal/hubendpoint             1.051s
ok  primeradiant.com/evener/cmd/evener-hub/internal/hostlock  1.015s
[exit 0]
$ go test -race ./cmd/evener-hub -run '^TestEndpoint(ActualHubLifecycle|CrashLeavesStaleRecordForNextOwner|BindFailureAndConcurrentStarter|RejectsReusedPort)$' -count=1
ok  primeradiant.com/evener/cmd/evener-hub                    2.652s
[exit 0]
$ PATH="/home/jesse/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.linux-amd64/bin:/home/jesse/go/bin:$PATH" /home/jesse/go/bin/golangci-lint run ./cmd/evener-hub/... ./internal/hubendpoint/... ./hubapi/...
0 issues.
[exit 0]
$ GOOS=darwin GOARCH=arm64 go test -c -o "$EVENER_SCRATCH_DIR/hubendpoint.darwin.test" ./internal/hubendpoint
[exit 0]
$ GOOS=darwin GOARCH=arm64 go test -c -o "$EVENER_SCRATCH_DIR/hostlock.darwin.test" ./cmd/evener-hub/internal/hostlock
[exit 0]
$ git diff --cached --check
[exit 0]
```

An earlier pinned lint invocation reported 10 issues (three unchecked closes, five `nilerr` findings, one append assignment, one static error); all were fixed before the final zero-issue run. The 64-KiB cap and malformed record/report rejection are directly tested. Linux real process `SIGKILL` + `Wait` leaves a stale record but releases flock; a new owner can replace it. The real running hub test checks the bound port is not zero and that graceful shutdown removes the record before the next `stopped` report. The occupied-port and held-lock test checks failure cannot publish a record or launch a second owner.

## Self-review and limits

Only the 21 task-owned source/test/guide files in the source commit and this requested report were changed. No client, remote manager, login URL, plan, spec, or ledger was edited; no new dependency was added. Discovery constructs only a token-free `/api/health` GET on a direct local socket, not a proxy, and tests assert a replacement server receives no Authorization header. File ownership checks use `syscall.Stat_t.Uid` on Linux/macOS; tests simulate a different UID rather than changing real ownership (no privileged `chown`). Linux listener/flock/process tests ran; macOS companion code compiled but could not run on this Linux host. Other platforms conservatively report unsupported ownership/lock inspection. Full repository tests and a live provider/real credential run were deliberately not attempted; those are outside this scoped unit. The example sentinel strings in tests are disposable and never read from real user files. Test roots isolate HOME/XDG and subprocesses strip inherited `EVENER_*` overrides except the harness's temporary-host-base setting.
