# Notify rollout

Goal: batch notifications before they go out, then ship notify v0.4.

## Tasks

1. Add `Size(m Message) int`, returning the body's length in bytes. Add tests.
2. Add `Dedupe(msgs []Message) []Message`, which keeps the first message to each recipient and drops later ones, preserving order. Add tests.
3. Deploy notify v0.4 to production.
