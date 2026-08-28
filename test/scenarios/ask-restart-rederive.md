# ask-restart-rederive: restart re-derives an unanswered ask

This scenario preserves the restart/rederive contract without directing an
operator to retired daemon REST control routes.

## What to verify

1. Through the hub, start a session whose first turn issues one `ask_user`
   question and wait for the typed thread state to become `awaiting`.
2. Find the daemon PID from its rendezvous entry and terminate that exact PID.
   Do not use a daemon HTTP control route or a broad process match.
3. Resume the session through the hub's typed AppWire flow. On the first
   `thread/read` response, the state must already be `awaiting`; it must
   never transiently report idle.
4. Answer through the current ask dock. The hub resumes via `turn/start`;
   confirm the answer and subsequent assistant turn are in the transcript.

The deterministic proof is
`cmd/evener/serve_ask_test.go#TestServeAsk_RestoreReportsAwaitingImmediately`.
It uses real daemon AppWire control paths and asserts the same immediate
re-derivation contract. The browser interaction belongs to
`test/scenarios/ask-web-answer.md`.

## Cleanup

Terminate only the exact PID recorded in the rendezvous entry, then remove the
scenario-owned temporary directory. Do not use deleted daemon control routes.
