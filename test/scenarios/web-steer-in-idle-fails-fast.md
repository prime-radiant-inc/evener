# web-steer-in-idle-fails-fast: optimistic-rendering reject path

Verifies the failure side of the optimistic-rendering pattern (kata
`wymv`). When the user clicks "send as steer" against an IDLE
session, the daemon's `caps.Steer` is now gated on `processing` so
the hub returns `Unavailable` (`appwire.Unavailable("steer is not
available for this session")` — see
`cmd/serf-hub/app_rpc.go:ensureThreadActionAvailable`). The
renderer's `optimisticCall` wrapper marks the pending chip as
`.optimistic-failed` immediately with the rejection reason and a
Retry link — no silent drop, no spinning forever.

The button is disabled in IDLE in production; this scenario drives
the bug surface that would otherwise appear if a race re-enabled
it. Forcing `.disabled = false` from the page is the closest
deterministic reproduction.

Driver: superpowers-chrome:browsing.

## Pre-state

- Hub running on `0.0.0.0:9180` with `--serf` resolvable.
- OpenAI or Anthropic OAuth signed in.
- `~/.serf/auth-token` readable.
- Chrome session authenticated against the hub (visit
  `/auth?token=<auth-token>&next=/s/<sid>` once to set the cookie).

## Steps

```bash
tmpdir=$(mktemp -d -t serf-e2e-steer-idle-XXXXX)
TOKEN=$(cat ~/.serf/auth-token)
HUB=http://localhost:9180
```

1. **Spawn a short session** and let it run to IDLE. Use Haiku and
   a trivial prompt so the turn finishes in seconds:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"please run \\\"echo hello\\\" via exec_command then stop\",\"model\":\"anthropic/claude-haiku-4-5-20251001\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['session_id'])")
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 1
   done
   echo "state=$state"
   ```

2. **Authenticate the browser** and open the workspace via the
   browsing skill:
   ```
   navigate http://127.0.0.1:9180/auth?token=<TOKEN>&next=/s/<SID>
   await_element [data-steer-trigger]
   ```

3. **Force-enable the steer button** (it is disabled in IDLE; we
   drive the bug surface that would appear if the button were
   enabled by a race), type a steer body, and click:
   ```javascript
   document.querySelector("[data-steer-trigger]").disabled = false;
   const ta = document.querySelector("textarea.message-input");
   ta.focus(); ta.value = "this steer should fail visibly";
   ta.dispatchEvent(new Event("input", { bubbles: true }));
   document.querySelector("[data-steer-trigger]").click();
   ```

4. **Wait for the failed chip** to render:
   ```
   await_element .optimistic-failed
   ```

## Expected

- A `.optimistic-failed` element appears within ~200 ms of the
  click. (The pending chip is rendered synchronously by
  `SerfAppwirePending.register` in `pending.js`; when the appwire
  request rejects with the `Unavailable` error, `optimisticCall`
  calls `fail(handle, err.message)`. There is no 10 s timeout
  wait — the daemon rejects on the same RTT.)
- The chip's `.optimistic-failed-reason` text contains
  `"not available"` (substring of `"steer is not available for
  this session"`).
- A `.optimistic-retry` link is present inside the chip.
- The transcript pane has the same number of authoritative
  `details.steering` entries before and after the click (the
  failed chip is the pending-placeholder; it never gets promoted
  to an authoritative entry because no `serf/steering/injected`
  notification arrived).

Falsification:

- The chip stays `.optimistic-pending` for ~10 s, then flips to
  `.optimistic-failed` with `"server did not confirm"` — means the
  daemon swallowed the request rather than rejecting it (a
  `caps.Steer` gating regression).
- A new `details.steering` element appears in the conversation
  pane — means the daemon accepted the steer despite IDLE
  (capability regression on the daemon).
- The page shows an `error` banner with `title="Hub steer error"`
  but no `.optimistic-failed` chip — means the renderer regressed
  and is rendering the old error path instead of routing through
  `SerfAppwirePending.fail`.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```
