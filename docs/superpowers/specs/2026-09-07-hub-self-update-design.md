# Hub self-update from Settings

Date: 2026-09-07

## Goal

From Settings → Hub, a user can see whether a newer evener build exists on
their channel, pick the channel (release or snapshot), and click one button
that downloads the build, installs it, and replaces the running hub process
with it. No terminal, no supervisor knowledge required.

## What already exists

- `internal/selfupdate.Upgrade` downloads `evener_<os>_<arch>.tar.gz` from the
  GitHub release for a channel (`latest` or `snapshot`, or a `v*` tag),
  installs `evener` and `evener-dev` into `<prefix>/share/evener/bin`, and
  symlinks them from `<prefix>/bin`. Default prefix `~/.local`.
- The hub exposes that as the `evener/upgrade` RPC; the command palette's
  "Upgrade Evener" calls it and toasts "restart evener". Neither restarts.
- `buildinfo` carries `GitSHA` (goreleaser `ShortCommit`), `Channel`
  (`release`, `snapshot`, or empty = dev). `UpgradeChannel()` maps dev to
  release.
- `.github/workflows/binaries.yml` builds on every push to main, force-moves
  the `snapshot` tag to that commit, and re-uploads the archives to the
  `snapshot` prerelease. Verified 2026-09-07: the last five main pushes each
  refreshed the snapshot within ~3 minutes; `refs/tags/snapshot` ==
  `origin/main`. No workflow change is needed.
- The repo is public, so unauthenticated GitHub API calls work (60/hour).

## Decisions

- **Restart mechanism: exec-in-place.** After a successful install the hub
  `syscall.Exec`s the freshly installed `evener` binary with its own original
  argv (`os.Args[1:]`) and environment. The PID is unchanged, so launchd,
  systemd, and a bare shell all keep the process they had. Go opens files and
  sockets `O_CLOEXEC`, so `hub.lock` (flock) and the listener are released by
  the exec and re-acquired by the new image; Go listeners set `SO_REUSEADDR`
  so the rebind does not wait on TIME_WAIT. Spawned session daemons are
  already detached and survive (they keep the old binary until their sessions
  restart, as documented).
- **Dev builds are excluded.** When `buildinfo.BuildChannel() == "dev"` the
  check returns "not applicable" without touching the network, apply is
  refused, and Settings shows a note instead of the controls. A worktree
  build must never be silently replaced by a release binary.
- **No stored channel setting.** The selector defaults to the running
  binary's `UpgradeChannel()`. After an update, the installed binary's baked
  channel becomes the new default. The installed binary is the state.
- **Update detection is by commit.** `updateAvailable` is true when the
  channel's commit SHA does not have `buildinfo.GitSHA` as a prefix. A tag
  moved to a different commit, or a newer release tag, both read as an
  update; a rebuild of the same commit does not.

## Backend

### `internal/selfupdate`

New file `check.go`:

```go
type CheckOptions struct {
    Channel    string        // "release" or "snapshot" (required)
    CurrentSHA string        // buildinfo.GitSHA
    RepoURL    string        // default https://github.com/prime-radiant-inc/evener
    APIURL     string        // default https://api.github.com; tests point it at httptest
    HTTPClient *http.Client  // default: 10s timeout client
}

type CheckResult struct {
    Channel         string `json:"channel"`
    LatestTag       string `json:"latestTag"`    // "snapshot" or "vX.Y.Z"
    LatestCommit    string `json:"latestCommit"` // full SHA from GitHub
    UpdateAvailable bool   `json:"updateAvailable"`
}

func Check(ctx context.Context, opts CheckOptions) (CheckResult, error)
```

- `snapshot`: `GET {api}/repos/{owner}/{repo}/commits/snapshot` → `.sha`.
- `release`: `GET {api}/repos/{owner}/{repo}/releases/latest` → `.tag_name`,
  then `GET .../commits/{tag}` → `.sha`.
- owner/repo are parsed from `RepoURL` (the existing `defaultRepoURL`).
- Any other channel value is an error. Non-2xx responses surface as an error
  carrying the status and the GitHub `message` field when present (rate
  limit responses say so).
- `UpdateAvailable = CurrentSHA == "" || !strings.HasPrefix(LatestCommit, CurrentSHA)`.
  The caller handles dev builds before calling; an empty `CurrentSHA` here
  is a defensive "assume stale".

New file `restart_unix.go` (build tag `unix`):

```go
// Restart replaces the current process image with binary, keeping argv[1:]
// and the environment. It only returns on failure.
func Restart(binary string, args []string) error
```

Implemented as `syscall.Exec(binary, append([]string{binary}, args...), os.Environ())`.
The non-unix file returns an "unsupported platform" error.

### `appwire`

Catalog entries in `protocol.go` (regenerate with `make generate`):

- `evener/update/check` `UpdateCheckParams{Channel string}` →
  `UpdateCheckResponse{Channel, BuildChannel, CurrentVersion, CurrentCommit,
  LatestTag, LatestCommit string; UpdateAvailable, Applicable bool}`.
  `Applicable=false` and `BuildChannel="dev"` for dev builds; the other
  latest-* fields are empty then.
- `evener/update/apply` `UpdateApplyParams{Channel string}` →
  `UpdateApplyResponse{Release, Channel, Installed []string; Restarting bool}`.

Both `ScopeHub`. `SettingsHubOverview` gains `BuildChannel string
json:"buildChannel"` filled from `buildinfo.BuildChannel()`.

### `cmd/evener-hub`

New file `app_update.go`:

