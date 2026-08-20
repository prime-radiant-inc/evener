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
No target may ship an evener-hub binary with a stale or empty embedded web
UI — `install`, `install-home`, and `install-system` inherit the same
guarantee by depending on `install`, which depends on `build-web` directly.

`build-web`'s own build is deliberately unconditional — dist freshness is the
entire point, and `web-preflight` (the frontend dependency install, shared
with the test gates) is the only cacheable part. Vite's `emptyOutDir` wipes
the tracked `frontend/dist/PLACEHOLDER` on every build; `vite.config.ts`
writes it back at `closeBundle`, so nothing in the Makefile has to restore it
and `git status` stays clean after a build however Vite was invoked (kata
88nn).

## Release builds

`make dist` runs goreleaser in snapshot mode — no tag, no publish — and
produces `dist/evener_<os>_<arch>.tar.gz` plus `checksums.txt`, the same
layout the release workflow ships and `install.sh` consumes. The web build
runs as goreleaser's `before` hook, so the release binaries embed a fresh SPA
the same way the ordinary runtime build does.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/building.mk, then run `make generate`. -->
<!-- END GENERATED -->
