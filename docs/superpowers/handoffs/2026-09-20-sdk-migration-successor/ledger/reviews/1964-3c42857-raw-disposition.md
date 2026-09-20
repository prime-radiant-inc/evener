# Current raw panel disposition: #1964 at3c42857f

All three current panel members completed. Muse and DeepSeek found no issue. Luna alleged Medium exposure because the native projection carries raw hubError alongside sanitized error.

Refuted: this is a native in-process snapshot, not an exported wire payload. Production reads of hubError/loadError are in nativePreferences.ts and NativePreferencesProvider.tsx and feed keybindingsErrorMessage. That helper emits fixed user-facing hub/load messages. KeybindingPreferencesScreen renders domain.error, never raw hubError; its loadError check is boolean. The raw diagnostics already exist in the same-privilege shared store. No new rendering, logging, persistence, or external exposure path was identified. Keeping source diagnostics is necessary to preserve unrelated hub/load errors during draft-only reconciliation. No speculative security rewrite is warranted.

The separately confirmed same-client disconnected projection gap is implemented in #1988 ataf83b51 after independent and local2625 reviews passed. The screen consumer is #1904. These are explicit next slices, not dismissed findings. Accepted genuine-storage uncertainty and copy/coverage Lows remain1941/1948.
