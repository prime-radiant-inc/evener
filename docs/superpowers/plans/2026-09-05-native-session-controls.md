# Native session controls

Add a Session button in the conversation navigation bar. It opens a native sheet with the session name, hub, model provider, and capability-gated controls for renaming, compacting context, and stopping the runtime. Use the established native typography and quiet separated groups. Keep the conversation draft untouched.

Rename uses the existing shared service and refreshes the authoritative conversation; update the navigation title from that projection. Compact explains context summarization and reports a request acknowledgement, not completed compaction. Stop runtime requires an in-app confirmation describing interruption and retained history. After acknowledgement, close the conversation binding and return to the session list without rereading the stopped session (a read could resume it).

A conversation-owned action controller prevents duplicate concurrent submissions and ignores completions after its binding is disposed. Sheet dismissal does not erase in-flight action state. Errors remain explicit; no automatic mutation replay. Existing shared service capability checks remain authoritative at dispatch. Changing destinations disposes the controller before any old confirmation can dispatch.

Test controller behavior at the protocol boundary: rename projection, unavailable actions, failed response without replay, overlapping submissions, disposal, and shutdown without a post-shutdown read. Build and manually exercise both native platforms against the isolated real Evener hub. Verify keyboard, dismissal, rename persistence and runtime stop/resume. Record compaction acceptance separately from actual completion.


Implementation and initial manual acceptance are recorded in `docs/design/mobile/session-controls-evidence.md`. The reconnect ownership review finding is fixed and covered by a regression test. Native injected network-failure and accessibility/device acceptance remain open. Reopened history can stay cold; the UI labels that state and hides redundant shutdown. Compaction can resume a runtime and therefore refreshes afterward, while shutdown does not.
