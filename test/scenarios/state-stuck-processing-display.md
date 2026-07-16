# state-stuck-processing-display: failed turns return the owning session to idle

**What this covers**: a recoverable provider/stream failure must finish the
active turn boundary and return the live `serf serve` session to `idle`. The
daemon's `/status` response is the owning source of truth. Hub readers must not
infer failure state from transcript tails or scan the private API log to invent a
replacement status.

## Deterministic gate

Run the scripted-provider contracts:

```bash
go test ./agent -run 'TestSession_(StreamErrorReturnsSessionToIdle|ProviderAbortKeepsSessionIdle)' -count=1
```

The first test closes a successful stream open before any finish event and
asserts the exhausted error path leaves `Session.State()` at `SessionIdle`. The
second asserts the same boundary for a provider abort. These are deterministic
Serf plumbing tests and require no provider credential or network access.

## Optional live check

If a real `serf serve` session encounters a provider error naturally:

1. Capture the SID from `GET /status` before sending input.
2. Send the turn and wait for the request to return its error.
3. Read `GET /status` from the same daemon again.
4. Open the Hub workspace only after confirming the daemon response.

## Expected

- Both deterministic tests pass.
- After a recoverable live failure, the owning daemon reports `state: "idle"`;
  it does not remain `processing`.
- Hub projections follow the owning status and keep steer/send available for a
  subsequent turn.
- The semantic transcript remains readable. Provider-attempt diagnostics are
  available separately through `serf-doctor apilog <SID> --errors` or explicit
  API-log expansion, but neither artifact determines the live state.

Falsification: the daemon still reports `processing` after the failed request
has returned, or a Hub view changes state by interpreting transcript/API-log
contents instead of the daemon's owning status.

## Cleanup

- Shut down any daemon used for the optional live check.

## Sharp edges

- Do not manufacture a stale state by killing the daemon and then classify a
  historical transcript. That exercises dead-process presentation, not the
  serve-owned failure boundary this scenario specifies.
- A provider error is not deterministic enough for the default suite. Keep the
  scripted-provider tests as the required gate; the live check is opportunistic.
