# Failed-turn continuation journey

`TestRetryContinuationJourney` starts a real hub and daemon, fails the first
provider request, and presses each shipped client's Retry action. It verifies
that the provider retains the original context and receives one continuation,
that the transcript contains one original and one continuation, and that an
unsent draft survives. The lost-reply case drops a real acceptance reply and
closes the socket. The refusal case sends a wrong instance precondition once,
then uses the client's explicit recovery action.

The browser journey additionally runs three integrated scenarios. `eligibility`
holds the acceptance response, authoritative reads and the new turn's
history/overlay events independently, presses Retry twice in one task, and
proves one durable mutation identity and no duplicate provider continuation.
It also proves a newer failure offers its own Retry while the older failure
stays readable without one. `recovery` loses the acceptance reply,
reloads while the durable pending read is held unknown and the first
authoritative history read fails once, then proves automatic reconciliation,
one mutation identity, a completed continuation and the documented draft and
attachment lifetimes. `history` creates more than one newest page of turns,
pages to an older failure through the real older-history owner, and repeats
that after a reload.

`running` starts from a still-latest failure whose Retry is enabled, then holds
history and overlay events while a newer turn's running identity arrives, and
asserts the action is gone before any transcript item exists. `remount` holds
the durable outbox write for a Retry continuation, switches the session away
and back in the same document, releases the write, and asserts one mutation
identity with no stranded optimistic owner and a preserved draft.

The holds live at external boundaries only: WebSocket frames (including
independently released RPC responses and notifications) and IndexedDB read
completion. No hub reply, eligibility decision, mutation row or transcript item
is fabricated.

From the repository root, with frontend, native and AppWire TypeScript
dependencies installed:

```sh
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestRetryContinuation' -count=1 -v
```

Chrome must be available through the existing browser-guard setup. Missing
prerequisites fail the test. CI runs the journey after `make test-web-browser`.
To isolate a client, use `^TestRetryContinuationJourney$/browser` or
`^TestRetryContinuationJourney$/native`. On Linux, the driver controls also
verify cancellation cleanup, preserved exit errors and bounded forced stop
with real Node processes and the browser lifecycle.

The browser uses the production bundle. Native uses the actual conversation
screen, connection provider, shared conversation service, durable mutation
runtime and draft document with React Native's test renderer. Its platform
adapters stand in for React Native, navigation and Expo services on Linux.
This is not a simulator or physical-device test. The LLM provider is scripted
at its HTTP boundary; no provider credentials or live model calls are used.

The Go fixture owns process cleanup and private connection files. Drivers do
not print credentials. `wireFault.mjs` intercepts only the external WebSocket
boundary and never fabricates a hub response.
