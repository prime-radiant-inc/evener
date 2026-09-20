# #2005 raw Low follow-ups

Recorded 2026-09-19 from current full head `74ef4039276e846958771ab14bb6bdd6aec63210` and complete raw receipt `reviews/raw/2005-74ef40392-remote.md`.

- Native timeline parity Low: `interrupted-salvage` is absent from `mobile-native/src/timeline.ts` label/gap handling and the timeline table cases. Filed as [#2015](https://github.com/prime-radiant-inc/evener/issues/2015). Scope is the `Interrupted draft` label, deliberate routine-gap classification, and focused table coverage.
- TUI fixture-quality Low: `cmd/evener-tui/question_overlay_test.go` names a salvage fixture but `transcript.ChatMessage` has no steering kind, so the assertion duplicates the existing kind-agnostic steering non-resolution test. Filed separately as [#2016](https://github.com/prime-radiant-inc/evener/issues/2016). Clarify or consolidate coverage; do not widen TUI types just for this review.
- The raw interrupt-marker live/durable state-consistency Medium remains separate and unresolved. It is not folded into either Low or marked fixed.

PR disposition was posted at [#2005 comment 5745231859](https://github.com/prime-radiant-inc/evener/pull/2005#issuecomment-5745231859). Duplicate searches found no existing tracker for either exact Low.