- `hubUpdateCheck(ctx, params)`: empty channel → `buildinfo.UpgradeChannel()`.
  Dev build → `Applicable:false` response, no network. Otherwise
  `selfupdate.Check` via a package var `runHubUpdateCheck` (test seam).
- `hubUpdateApply(ctx, params)`: dev build → error "dev build; rebuild with
  make build-hub". Otherwise `runHubSelfUpgrade` (the existing seam) with
  `Requested: channel`, then `scheduleHubRestart(ctx, binary, args)`, where binary is
  `result.Installed[0]` (the installed `evener`) and args is
  `hubProcessArgs()[1:]`. Returns `Restarting: true`.
- `scheduleHubRestart` is a package var. The real one registers the restart
  with `appserver.AfterResponseWritten(ctx, ...)`, so it runs only once the
  apply response has reached the transport (or the connection tore down
  without it) and the browser has the success result it needs; it then starts
  a goroutine that waits a 500ms flush grace and calls `selfupdate.Restart`.
  A context with no appserver connection, or one whose send loop has already
  stopped, restarts immediately. On failure it
  logs to stderr with the `[hub]` prefix used elsewhere and the hub keeps
  running on the old binary.
- Registered in `app_rpc.go` next to `MethodEvenerUpgrade`.
- The existing `evener/upgrade` RPC and the palette command are unchanged.

## Frontend

### `stores/hubUpdate.ts`

Zustand store, same shape as the other RPC-backed stores:

```
channel: "release" | "snapshot" | null   // null until overview loads
check: UpdateCheckResponse | null
checking: boolean
checkError: string | null
applying: boolean
applyError: string | null
restarting: boolean
restartTimedOut: boolean
setChannel(c), check(), apply()
```

- `check()` calls `evener/update/check` with the selected channel.
- `apply()` calls `evener/update/apply`; on success sets `restarting` and
  starts a health poll: `GET /api/health` every 1s until the returned
  `version` differs from the pre-update version, then `location.reload()`.
  After 30s, `restartTimedOut` is set and polling stops. Health fetch failures
  during the poll are expected (the hub is mid-restart) and are swallowed.
- Poll timer and `fetch` are injectable so tests use fake timers.

### `panes/settings/sections/hub.tsx`

An "Updates" block after the existing three fields:

- Row "Build": `version (commit) · channel` from the overview.
- Dev build: a dim note "Dev build. Rebuild with `make build-hub` to update."
  Nothing else.
- Otherwise: a `RadioGroup` (existing widget) "Channel" with Release /
  Snapshot; the `check()` result line ("Up to date on snapshot (be70029)" /
  "Update available: snapshot be70029, running 3b1c5f8" / the error);
  buttons "Check for updates" and "Update and restart" (the latter enabled
  only when `updateAvailable`). "Update and restart" opens the existing
  `ConfirmDialog` ("The hub will go offline for a few seconds. Sessions keep
  running."). While `restarting`: "Restarting hub…" with a loader; on
  `restartTimedOut`: "The hub didn't come back within 30s. Check its logs."
- `check()` runs once when the section mounts with the default channel, and
  again when the channel changes.

## Release housekeeping

- Delete the stale `serf_darwin_arm64.tar.gz` and `serf_linux_amd64.tar.gz`
  assets from the `snapshot` release (`gh release delete-asset snapshot <name>`).
  One-off; not part of the code change.

## Testing

Default tests stay offline (AGENTS.md).

- `internal/selfupdate/check_test.go`: httptest server standing in for the
  GitHub API; cases: snapshot up to date, snapshot stale, release resolves
  tag then commit, 403 rate-limit surfaces the message, unknown channel,
  empty CurrentSHA.
- `internal/selfupdate/restart_unix_test.go`: `Restart` into a helper
  process (`os.Args[0]` with a `GO_WANT_HELPER` env, the stdlib pattern)
  checks argv and that the PID is preserved; `Restart` on a nonexistent path
  returns an error.
- `cmd/evener-hub/app_update_test.go`: dev build → not applicable / refused
  without calling the seams; check passes the channel through and fills
  current fields; apply calls upgrade then schedules restart with
  `Installed[0]` and `hubProcessArgs()[1:]`; upgrade failure → no restart
  scheduled; default channel = `UpgradeChannel()`.
- `appwire` golden/catalog tests pick up the new methods via `make generate`
  and the existing golden update flow.
- Frontend: `hubUpdate.test.ts` (scripted client: check, apply, health poll
  to reload, timeout) and `hub.test.tsx` (dev / up to date / available /
  restarting / timed out renderings, confirm dialog gating apply).
- Opt-in e2e `EVENER_UPDATE_E2E=1` in `cmd/evener-hub`: build this branch's
  own `evener` binary as a `snapshot`-channel build (so it isn't refused as a
  dev build), start it as a hub on `127.0.0.1:0`, call `evener/update/apply`
  over appwire against the real public snapshot release, and assert the PID
  is unchanged, `/api/health` comes back, and its version differs from the
  branch build's. This is the only test that touches GitHub; it downloads
  the current public snapshot but does not depend on that release carrying
  this feature, since the hub under test is built locally.

## Docs

`docs/evener-hub.md` gets an "Updating the hub" subsection: the Settings
flow, the dev-build carve-out, exec-in-place and why it is supervisor
agnostic, and a cross-link to the note that daemons keep their old binary.

## Out of scope

- Periodic background update checks.
- Re-pointing the palette "Upgrade Evener" command at the new flow.
- Windows.
- Rollback. A failed download or install leaves the old binary running; a
  failed exec logs and leaves the old binary running.
