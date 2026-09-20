== panel acbd637a-308a-43a1-ae66-5d81f90cef7b head 6dacb1999 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=failed job=21575 verdict= chars=0
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=failed job=21576 verdict= chars=0
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=21577 verdict=0 chars=2828

######## member 0 (codex default)
<no output; error: quota: agent codex quota cooldown active>

######## member 1 (codex default)
<no output; error: quota: agent codex quota cooldown active>

######## member 2 (pi default)
## Summary

The series adds reconnect resilience to the native hub settings surfaces. It introduces a `fatal` flag on `HubConnection` (set only for a protocol close a retry can't fix), a small `connectionDisplay` decision helper (`none`/`banner`/`wall`) with an `everReady` ref, and extracts `ConnectionStatus` into its own module so a banner can be shown above `HubSettings`, `Plugins`, and `Providers` without unmounting the ready-only content during a flap. Tests cover the state-machine transition, the display decision, and the `Providers` screen's banner/wall behavior.

---

**Severity: medium** — `mobile-native/src/ProvidersScreen.tsx:104` (`onSignIn`) and `:577` (the OAuth action's `disabled` condition)

The diff removed the `state === "ready"` gate that previously kept the provider screen (and its instance modal) off-screen during a flap, but the OAuth "Sign in"/"Refresh sign-in" action is still only disabled on `surface.busy || stale`, not on connection readiness. Tapping it while the banner is up calls `flow.setConnection(client)` with a `client` that is frequently unusable:

- During the connect window created by a manual retry, a foreground/background transition, or an edited hub token, `useHubConnection` returns `client === null` (its `connectedFor` key no longer matches `targetKey`). `ProviderSignIn.setConnection(null)` is a no-op and `ProviderSignIn.start()` immediately returns because `!this.connection`. The sheet then renders phase `idle`, where `ProviderSignInSheet` exposes **no** action (the "Start again" button is gated to `error`/`expired`); when the connection later returns, the screen effect only calls `setConnection(client)`, which `schedule()` ignores for a non-`device` phase. The sheet is stuck with only Cancel until the user reopens it.
- During a passive `reconnecting` flap the `client` is non-null but the same; `start()` issues `deviceStart` on a client that rejects requests while reconnecting, and the screen effect then runs `setConnection(state === "ready" ? client : null)` → `null`, bumping the generation and overwriting the flow with "Sign-in could not be confirmed before the connection changed." So the flow fails immediately even though the banner is meant to cover that flap.

Concrete harm: a user who starts OAuth sign-in just as the connection is flapping gets either a dead modal with no way to proceed or a spurious connection-changed error, in both cases requiring a cancel-and-retry, whereas before the ready-gate this state was unreachable.

Suggested fix: gate the OAuth action on connection readiness (e.g. add `|| state !== "ready"` to the `disabled` at line 577 and/or guard in `onSignIn`), matching the old wall behavior; alternatively, have the screen effect / `ProviderSignIn.setConnection` start an `idle` flow once a usable connection arrives.
verification_snapshot_utc=2026-09-19T19:01:56Z
rawreviews_exit=0
