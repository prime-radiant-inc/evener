# web-steer-no-active-turn-guard: idle Shift+Enter is blocked by the Composer

**What this covers**: the Composer's client-side no-active-turn guard. On an
idle session, a human's Shift+Enter steer attempt is rejected before any RPC
is sent, the typed text remains in the composer, and a visible toast explains
that there is no active turn.

AppWire v3 intentionally accepts an idle `turn/steer` from callers that reach
the daemon: it reserves a `turn_mN` and runs a steering-carrier turn. That is
a separate acceptance invariant and is not covered by this card. This card
covers only the browser guard; it does not imply that the TUI and web pin the
same daemon contract.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web
UI" and "The REST surface, and what is no longer on it". There is no REST
route for steer; this card verifies that the Composer does not make the
AppWire request in the first place.

## Pre-state

- Hub running on an isolated `$HOME` and free port (never `9180`, Jesse's
  real one — see the Setup checklist in
  `docs/developing-evener/agentic-testing.md`) with `--evener` resolvable.
- `$MODEL` set to a model ID the isolated hub can launch, including any
  provider configuration or credential that launch validation requires. This
  scenario starts no model turn and makes no completion request.
- `$HOME/.evener/auth-token` readable (that isolated `$HOME`).
- The SPA must be built (`make build-web`) before the hub, or the browser gets
  a placeholder.

## Steps

```bash
tmpdir=$(mktemp -d -t evener-e2e-steer-idle-XXXXX)
TOKEN=$(cat "$HOME/.evener/auth-token")
HUB=http://127.0.0.1:$PORT
```

1. **Spawn a dormant session.** A blank prompt establishes the exact idle,
   no-active-turn precondition without issuing a model completion:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"\",\"model\":\"$MODEL\",\"working_dir\":\"$tmpdir\",\"harness\":\"evener\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | jq -r '.session_id')
   detail=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
   echo "$detail" | jq '{state, active_turn_id, steer: .capabilities.steer, queue: .capabilities.queue}'
   ```

2. Navigate to `/auth?token=$TOKEN&next=/s/local:$SID` and wait for
   `[data-testid="composer-input-card"]`.

3. Type text into the composer, then press **Shift+Enter**. The Steer button
   does not render in an idle session, but the chord still reaches
   `handleSteerClick` (`cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx#handleSteerClick`).
   ```javascript
   (async () => {
     const before = document.querySelectorAll(
       '[data-testid="user-message-item"]:not([data-opens-exchange])').length;
     const ta = document.querySelector('[data-testid="composer-input-card"] textarea');
     const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
     setter.call(ta, "this steer should remain visible");
     ta.dispatchEvent(new Event("input", { bubbles: true }));
     ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true }));
     await new Promise((r) => setTimeout(r, 1000));
     return JSON.stringify({
       port: location.port,
       steerButton: !!document.querySelector('[data-testid="composer-steer"]'),
       toast: document.querySelector('[aria-label="Notifications"]')?.textContent,
       textAfter: ta.value,
       chips: document.querySelectorAll('[data-testid="pending-chips"] li').length,
       steersBefore: before,
       steersAfter: document.querySelectorAll(
         '[data-testid="user-message-item"]:not([data-opens-exchange])').length,
     }, null, 2);
   })()
   ```

## Expected

- **Step 1**: `state` is `idle`, `active_turn_id` is empty, and
  `capabilities.steer` is **false**. The capability is a UI hint; the daemon
  itself accepts an idle steer through its carrier-turn path.
- **Steps 2-3**: no `[data-testid="composer-steer"]` button exists; the
  toast region contains `Steer failed: no active turn`; the composer text is
  unchanged; `pending-chips` is empty; and the steer count is identical before
  and after. The Composer returns before `submitAction`, so no RPC is sent.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```

## Sharp edges

- **AppWire frames are not part of this card.** A direct idle AppWire steer is
  intentionally accepted in v3 and may start a steering-carrier turn. A
  carrier-acceptance scenario would be a separate card.
- The dormant spawn is only a deterministic way to establish `state=idle`
  with no active turn. Its own zero-turn contract is covered by
  `spawn-empty-prompt-starts-dormant.md`.
- **Shift+Enter is only the Steer chord while `enterToSend` is off** — with
  `evener.prefs.enterToSend` on, Shift+Enter inserts a literal newline.
- React's controlled textarea ignores a plain `ta.value = "..."` assignment;
  use the native value setter plus an `input` event, as the snippet does.

**Composer observation verified live 2026-07-31** against the then-current card
on an isolated `$HOME` and kernel-assigned port. **Source, unit, and docs
contracts were re-audited 2026-08-26**; no new live run is claimed here. The
daemon acceptance behavior is recorded in issue #171 and intentionally not
asserted in this card.
