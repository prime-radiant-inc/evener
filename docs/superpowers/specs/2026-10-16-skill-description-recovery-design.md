# Tolerant skill descriptions

## Intent and approved behavior

Jesse chose automatic recovery of an unquoted description containing colon-space,
without editing installed files. Invocation controls remain strict. Healthy skills
remain usable. An unrecoverable skill produces a specific nonfatal skill warning,
not advice to diagnose the session daemon.

The reported `local-dev` and `sen` files both contain a description of the form
`description: LEGACY (previous assistant): ...`. The current YAML parser rejects
that line. Quoting only its value makes both files valid and preserves their
`user-invocable: false` flags. Discovery already continues after individual parse
failures, but the warning has no title or hint and receives generic failure copy.

## Scope

- Recover a single top-level, single-line, unquoted `description` value containing
  colon-space, only after ordinary YAML parsing fails and only when quoting that
  value makes the complete metadata mapping valid.
- Recovery must consume the whole frontmatter block as one YAML document. A
  document terminator followed by controls, garbage or another document is not
  recoverable. Existing successful strict-parser behavior remains unchanged.
- Preserve ordinary YAML semantics for already-valid inputs, including quoted
  descriptions, block scalars, comments, collections and nested metadata.
- Do not reinterpret YAML syntax such as quotes, collections, tags, anchors or
  block-scalar indicators as plain prose. Do not swallow continuation lines or
  invocation-control fields into the description.
- Keep validation of names, required description, invocation booleans and
  `allowed-tools` unchanged. A recoverable description never makes an invalid
  control valid. Duplicate keys and unrelated malformed YAML remain invalid.
- Preserve existing missing-opening-delimiter recovery. Combined malformed
  shapes outside that existing contract are not required for this change.
- Use the same validated interpretation during discovery and activation. Deliver
  the original body with existing line-ending normalization. Source identity and
  file digests still refer to the original bytes; no installed file is rewritten.
- Record successful description recovery as a machine-readable compatibility
  diagnostic, advisory at Full transcript detail through the existing code.
- Stamp all skill-discovery warnings with skill-specific guidance at their owner.
  Unavailable skills remain visible warnings at ordinary detail, not session
  failure events. Collision and informational diagnostics must not falsely say
  an available skill is unavailable.

## Surfaces and recovery

The shared Go skill parser owns this behavior for session startup, activation and
cold catalogs. The session owns warning payloads consumed by CLI, TUI, web and
native clients. No client-local parser or new frontend UI is needed.

A truly broken skill stays unavailable while healthy work continues. Correcting
its source restores it on the next existing discovery boundary (cold catalog
read or session startup). Existing retained-descriptor loading continues to
revalidate current bytes. This change adds no live filesystem watcher, retry
owner, metadata coercion, automatic source rewrite or daemon restart.

## Behavior evidence

1. Parser fixtures modeled on both reported headers fail before the fix and pass
   afterward with exact description data and `user-invocable: false` preserved.
2. Valid descriptions (plain, quoted, block scalar and commented), BOM and CRLF
   inputs keep their existing interpretation. Ambiguous or unrelated malformed
   shapes and invalid controls remain unavailable. Recovery refuses trailing
   controls or garbage after `...`, and any additional YAML document.
3. Real discovery and Load/Render deliver a recovered skill's body and preserve
   source bytes and digest. A healthy sibling remains available beside a truly
   malformed source. Repair followed by rediscovery restores that source.
4. A real Session with a scripted provider admits a recovered skill into an
   outgoing request and completes ordinary input despite an unrelated broken
   skill. Capture startup warnings and assert event kind, code, metadata and
   successful session work, never production message wording.
5. Trace warning payloads through the existing shared projection and verify
   supplied diagnostic fields survive. Review title/hint copy directly rather
   than asserting phrases in tests.

Update `docs/skills.md` and S18 in `docs/product/subsystems.md` with verified
behavior. Targeted tests and lint cover changed packages; CI owns the full suite.

## Workspace boundary

All changes belong to the isolated `wip-skill-description-recovery` worktree.
Leave `mobile-native/scripts/e2e-transcript.mts` in the main checkout untouched.
Do not edit plugin caches or restart/deploy the running hub.
