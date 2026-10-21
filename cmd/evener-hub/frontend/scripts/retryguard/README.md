# Failed-turn continuation journey

`TestRetryContinuationJourney` starts a real hub and daemon, fails the first
provider request, and presses each shipped client's Retry action. It verifies
that the provider retains the original context and receives one continuation,
that the transcript contains one original and one continuation, and that an
unsent draft survives. The lost-reply case drops a real acceptance reply and
closes the socket. The refusal case sends a wrong instance precondition once,
then uses the client's explicit recovery action.

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
