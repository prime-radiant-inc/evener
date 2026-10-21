# Task 1a report: stable workspace identity

## Result

The identity-only slice is complete in commit `f3a1aeca8f726857263327d23e2328176d43588a`. The prior stop had no technical blocker: I failed to decompose the original broad assignment and stopped after only the metadata field/accessor. This focused brief supplied the right scope; no additional decision or authority was needed.

A new root uses its own ID as `WorkspaceID`; the runtime value is saved in metadata and omitted from config snapshots. Legacy metadata missing `workspace_id` adopts its own saved session ID. Restore persists that fallback through the existing session metadata save owner before emitting the restored start event or returning the session to serve. Nonempty saved values are retained. Clear keeps the same workspace reference while changing the session instance ID, and fork metadata starts with the fork's own ID. Serve prepares and serves the public reference from `Session.WorkspaceID()`.

## Changed paths

- `agent/schema/snapshot.go`
- `agent/schema/session_workspace_identity_test.go`
- `agent/session.go`
- `agent/session_config.go`
- `agent/session_init.go`
- `agent/session_state.go`
- `agent/fork.go`
- `agent/session_workspace_identity_test.go`
- `cmd/evener/serve.go`
- `cmd/evener/serve_workspace_identity_test.go`

## Red/green evidence

The filtered agent suite first failed on both missing behaviors:

```text
--- FAIL: TestWorkspaceIdentityLegacyMetadataIsPersistedBeforeRestoreReturns
    session_workspace_identity_test.go:44: persisted workspace ID = "", want saved session ID "034jpkKDayZSbZrk6KGVeb"
--- FAIL: TestWorkspaceIdentityForkStartsAtChildID
    session_workspace_identity_test.go:59: fork workspace ID = "", want child ID "034jpkKPC7p1jzjeE87stL"
```

The Clear test first failed when its one propagation line was temporarily removed:

```text
--- FAIL: TestWorkspaceIdentityClearKeepsReferenceAndChangesInstance
    serve_workspace_identity_test.go:26: cleared workspace ID = "034jpqFloI06ZizMU4xR3Y", want retained "034jpqFa1C6WySMJnOEbcZ"
```

The fresh-root test first failed when its default assignment was temporarily removed:

```text
--- FAIL: TestWorkspaceIdentityNewRootUsesSessionID
    session_workspace_identity_test.go:16: WorkspaceID() = "", want new root ID "034jpvdlnjrCCkrL3WgUL9"
```

The final required commands passed and selected the named tests:

```text
(cd agent && go test ./schema ./ -run 'TestSessionMetaPersistsWorkspaceID|TestWorkspaceIdentity|TestForkSession' -count=1)
ok primeradiant.com/evener/agent/schema 0.087s
ok primeradiant.com/evener/agent 1.507s

go test ./cmd/evener -run 'TestWorkspaceIdentity|TestServeFreshIdleSessionOwnsResumeTarget|TestServeResume' -count=1
ok primeradiant.com/evener/cmd/evener 0.979s

git diff --check
# exit 0, no output
```

The tests exercise real metadata save/load; a legacy metadata fixture without `workspace_id`; unchanged transcript bytes; fallback persistence observed before serve's app-identity preparation; preservation of a nonempty identity; fork ID separation; and Clear plus restart preserving the public `EvenerThread.Ref` while the runtime ID changes. The tests also assert the identity is not present in the inherited config snapshot.

## Review and remaining checks

`git diff --cached --check` and the post-commit `git diff HEAD^ --check` exited 0. Normal commit hooks completed, and `git status --short --branch` showed only `## manager-session-tools` after commit. No race test was added: the identity is assigned during construction and read from immutable session config, so there is no shared mutable state in this accessor/lifecycle path.

I did not run `go test ./...`, race tests, or broader repository gates. Manager preference storage, RPC, transport, and UI are outside this Task 1a scope and remain for later seats.
