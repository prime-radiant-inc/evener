Native deferred mutations now check the live connection and hub/client ownership when the action runs, preserving readable retained screens without sending a write through a disconnected or replaced client.

Stacked on #1952. #1922 supplies the required reconnect read recovery, distinct not-ready behavior, and retries within retained form modals. This replaces the mutation-readiness portion of #1915; the qualified stack is intended to land together.

Refreshed head `335d517c7f8bc2c1b699ba865cdd172d56721934` preserves the complete reviewed own patch byte-for-byte, SHA-256 `68947c899cae1695d2ee6c226938a53ebe53ac7e5be7f132ec7631ec05c9ece4`. Independent review/simplify and local branch review passed with the documented successor dependencies. The refreshed stack passed 64 focused native tests, native typecheck, import lint, and diff checks; new exact-head CI is required. Measured cross-hub allegations are refuted by the scoped live hooks. Separate Lows remain in #1942.
