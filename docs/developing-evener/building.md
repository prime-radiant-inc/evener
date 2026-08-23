# Building

Build, distribution, and install targets. Their frontend dependency-install
prerequisite is shared with the frontend test gates: see [Frontend setup
boundary](testing.md#frontend-setup-boundary) in testing.md for what makes
`make test-web` and `make test-web-browser` deterministic only after that
install exists.

## The runtime pair always embeds a fresh SPA

`make build` (the default goal) is an alias for `make build-runtime`, which
depends on `build-web` rather than the other way around. That direction is
load-bearing, not stylistic: make guarantees a target's prerequisites
COMPLETE before its own recipe runs, even under parallel `make -j`, so
hanging `build-web` off `build-runtime` structurally guarantees every
evener/evener-hub pair build embeds the frontend `build-web` just produced.
No target may ship an evener hub subcommand with a stale or empty embedded web
UI — `install` depends on `build-web` directly, and `install-home` and
`install-system` inherit the same guarantee by depending on `install`.

`build-web`'s own build is deliberately unconditional — dist freshness is the
entire point, and `web-preflight` (the frontend dependency install, shared
with the test gates) is the only cacheable part. Vite's `emptyOutDir` wipes
the tracked `frontend/dist/PLACEHOLDER` on every build; `vite.config.ts`
writes it back at `closeBundle`, so nothing in the Makefile has to restore it
and `git status` stays clean after a build however Vite was invoked (kata
88nn).

## Runtime builds get a disposable process home

`scripts/ops/build-runtime-pair.sh` sources `scripts/lib/private-go-home.sh`
and prepares a disposable `HOME` beneath its own scratch root before it builds
anything, so a runtime build cannot write into the caller's home directory.
The caller owns and removes that root; the helper only creates state beneath
it and exports the environment for the commands that follow.

It is deliberately not an isolated *cache*. The helper preserves the caller's
reusable Go build and module caches and carries the persisted `go env`
settings across, so an isolated home does not cost a cold rebuild every time.
`make test`'s runner gives every Go and frontend stream the same treatment for
the same reason — see [Post-Merge Gate](testing.md#post-merge-gate) in
testing.md.

## Release builds

`make dist` runs goreleaser in snapshot mode — no tag, no publish — and
produces `dist/evener_<os>_<arch>.tar.gz` plus `checksums.txt`, the same
layout the release workflow ships and `install.sh` consumes. The web build
runs as goreleaser's `before` hook, so the release binaries embed a fresh SPA
the same way the ordinary runtime build does.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/building.mk, then run `make generate`. -->
| Command | Summary | What it proves | Trigger | Requires | Fails when |
| --- | --- | --- | --- | --- | --- |
| `make build` | Build the runtime pair (evener, evener hub) with a fresh embedded SPA. The default goal. | build-web completes before the evener/evener-hub pair is built, so the runtime pair contains the fresh SPA. | Local pre-merge; required CI together with make build-go. | Go, Node/npm, the frontend install, and enough disk. Build metadata includes the current SHA, dirty state, time, and channel. | Frontend preflight, build, or pair-script failure is nonzero; stale or failed embedding is not a pass. |
| `make web-preflight` | Ensure the frontend dependency install is present, healthy, and lockfile-compatible before any web target runs. | The worktree has a lockfile-compatible install and a real local TypeScript compiler. | Setup prerequisite for the web/build/browser gates. | May run npm when the install is absent or stale; refuses an unsafe npm ci through a mismatched shared symlink. | A missing, mismatched, or unhealthy install is nonzero; npm/network unavailability is a setup failure. |
| `make build-web` | Build the frontend (TypeScript typecheck + Vite production build) into frontend/dist for Go embedding. | TypeScript typecheck and the Vite production build complete and refresh frontend/dist. | Frontend CI; prerequisite of the runtime and release builds. | Node/npm; may run npm ci when the install is absent or stale. No provider credentials. Node's automatic compile cache is disabled. | An npm, typecheck, or Vite failure is nonzero. |
| `make dist` | Build release/distribution binaries with goreleaser in snapshot mode. | goreleaser builds evener, evener hub, evener tui, evener doctor, and evener migrate for linux/amd64 and darwin/arm64 into directory-wrapped archives plus checksums.txt, with a fresh SPA embedded via the before hook. | Release/snapshot CI; manual distribution verification. | Cross-compilation and frontend dependencies; release CI has networked setup for tool/dependency installation. | Any build, archive, inspection, or checksum failure is nonzero; unavailable release tooling blocks release. |
| `make build-go` | Compile every non-fuzz Go workspace module. | All packages in the seven GO_MODULES compile, including packages root-level `go build ./...` does not visit under go.work. | Required CI build job; local compile diagnostic. | Deterministic Go compilation; no provider calls or frontend/browser requirements. | Any module or package compilation failure is nonzero; the loop stops at the first failing module. |

### Other targets

| Command | Summary |
| --- | --- |
| `make build-runtime` | The actual recipe `build` runs: builds the evener/evener-hub pair via scripts/ops/build-runtime-pair.sh. |
| `make build-linux` | Cross-compile evener-linux-amd64 for Linux eval deployments. Starts by running `go clean -cache`, which wipes the whole Go build cache. |
| `make build-hub` | Alias for build-runtime. |
| `make build-tui` | Build the standalone TUI binary (evener tui). |
| `make build-doctor` | Build evener doctor, the read-only forensic inspector. |
| `make build-all` | Build every runtime binary: the runtime pair, the TUI, evener doctor, and evener-migrate. |
| `make build-llmcall` | Build the llmcall standalone CLI binary. |
| `make build-migrate` | Build evener-migrate. |
| `make install` | Install the runtime binaries into PREFIX (default ~/.local), building a fresh SPA first so the installed evener hub never embeds the tracked placeholder. |
| `make install-home` | Install into the user prefix (~/.local). |
| `make install-system` | Install into the system prefix (/usr/local). |
| `make test-install` | Integration-test the install path end to end: copy the tracked working tree into a fixture, run the install target with a synthetic HOME, and verify the installed binaries and symlinks. Skipped under -short. |
<!-- END GENERATED -->
