# Native iOS session management

Continue the accepted delivery plan with existing AppWire v4 operations and
native menus. iOS is the v1 delivery target; preserve Android sources.

## Contracts and boundaries

- Ordinary fork uses `thread/fork` with `ref`, selected `sourceTurnId`, and
  optional edited input/label/model fields. Its response contains `thread` and
  optional `originalInput`, with no navigation receipt. Navigate to the returned
  child only after acknowledgement. Preserve the existing aside workflow.
- Session deletion uses `evener/session/delete { ref }`. Its response contains
  `deleted`, `skipped`, and `navigation`, with no `ok` field. Explain skipped
  live sessions and confirm actual deletion before leaving the conversation.
- Pin assignment/unpin and section rename/delete return `ok`, `changed`, and a
  navigation receipt. Reuse `NavigationActions` and `NavigationPages`; do not
  create another organization controller. A section deletion removes the section
  while keeping sessions.

Source authority: generated protocol types and web rail actions under
`cmd/evener-hub/frontend/src/`. Server responses remain authoritative; do not
optimistically remove or repin rows. Never replay a mutation after a lost reply.

## Implemented controller slice

`NavigationActions` now offers assignPin, unpin, renamePinSection and
deletePinSection alongside archive/favorite. It serializes mutations, blocks
further writes while uncertain, and terminates pending state when scope changes.
A read-only reconcile callback clears uncertainty only after successful current
navigation refresh. `ProjectsScreen` wires existing refresh controls to this
callback and checks the page model's error state.

Ten deterministic tests cover payloads, acknowledged no-ops, delayed mutation
receipts and refreshes, disposal/current-scope fencing, failed reconciliation,
and no replay. The integrated native gate passed 402 tests plus typecheck during
this slice. These checks do not qualify the missing pin UI or durable mutation
recovery across model disposal.

## Remaining implementation

- [ ] Add native pin catalog/section destinations and accessible menus for
  assigning an existing section, naming a new section, unpinning, renaming and
  deleting a section. Consume the shared controller; preserve paging/reveal.
- [ ] Add selected-turn ordinary fork with editable input and truthful uncertain
  outcomes. Reuse conversation services for returned child identity.
- [ ] Add ended-session deletion with confirmation and server skipped results.
  Refresh from its navigation receipt without assuming an `ok` field.
- [ ] Preserve unfinished edits and uncertain mutation targets across screen/hub
  replacement and process death; current controller lifetime fencing alone
  does not establish that requirement.
- [ ] Run deterministic response and lifecycle tests for these UI integrations.
- [ ] On the owned scripted hub, execute fork, delete, pin/unpin, and section
  create/rename/delete from iOS and independently read resulting server state.
- [ ] Qualify lost replies, overlapping hub/session identities, large text,
  VoiceOver and iPad. Record actual outcomes rather than inferring acceptance
  from controller tests.
