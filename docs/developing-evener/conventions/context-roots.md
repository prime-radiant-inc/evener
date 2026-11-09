# Context roots

A fresh `context.Background()` in a request or turn path means cancel and
shutdown never reach the work. Every non-test `context.Background()` or
`context.TODO()` is one of three things:

- **Root owner.** `main`, a CLI command, or a constructor that owns a daemon
  or server lifetime and cancels it on shutdown.
- **Detached on purpose.** Cleanup, close, hook and boundary-settle work that
  must finish even though the caller was cancelled. It carries its own bound
  (a timeout or budget) and a comment that says why it is detached.
- **Bug.** Code that runs on behalf of a caller (an appwire request, a tool
  call, a turn) while that caller's context, or the owning session's
  `sessionCtx`, was available to inherit.

A function without a `ctx` parameter that wraps a `...Ctx` variant is a
convenience root. Production callers that already hold a context call the
`...Ctx` variant.

`scripts/ops/list-context-roots.sh` lists every site and marks the ones whose
enclosing function takes a context (`--inherit`), so a review can classify
them. The marker is a heuristic; deciding between detached and bug is a read of
the code. There is no vet check: a detached site is legitimate, so a mechanical
rule cannot tell it from a bug, and the script is cheap enough to rerun.
