## Step 3 — Active guidance and final verification (M4)

Files: `internal/appwiredoc/protocol.md.tmpl`, generated `docs/appwire-protocol.md`, comments in `cmd/evener-hub/app_rpc.go` and `cmd/evener-hub/web_api_tree.go`, this plan.

1. Establish stale guidance with exact text search; replace removed external source/managed launcher/bridged-row claims with generic AppWire descriptions.
2. Run canonical `make generate`; verify protocol output is reproducible, inspect staged diff, and commit only allowed paths. Run `make lint-generated` after commit.
3. Run `gofmt -w` on touched Go files, confirm `gofmt -d` empty; run both full subsystem packages, hub package, and focused race gates. Check `git diff --check` from base, scope of changed paths, and final empty porcelain.
4. Write scratch report with exact RED/GREEN commands, failure evidence, exit statuses, commit hashes, architecture, self-review and limitations. Return integration/re-review packet to controller; controller owns rerunning original adversarial overlays and sending the same reviewer the packet after integration.

4. **F4/F5:** change only the misleading usage example in `cmd/evener/serve.go`. Report raw grep rows 188,193,194,3065,3068 as W (generic `CodexErrorInfo` wire clone/server code/tests), not C; leave all code and controller-owned ignored classification artifacts intact.
