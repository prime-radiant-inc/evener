# Hub Endpoint Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give listener binding, client connection and published origins distinct validated contracts, then use authoritative endpoint discovery across local and remote operations.

**Architecture:** Share endpoint syntax and normalization in a small Go package and a TypeScript package subpath, with one literal conformance corpus. The hub owns config resolution and publishes its actual bound endpoint; TUI and SSH consumers query that owner instead of guessing ports or deriving bind settings from URLs. Preserve caller-specific credential and invitation policies above the shared parser.

**Tech Stack:** Go (`net`, `net/url`, `net/http`, existing TOML/config and atomic-file helpers), TypeScript (`URL`, existing package tooling), Bash operations script, existing real-stack and SSH container test harnesses.

**Spec:** `docs/superpowers/specs/2026-10-15-hub-endpoint-cleanup-design.md`

## Approved scope amendment: TUI retirement

Jesse approved subagent-driven execution, then on 2026-10-16 chose to drop remaining TUI-specific work in favor of the separate TUI removal. Do not add TUI implementation, tests or documentation. Task 8 proves startup, endpoint discovery, authenticated AppWire, restart and preserved user data through surviving non-TUI clients. Keep shared endpoint and credential contracts intact. Reconcile already committed TUI changes with the removal branch during integration; this task does not implement a replacement terminal interface or duplicate the deletion. This amendment controls over the original task text below.

## Global Constraints

- Jesse chose to retain server `--addr`/`-addr` and TOML `addr`, cleanly rename client/public settings, require origin-only URLs, and allow explicit-local-URL autostart only when unchanged local configuration matches. The approved TUI-retirement amendment limits remaining client work to surviving consumers.
- Listener precedence: supplied CLI flag > present environment variable > present TOML key > default.
- Default listener: `127.0.0.1:9180`. Numeric listener ports: `0..65535`. Client URL ports: `1..65535`.
- Retained server names: `--addr`/`-addr`, TOML `addr`. New names: `EVENER_HUB_LISTEN_ADDR`, `--hub-url`, `EVENER_HUB_URL`, `public_url`.
- Chosen clean cutover for renamed settings: no compatibility aliases; no automatic edits to installed configs or services.
- Chosen URL contract: HTTP(S) origins only; credentials remain separate.
- An explicit local HTTP URL permits autostart only when unchanged local configuration matches. Remote, HTTPS, mismatched-port and unresolved ephemeral-port URLs never trigger startup. Local startup passes configuration selection, never a derived listener override.
- Preserve SSH literal-loopback/no-proxy policy, config/host-record contents, saved profiles, tokens and sessions.
- No test asserts human-readable phrasing. Assert typed errors, data fields, sockets, requests, files and process effects.
- No production restart, deployment, live credentials or provider requests. All disposable runs use isolated HOME, config, state and rendezvous roots and explicitly scrub inherited Evener overrides.
- Use the repository's pinned tools and existing dependencies. Add no runtime dependency unless an existing dependency cannot satisfy the contract and Jesse approves the change.
- Targeted tests run locally; CI is the full-suite authority. Heavy local gates run serially. Never `npm ci` through a symlinked `node_modules`.
- Jesse chose credential-bearing login links only during interactive startup and explicit retrieval. Ordinary redirected/background startup logs remain token-free; `evener hub login-url [--config PATH]` deliberately writes the link to stdout.
- Preserve ownership-verified recovery of an unresponsive endpoint-capable service and pending restarts. A live authenticated endpoint is not the authority for supervisor recovery.

## Review Focus

Each risk has a named test in its owning task; these are requirements, not reviewer suggestions.

1. An explicitly empty flag/env must fail instead of falling back to localhost (Tasks 1, 2, 4).
2. A stale record or reused port must not receive the local token or authorize a restart (Tasks 3, 4, 7).
3. Browser URL normalization must not erase a forbidden prefix, control character or ambiguous numeric host (Tasks 1, 5).
4. An ephemeral/wildcard listener must survive local autostart and remote restart with a newly resolved client URL (Tasks 3, 4, 7, 8).
5. An older remote hub lacking endpoint discovery must not cause a guessed-port probe, deployment cycle or unrelated-process termination (Task 7).

## Execution and delivery map

Tasks 1 → 2 → 3 → 4 establish the smallest complete local journey. Task 5 can follow Task 1 independently. Task 6 consumes Tasks 2 and 3. Task 7 consumes Tasks 1–4, including the shared authenticated transport. Task 8 closes cross-component evidence and delivery. Keep one writer per checkout; use isolated delegate lanes only for independent tasks. Do not merge lanes while another delegate reads an absolute path inside the integration checkout.

Each task follows red → minimal implementation → green → focused regression → commit. The listed tests are new tests unless marked existing. Before changing a test file, read `docs/developing-evener/testing.md`; before changing frontend production code, read `docs/developing-evener/design-system.md`. These changes need no widgets or UI redesign.

## File ownership and interfaces

| Unit | Owner paths | Responsibility |
| --- | --- | --- |
| Go endpoint values | `internal/hubendpoint/` | Listener and origin grammar, normalization, URL construction, typed validation errors |
| Shared fixtures | `testdata/hubendpoint/origins.json` | Literal input, acceptance and canonical-origin expectations consumed independently by both languages |
| Server config and discovery | `cmd/evener-hub/config.go`, new `cmd/evener-hub/endpoint.go`, new root `internal/hubendpoint/{report,runtime,verify}.go` | Effective settings, shared report, bound-instance record and read-only JSON command |
| TUI startup | `cmd/evener-tui/internal/hubstart/`, `cmd/evener-tui/main.go` | Explicit destination versus local discovery, subprocess ownership and credential binding |
| Authenticated transport | `internal/hubendpoint/http.go` | Origin-bound bearer injection, redirect rejection and same-connection verification shared by TUI and SSH attachment |
| TypeScript origin helper | `appwire-client/typescript/hubOrigin.ts` | Shared HTTP origin mechanics, separate from the low-level WebSocket transport |
| Published origins | `cmd/evener-hub/mobile_origin.go`, `app_mobile.go`, `device_pairing.go`, `internal/hubcore/` | Public URL defaults and existing invitation reachability/wire policy |
| Remote operations | `cmd/evener-hub/attach.go`, `internal/sshconn/`, `scripts/ops/deploy-hub.sh` | One remote endpoint result for connect, probe and restart |
| Evergreen guidance | `docs/evener-hub-endpoints.md` plus existing guides | One owning contract with links and updated examples |

The root `internal/hubendpoint` package owns endpoint values, report types and bounded record/connection verification. It never loads TOML, starts processes or imports the SSH manager. Server configuration ownership stays in `cmd/evener-hub`; TUI consumes the shared report type through the CLI.

---

### Task 1: Define endpoint value types and a literal conformance corpus

**Files**
- Create: `internal/hubendpoint/listen.go`, `origin.go`, `errors.go`, `listen_test.go`, `origin_test.go`.
- Create: `testdata/hubendpoint/origins.json`.
- Modify/test: `hubapi/client.go`, `hubapi/client_test.go`.

**Interfaces**
- `ParseListenAddr(raw string) (ListenAddr, error)`; `ListenAddr.String() string`; listener port zero is valid.
- `ParseOrigin(raw string) (Origin, error)`; `Origin.String() string`; `Origin.RPCURL() string`; `Origin.Hostname() string`; `Origin.Port() uint16`; `Origin.Scheme() string`.
- `LocalOrigin(boundAddr string) (Origin, error)` maps an actual bound wildcard to numeric same-family loopback and retains specific numeric interfaces.
- `ValidationError` has machine fields `Code` and `Field`; codes include `empty`, `scheme`, `host`, `port`, `userinfo`, `path`, `query`, `fragment`, `control`, `wildcard`, `scoped_host`. It never stores raw secret-bearing input.
- Preserve `hubapi.NewClient(base string, httpClient *http.Client) (*Client, error)`; validate `base` using `ParseOrigin` rather than adding `http://`.

- [ ] **Write red tests:** `TestParseListenAddr`, `TestOriginCorpus`, `TestLocalOrigin`, `TestClientRequiresOrigin`. Table assertions include:
  ```go
  // Exact accepted listener values include wildcard and port zero.
  got, err := ParseListenAddr("0.0.0.0:0")
  if err != nil || got.String() != "0.0.0.0:0" { t.Fatalf("listener: %v, %v", got, err) }
  // Corpus rows independently pin canonical outputs, never computed from the parser.
  // https://EXAMPLE.test:443/ => https://example.test
  // http://[::1]:9180/ => http://[::1]:9180
  // http://0.0.0.0:9180 => wildcard error
  // http://example.test/prefix => path error
  // http://example.test/a/.., http://127.1, trailing '?' and '#' => rejection
  ```
  Include empty strings, omitted scheme, HTTPS defaults, Unicode/IDNA, IPv6, credentials, encoded paths, backslashes, whitespace/control characters, ports -1/0/65536 and nonnumeric ports. Binding tests also cover `:9180`, `[::]:0`, a DNS name and scoped IPv6. `LocalOrigin` rejects unresolved port zero and scoped-only URL derivation.
- [ ] **Run red:** `go test ./internal/hubendpoint ./hubapi -run 'Test(ParseListenAddr|OriginCorpus|LocalOrigin|ClientRequiresOrigin)' -count=1`. Observe the missing contract or changed-behavior failure.
- [ ] **Implement the interfaces:** use `net.SplitHostPort`, explicit numeric-port validation, `net/netip`, `net/url`, and existing IDNA support. Check the raw origin form before a permissive parser can normalize forbidden input away. Keep URL path construction in `hubapi.Client.URL` for API paths and queries, without retaining a base prefix.
- [ ] **Run green and regression:** `go test ./internal/hubendpoint ./hubapi -count=1`; add fuzz seeds from every rejected corpus category using repository fuzz conventions. Update existing hubapi caller fixtures to explicit origins where needed, without weakening request assertions.
- [ ] **Commit:** stage only the listed package files, shared corpus and changed callers identified by the compile checks. Commit message: `refactor(hub): define listener and origin contracts`.

### Task 2: Resolve server settings once and introduce the naming cutover

**Files**
- Modify: `cmd/evener-hub/config.go`, `main.go`, `envvars/envvars.go`.
- Create: `cmd/evener-hub/endpoint_config.go`, `endpoint_config_test.go`.
- Modify/test: `cmd/evener-hub/config_test.go`, `main_test.go`, `app_host_toml_store_test.go`, relevant existing config fixtures.
- Create: `docs/evener-hub-endpoints.md`; update server examples in `docs/evener-hub.md` and the environment registry documentation.

**Interfaces**
- Retain server `Config.Addr` (`toml:"addr"`) as configured listener input; use the typed `EndpointConfig.ListenAddr` after resolution. Replace `Config.MobileBaseURL` with `Config.PublicURL` (`toml:"public_url"`). Keep running listener state separate.
- `EndpointInput { Value string; Present bool }` and `EndpointConfig { ListenAddr hubendpoint.ListenAddr; ListenSource string; PublicURL *hubendpoint.Origin; ConfigPath string; ConfigExists bool }` in the hub package.
- `resolveEndpointConfig(flagValue, envValue, configValue EndpointInput, publicURL string) (EndpointConfig, error)` selects the winning listener, then validates it. Config path/existence metadata is filled by the common loader.
- `envvars.EVENERHubListenAddr` and `envvars.EVENERHubURL`; retire the ambiguous registry entry from supported settings while keeping detection for its migration error.
- A typed migration error identifies the retired setting and replacement; it contains no raw value.

- [ ] **Write red tests:** `TestEndpointConfigPrecedence`, `TestEndpointConfigExplicitEmpty`, `TestEndpointConfigRetiredSettings`, `TestEndpointConfigValidationBeforeEffects`, `TestEndpointConfigPreservesOperatorKeys`. Assert flag/env/config/default selection and source fields, `errors.As` fields, zero lock/token/bind side effects for invalid input, and unchanged unrelated TOML/host-record values after a host rewrite. Test `--addr=value`, `-addr=value` and separated arguments; explicitly preserve server flag/TOML support.
- [ ] **Run red:** `go test ./cmd/evener-hub -run '^TestEndpointConfig' -count=1`.
- [ ] **Implement common resolution:** mechanically update all `MobileBaseURL` field references to `PublicURL` in this task so its commit compiles; Task 6 changes the publication behavior. Retain TOML key presence via decode metadata and CLI presence via `FlagSet.Visit`; use `LookupEnv`, not `Getenv`, for endpoint variables. Validate after choosing the winning listener. Keep config parse/type failures and recognized retired-name errors visible. Do not make all unknown top-level TOML keys errors. Keep default loading reusable by the endpoint command.
- [ ] **Run green and regressions:** `go test ./cmd/evener-hub -run 'Test(EndpointConfig|LoadConfig|RunMainHelp|Host.*TOML)' -count=1` and `go test ./envvars -count=1`. Read test names first and verify every selected family actually ran with `-v` if uncertain. Document the selected precedence and migration examples.
- [ ] **Commit:** explicitly stage the touched server/config/envvars/doc files and fixture edits. Commit message: `feat(hub): separate listener configuration from client URLs`.

### Task 3: Publish the actual running endpoint and expose read-only discovery

**Files**
- Create: `cmd/evener-hub/endpoint.go`, `cmd/evener-hub/endpoint_test.go`, root `internal/hubendpoint/{report,runtime,verify}.go` and their `_test.go` files.
- Modify: `cmd/evener-hub/main.go`, `cmd/evener-hub/internal/hostlock/hostlock.go` and platform lock files, `cmd/evener-hub/web_api.go`, `cmd/evener-hub/internal/hubcore/` runtime configuration.
- Modify/test: `hubapi/types.go`, `cmd/evener-hub/main_ephemeral_port_test.go`, `main_hublock_test.go`.
- Update: `docs/evener-hub-endpoints.md`.

**Interfaces**
- New command: `evener hub endpoint --json [--config PATH] [--addr HOST:PORT]`.
- `hubendpoint.Report` carries JSON version `1`: `schema_version`, `state`, `config_path`, `config_exists`, `hub_state_root`, `configured_listen_addr`, `listen_source`, optional `public_url`, optional `running`, optional `reason`. States: `stopped`, `starting`, `running`, `unavailable`. `Running *hubendpoint.Record` is present only after verification. Go names use exported CamelCase (`SchemaVersion`, `HubStateRoot`, `ConfiguredListenAddr`, and so on). No token values.
- A running endpoint comes from the private record and validated serving instance, not the effective config. Non-running output never invents an actual port for `:0`.
- `hubendpoint.Record` contains `SchemaVersion`, `BootID`, `PID`, `StartedAt`, `ConfigPath`, `HubStateRoot`, `RequestedListenAddr`, `ListenSource`, `BoundAddr`, `LocalURL`, `LoopbackURL`, `PublicURL`, `Version`, `BackendGitSHA`; JSON uses the spec's snake_case spellings. Reuse existing `HubBootID`; extend `hubapi.HealthResponse` with `BootID string` tagged `boot_id`.
- `Publish(stateRoot string, record Record) (remove func() error, err error)`, `Read(stateRoot string) (Record, error)`, `DecodeReport(r io.Reader) (Report, error)` in root `internal/hubendpoint`; file/report/health reads are capped at 64 KiB. Record mode is `0600`; reject symlinks, nonregular files and different-owner or group/world-writable records. Cleanup compares boot identity before removal and runs only while the caller holds the hub lock.
- `hostlock.IsHeld(path string) (bool, error)` inspects an existing lock without creating or replacing it. Existing `AcquireLock` remains the startup authority.
- `runEndpoint(args []string, stdout, stderr io.Writer, deps mainDeps) error` and `resolveHubEndpoint(ctx context.Context, opts endpointOptions, deps mainDeps) (hubendpoint.Report, error)` in the hub package compose config, lock, record and token-free health verification.
- `VerifyLocalConn(ctx context.Context, conn net.Conn, origin Origin, expected Record) error` verifies token-free HTTP health on that same open connection, drains its bounded response and leaves the connection usable only if boot ID and actual listener match. It never reads tokens or opens another connection. A failed verification closes the connection at the caller.

- [ ] **Write red tests:** `TestEndpointReadOnly`, `TestEndpointBoundPortZero`, `TestEndpointLifecycle`, `TestEndpointRejectsReusedPort`, `TestEndpointConfigChangedWhileRunning`. Assert private record permissions, no token/config mutation by the command, actual socket port > 0 with configured port still 0, matching live identity, and generation-conditional retirement. A different server on the old port must receive no Authorization header and must never be reported as this hub.
- [ ] **Run red:** `go test ./cmd/evener-hub ./cmd/evener-hub/internal/hostlock ./internal/hubendpoint -run 'Test(Endpoint|RunMainAddrZero|.*HubLock)' -count=1`.
- [ ] **Implement record and command:** publish after bind and handler construction, immediately before serving; readiness requires live verification; use the selected state root and existing lock owner. Bound file reads and decode validation; publish atomically with private mode. Verify discovery without credentials/proxy/redirects. Preserve config and runtime values separately. Add instance identity to health without changing existing health field meanings.
- [ ] **Run green and lifecycle regression:** same targeted command, then package tests for both internal packages. Exercise real listeners/processes for cold start, bind failure, concurrent starters, crash leftovers, corrupt records and ownership races; use completion channels/process exit rather than sleeps. A held lock with bad metadata is unready/unavailable, not stopped.
- [ ] **Commit:** explicitly stage discovery, health, lock, tests and endpoint guide. Commit message: `feat(hub): report the live bound endpoint`.

### Task 4: Make TUI startup and credential selection destination-aware

**Files**
- Modify: `cmd/evener-tui/internal/hubstart/hub_start.go`, `cmd/evener-tui/main.go` and direct helper callers.
- Create: `cmd/evener-tui/internal/hubstart/local_endpoint.go`, `endpoint_startup_test.go`, `credential_transport_test.go`; `internal/hubendpoint/http.go`, `http_test.go`.
- Modify/test: existing `hub_start_test.go`, `hub_start_client_close_test.go`, `cmd/evener-tui/main_startup_test.go`, reconnect tests affected by runtime address shape.
- Update: `docs/evener-hub-endpoints.md`, `docs/getting-started.md`.

**Interfaces**
- Replace `RawAddr`/`HubAddr` destination inputs with `HubURL` plus `HubURLPresent`. Parse environment through a lookup function returning `(string, bool)` so empty is distinguishable.
- Add `--hub-config PATH` for automatic local discovery/startup; it selects server config and does not reinterpret `--state-dir`, which remains project/session state.
- Replace URL-derived `BindAddr`/`IsLocal` launch decisions with explicit mode: configured destination or automatic local discovery.
- `HubCommandContext { Binary string; ConfigPath string; Env []string }` in `local_endpoint.go` captures one child execution environment. Preserve the existing launcher's `EVENER_STATE_DIR` and derived `XDG_STATE_HOME` assignments when `StateDir` is supplied, without mutating the parent environment. Endpoint inspection and startup both use this exact context; it never enters diagnostics.
- `readLocalEndpoint(ctx context.Context, command HubCommandContext) (hubendpoint.Report, error)` runs that binary's `hub endpoint --json` with the captured environment and calls Task 3's `DecodeReport`. `HubStartRequest` consumes the same `Command HubCommandContext`, plus its existing log/operational settings.
- `matchesConfiguredLocalOrigin(ctx context.Context, origin hubendpoint.Origin, listen hubendpoint.ListenAddr) (bool, error)` requires HTTP, matching nonzero port, and a literal local/localhost target accepted by the configured bind in that IP family. Resolve configured DNS bind hosts and localhost through the OS; arbitrary destination DNS names do not authorize autostart. Test loopback aliases, specific-interface binds and dual-family localhost explicitly.
- `hubendpoint.HTTPClientWithBearer(base *http.Client, token string, origin Origin, local *Record) (*http.Client, error)` in root `internal/hubendpoint/http.go` scopes header injection and blocks redirects. Non-nil `local` uses a private direct transport whose dialer pins the verified numeric address and calls `VerifyLocalConn` before giving each connection to HTTP/WebSocket code. Require this verified transport for any file-derived token, including a matching explicit URL; never mutate a shared default transport. Task 7 imports this shared transport rather than the TUI's internal package.
- `StartLocalHub` returns owned child completion/cancellation state until readiness, rather than discarding process identity before endpoint publication. It passes only selected config and existing operational flags, never a URL-derived bind.

- [ ] **Write red tests:** `TestTUIEndpointPrecedence`, `TestExplicitHubURLMatchesConfiguration`, `TestExplicitHubURLMatchesRunningInstance`, `TestAutomaticHubPreservesListener`, `TestAutomaticHubUsesResolvedTokenRoot`, `TestAutomaticHubStateDirEnvironment`, `TestHubBearerOriginIsolation`, `TestAutomaticHubConcurrentStart`, `TestHubStartupCancellation`. A matching explicit local HTTP URL starts once without an `--addr` override and authenticates to the verified instance. An explicit URL for a verified existing ephemeral listener, server flag override or edited-but-not-restarted TOML authenticates and launches no child. When no matching verified instance exists, remote/HTTPS/mismatched/unresolved-ephemeral destinations launch zero children and read zero local-token files. A local URL matching config but served by an unrelated process gets no file-derived token. A forced connection replacement between discovery and authenticated dial proves the per-connection verifier rejects the new port owner. Test explicit-empty flag/env, token flag/env precedence and no-token behavior. Two real HTTP servers plus TLS assert same-origin authentication, no credential on redirects, same-host different-port isolation and no downgrade disclosure. A real subprocess using a CLI-only `--state-dir` override proves discovery, indexed project state and token-root agreement, both without and with explicit TOML `hub_state_root`.
- [ ] **Run red:** `go test ./cmd/evener-tui/internal/hubstart -run 'Test(TUIEndpoint|ExplicitHubURL|AutomaticHub|HubBearer|HubStartupCancellation)' -count=1`.
- [ ] **Implement startup modes and shared transport:** inspect a potentially local explicit HTTP destination against the selected running instance before applying the config-match predicate. Only when the selected hub is stopped may automatic mode, or an explicit configuration match, start it with autostart enabled. Use the captured child environment for every inspection/start. Wait for publication or child exit, then load the matching state-root token. Preserve the requested origin and refuse a different running instance. Validate each fresh TCP connection before file-derived token use, including transport reconnects. Preserve reconnect/client-close behavior and `--no-auto-start-hub`.
- [ ] **Run green and regression:** `go test ./cmd/evener-tui/internal/hubstart ./internal/hubendpoint -count=1`; `go test ./cmd/evener-tui -run 'Test.*(Startup|Reconnect|ClientClose|HubUpdateConfig)' -count=1`. Run a real subprocess fixture against `0.0.0.0:0`, then reconnect and assert the hub still serves the same configured binding and assigned port.
- [ ] **Commit:** stage only changed TUI helpers/callers/tests, shared transport and docs. Commit message: `fix(tui): discover local hubs without changing their listener`.

### Task 5: Share TypeScript origin parsing and update development clients

**Files**
- Create: `appwire-client/typescript/hubOrigin.ts`, `hubOrigin.test.ts`.
- Modify: `appwire-client/typescript/package.json`, package README, `pairing.ts` and relevant package qualification expectations.
- Modify: `mobile-native/src/connection.ts`, `mobile-native/src/connection.test.ts`; `cmd/evener-hub/frontend/vite.config.ts`, `cmd/evener-hub/frontend/tsconfig.json`, `mobile-native/tsconfig.json`, `mobile-native/tsconfig.check.json`, `mobile-native/vitest.config.mts`, `mobile-native/metro.config.js` and resolver tests where their explicit subpath maps require the new export.
- Create: `cmd/evener-hub/frontend/src/dev/hubEndpointConfig.test.ts`.
- Modify: active frontend/browser test harnesses currently setting `EVENER_HUB_ADDR`, and shared pairing tests.

**Interfaces**
- Published subpath: `@evener/appwire-client/hubOrigin`.
- `parseHubOrigin(raw: string): string` returns the normalized HTTP(S) origin or throws `HubOriginError` with a `code` from the shared validation categories.
- `hubRPCURL(origin: string): string` validates and produces `ws[s]://.../rpc`.
- Vite resolves `EVENER_HUB_URL` once: absent → documented development default, present empty/invalid → startup error. No local autostart.

- [ ] **Write red tests:** `hubOrigin.test.ts` reads `testdata/hubendpoint/origins.json` as literal expectations. `hubEndpointConfig.test.ts` asserts Vite HTTP and WebSocket proxy target and Origin settings from a valid URL, and rejected empty/legacy/invalid settings. Native tests assert connection targets and that invalid input does not save a profile. Existing pairing round-trip tests keep exact wire destinations and restrictions.
- [ ] **Run red:** after safe preflight, run `(cd cmd/evener-hub/frontend && npm test -- ../../../appwire-client/typescript/hubOrigin.test.ts ../../../appwire-client/typescript/pairing.test.ts src/dev/hubEndpointConfig.test.ts)` and `(cd mobile-native && npm test -- src/connection.test.ts)`. The frontend script also runs its Node script tests after Vitest. Confirm each intended test file appears in the runner output; native tests use their own runner.
- [ ] **Implement shared mechanics:** use raw-form validation before `URL` normalization. Native and pairing call the shared helper, then retain their own policy checks. Vite imports by package name using its actual config-loader resolution; prove both config load and dev proxy routing, not only a helper test. Leave low-level WebSocket transport behavior unchanged.
- [ ] **Run green and regression:** shared corpus, native connection/pairing tests, real Vite proxy HTTP/WS smoke, package export qualification. Format touched frontend/shared-package paths with the frontend's pinned Biome, then run `make test-web`, `make test-api-package`, `make test-native` serially. Read each target/script before running and report platform capability skips explicitly.
- [ ] **Commit:** stage exact shared/native/Vite/harness files. Commit message: `refactor(clients): share hub origin validation`.

### Task 6: Use one advertised origin and truthful diagnostics

**Files**
- Modify: `cmd/evener-hub/mobile_origin.go`, `app_mobile.go`, `device_pairing.go`, `main.go`, `internal/hubcore/` public-origin configuration.
- Modify/test: `app_mobile_test.go`, pairing/origin tests, startup tests; add `endpoint_public_url_test.go`.
- Create: `cmd/evener-hub/login_url.go`, `login_url_test.go`.
- Update: `docs/evener-hub-endpoints.md`, `docs/device-pairing.md`, `docs/product/subsystems.md` (S04/S06 and affected shared-client ownership).

**Interfaces**
- `PublicURL` is a validated optional origin; use Task 1's parser below the existing invitation reachability/wire checks.
- Startup link choice and default pairing use the same configured public origin. Explicit invitation override remains higher precedence.
- Human startup output distinguishes configured listener/source, actual listener and connection/public origin; Task 3 JSON carries their structured counterparts.
- `runLoginURL(args []string, stdout, stderr io.Writer, deps mainDeps) error` implements `evener hub login-url [--config PATH]`. Consume Task 3's verified running record, then its state-root token; emit one URL plus newline to stdout. No autostart, token creation/rotation, browser opening or config writes. A non-running report or unreadable token returns a typed error with empty stdout and token-free stderr.
- `browserLoginURL(record hubendpoint.Record, hostname, token string) (string, error)` in `login_url.go` shares URL construction between startup and retrieval: the record's `PublicURL` wins, otherwise use the existing advertised-host fallback on `BoundAddr`, then `hubedge.AuthURLFor`. Never substitute currently edited TOML for the running record's publication settings.
- `isTerminalWriter(w io.Writer) bool` uses the actual `*os.File` descriptor and the existing `golang.org/x/term` dependency. Unknown writers, pipes and files are noninteractive. Only terminal startup gets the login link; never open `/dev/tty` or infer interactivity from a different descriptor. Explicit `login-url` stdout remains intentional secret output even when redirected.

- [ ] **Write red tests:** `TestPublicURLDoesNotChangeListener`, `TestPublicURLSharedByStartupAndPairing`, `TestInvitationOverrideWinsPublicURL`, `TestStartupLoginOutput`, `TestLoginURLCommand`. Bind a real local HTTP socket and configure `https://hub.example.test:9443`; assert actual serving HTTP and advertised HTTPS independently. Cover IDNA, IPv6, default ports and current unsafe-origin refusals. Capture startup output through pipes/files and the TUI child path; assert no sentinel token. Check terminal classification and intentional terminal output separately. The command returns the selected running instance's link, including custom root and actual ephemeral port, leaves config/token files unchanged, and emits no URL for stopped/unavailable/error states. Follow the generated link against a real disposable hub and prove cookie-authenticated API access. Assert structured values and secret absence, not diagnostic phrasing.
- [ ] **Run red:** `go test ./cmd/evener-hub -run 'Test(PublicURL|InvitationOverride|HubMobilePairing|StartupLoginOutput|LoginURLCommand)' -count=1`.
- [ ] **Implement publication and diagnostics:** remove the independent startup-host guess when public URL is set; retain useful fallback discovery when unset. Share login-link construction and gate startup disclosure on its actual stderr terminal. Add the explicit retrieval command, CLI help and password-like handling guidance. Keep unsafe invitation outcomes local to invitation generation, without disabling healthy direct clients or changing the listener.
- [ ] **Run green and regression:** the same command plus the existing device-pairing codec and origin-selection tests identified by `go test -list`. Review generated/public links without printing real token-bearing URLs.
- [ ] **Commit:** stage publication/config/test/docs paths. Commit message: `fix(hub): use the public origin consistently`.

### Task 7: Resolve SSH and deployment endpoints on their owning host

**Files**
- Modify: `cmd/evener-hub/attach.go`, `cmd/evener-hub/attach_bridge_test.go`, `cmd/evener-hub/config.go`, `cmd/evener-hub/internal/hostreg/hostreg.go` comments/validation, and direct host configuration adapters. Preserve durable host field names and receipt fingerprints.
- Modify: `cmd/evener/internal/launchcheck/` JSON capability report and its tests; adjust generated docs only if their source changes.
- Modify: `cmd/evener-hub/internal/sshconn/{preflight,runner,manager,version,deploy}.go` and their focused tests.
- Create: `cmd/evener-hub/internal/sshconn/endpoint.go`, `endpoint_test.go`.
- Modify: `scripts/ops/deploy-hub.sh`; create `scripts/ops/deploy-hub-endpoint-test.sh`.
- Update: `docs/evener-hub-remote-operations.md`, `docs/evener-hub-endpoints.md`.

**Interfaces**
- `hub attach --hub-url ORIGIN` is a connection override and still enforces literal-loopback/no-proxy policy. No override consumes Task 3 discovery.
- `func (m *Manager) resolveHostEndpoint(ctx context.Context, host hostreg.Host, facts Preflight) (hubendpoint.Report, error)` invokes remote endpoint inspection with the remote config selector. It cannot call Ensure, attach, deploy or bootstrap.
- The verified `Report.Running` is the resolved input for authenticated attach and readiness checks. SSH requires `LoopbackURL`; a specific non-loopback-only bind does not invent one. Re-resolve after each generation change; never persist actual ephemeral ports as future configuration.
- Attachment consumes Task 4's `hubendpoint.HTTPClientWithBearer` with the selected record. Its actual WebSocket connection runs `VerifyLocalConn` before any bearer is sent, rejects redirects and bypasses proxies. Preserve the bridge-origin header.
- Preserve `restartHub`, `restartSupervised`, `recoverRestart` and their durable pending-restart ownership. For an endpoint-capable installation, supervisor/executable/user/config/state-root/process checks can authorize the existing recovery operation independently of HTTP responsiveness. A verified `Report.Running` is not required to recover an unresponsive selected service or complete a recorded restart that left it stopped. Never use an unverified record as kill authority. Verify the replacement's endpoint/build/boot before declaring recovery complete.
- Retain `HostConfig.Addr`, `hostreg.Host.Addr`, TOML `[[hosts]].addr` and the AppWire `addr` field as fresh-bootstrap listener overrides. Rename only the internal manager option `HubAddr` to `HubListenAddr`. Remove config-path/address pairing requirements. No new host field, wire compatibility shim or mutation-fingerprint migration is needed.
- Expose `hub_endpoint_schema: 1` from `launch-check --json`. `probeLaunchCheck` omits `--protocol` so the remote reports capability and its own protocol; compare protocol separately in the existing upgrade decision path. Missing capability or an old running process without a verified record yields a typed manual-migration outcome before install/restart/kill. Record damage alone does not turn a known endpoint-capable generation into a legacy one. A fresh host still uses the existing authorized install/bootstrap path; test missing binary separately from unsupported old binary. No staged legacy inspector, default-port guess or automatic config rewrite.
- Deployment script interface: `--label LABEL`, new `--config PATH`, existing `--timeout N`; reject former health-destination `--addr`. Resolve the selected service definition's config and environment without guessing a health URL. A supplied config selector must agree. Verify report PID/boot against the selected service before and after replacement; a shared executable path alone is insufficient. Ambiguous service selection fails before mutation.

- [ ] **Write red tests:** `TestRemoteEndpointDefaultConfigCustomPort`, `TestRemoteEndpointExplicitConfig`, `TestRemoteRestartPreservesBinding`, `TestRemoteEndpointPortZeroRefresh`, `TestRemoteEndpointWrongProcess`, `TestRemoteEndpointLegacyCapability`, `TestRemoteEndpointProtocolMismatch`, `TestRemoteRestartUnresponsiveService`, `TestRemoteRestartRecoversStoppedReplacement`, `TestAttachRejectsReusedPort`, `TestAttachRejectsRedirect`. Replace the existing test that deliberately pins attach/probe port divergence with agreement assertions. Assert no restart/kill command for invalid ownership, wrong port owner or absent capability. A health-unavailable selected service remains recoverable when independently proved to be endpoint-capable and owned; a failed first replacement completes through the existing recorder. Protocol mismatch with endpoint capability retains the authorized upgrade path. Record exact process launch arguments; a local URL must not appear as a listener override. Attach-specific socket/redirect tests prove zero bearer disclosure at replacement destinations and successful bridge traffic to a verified hub.
- [ ] **Run red:** `go test ./cmd/evener-hub/internal/sshconn -run '^TestRemoteEndpoint|^TestRemoteRestart' -count=1`; `go test ./cmd/evener-hub -run '^TestAttach' -count=1`. Run the new shell harness against the real script's argument/JSON path using disposable external-command boundaries, not a second implementation of the script.
- [ ] **Implement one resolver:** run it at the remote config owner, thread its result through preflight/attach/readiness checks and delete default-port guesses and duplicated wildcard conversion. Use the shared verified transport for attachment. Keep existing verified process-owner and pending-restart checks separate from readiness, so an unresponsive owned service can recover. Make unsupported old hosts a typed, non-destructive manual-migration outcome with bounded retry behavior. Preserve supervisor launch settings; for ad-hoc restart retain verified argv and only the recorded listener env override, while allowing intentional current TOML edits to take effect.
- [ ] **Update the deployment script:** implement the interface above, query endpoint JSON with the selected service's config/environment, and use the reported local URL for readiness. Restart only the ownership-verified service, then query again and match its new PID/boot/build. An unavailable endpoint blocks authentication, not independently authorized recovery. Read service argv only to identify config/environment, never to guess a health URL; do not interpolate unvalidated text into shell code. The harness covers equals-form flags, custom config/state roots, IPv6, invalid JSON, unresponsive health, stopped replacement recovery and two service labels sharing one executable with different roots. It must prove the unselected service is untouched and cannot satisfy post-restart readiness.
- [ ] **Run green and regression:** targeted tests above, then `go test ./cmd/evener-hub/internal/sshconn -count=1` and `bash scripts/ops/deploy-hub-endpoint-test.sh`. Extend and run `EVENER_SSH_E2E=1 EVENER_SSH_E2E_DEPLOY=1 EVENER_SSH_E2E_CONTAINER=1 go test ./cmd/evener-hub -run '^TestHostDeployPipelineContainerE2E$' -count=1 -v` for default-path custom config, explicit config, wildcard and port-zero restarts. Do not set `EVENER_SSH_E2E_HOST`; this run must use its own disposable container. A Docker-capability skip is missing evidence, not a pass. Confirm an unrelated default-port service survives.
- [ ] **Commit:** stage only named remote/attach/script/test/docs files plus the launch-check capability report and its tests. Commit message: `fix(hub): share remote endpoint resolution across operations`.

### Task 8: Prove the complete journeys and deliver the coordinated cutover

**Files**
- Create: `cmd/evener-hub/endpoint_journey_test.go`.
- Modify: `cmd/evener-hub/app_host_pipeline_container_e2e_test.go` and existing live-stack helpers to exercise the new names and discovery.
- Update: `README.md`, `docs/getting-started.md`, `docs/evener-hub.md`, `docs/developing-evener/environment.md`, existing active examples and generated docs discovered by the source scan.
- Review-only: this plan and its spec; put implementation evidence in the PR report, not evergreen product docs.

**Interfaces**
- No new product interface. This task exercises the interfaces from Tasks 1–7 through real processes, sockets, RPC and configuration files.

- [ ] **Write/extend journey tests:** default-path TOML `addr = "0.0.0.0:0"` → automatic local discovery/start → actual bound port → authenticated AppWire connection → restart → rediscovery → successful read. Preserve a seeded session/config marker across restart. Explicit HTTPS URL failure must leave child process count and local token reads unchanged. Use the real Vite proxy and native/shared codec checks from Task 5 as separate client sentinels.
- [ ] **Run the new tests before any test-only harness fix:** demonstrate their behavioral sensitivity. Temporarily remove endpoint publication, token origin scoping or remote re-resolution one at a time, run its decisive test, and restore only that owned file before the next mutation. The final tests must fail for a real regression, not just a missing symbol or copied expected string.
- [ ] **Run complete focused verification:** inspect the Makefile, run `make help`, and read scripts before executing them. Run changed Go package tests and targeted race checks, changed-package lint, then frontend/native/package gates serially. Use the actual remote harness opt-in and capabilities from source; never label a skip as a pass. Run `make test-web-browser` on a Chrome-capable host. Do not run multiple heavy gates at once or block pushing on an unnecessary full local suite.
- [ ] **Audit migration coverage:** search tracked active source/scripts/docs for `EVENER_HUB_ADDR`, `--hub-addr`, `mobile_base_url`, and relevant `--addr`/TOML `addr` uses. Retain normal server `--addr` and TOML `addr` use, including `[[hosts]].addr` as a bootstrap listener override. Every remaining active use of a retired client/public name must be a rejection test or migration diagnostic/example. Keep archived plans unchanged. Verify CLI help, config writer and generated outputs agree with the code.
- [ ] **Independent review:** freeze base/head and approved spec. Run the installed PAR contract with two independent competing reviewers, resolving evidenced findings red-first. Then load `simplify-code:simplify-code`, simplify without behavior changes and rerun affected checks. Read and reconcile both reports.
- [ ] **Commit and shepherd:** inspect staged diff, commit all intended changes, load `shepherd-pr:shepherd-pr`, push once per complete review round, read current-head CI and both RoboRev surfaces. Meet repository base policy before final approval. Do not merge with unresolved significant findings or failed checks.
- [ ] **Verify merge and cleanup:** use authorized admin squash merge only when current-head gates permit it. Query merged state and content before retiring task-owned worktrees/branches. Delete owned scratch that the final report does not reference; preserve unrelated changes and designated scratch. Report PR, merge SHA, behavior evidence, untested platforms and that no production hub was restarted.

## Plan acceptance checklist

- [x] Jesse chose clean cutover for client/public settings while retaining server `--addr`/`-addr` and TOML `addr`.
- [x] Jesse chose origin-only client and public URLs, rejecting non-root prefixes.
- [x] Jesse chose explicit-local-URL autostart only when unchanged local configuration matches.
- [x] Jesse chose interactive startup and an explicit command for the browser login link; Task 6 defines the command and token-disclosure tests.
- [ ] Jesse reviews the frozen design and this plan and chooses native or subagent-driven execution.
- [x] Two independent spec reviewers have reviewed the same immutable inputs; legitimate findings are incorporated and affected requirements re-reviewed.
- [x] Runtime handoff and remote-field contracts have exact interfaces; spec review must verify them before implementation.

## Self-review coverage map

| Spec obligation | Owning tasks |
| --- | --- |
| Listener/client/public naming, presence and validation | 1, 2, 4, 5, 6 |
| Actual listener and ephemeral-port handoff | 3, 4, 7, 8 |
| Credential root, origin binding, redirect isolation | 3, 4, 7 |
| Browser login access without routine-log disclosure | 6, 8 |
| Shared origin grammar, native and pairing preservation | 1, 5, 6 |
| SSH attach/probe/restart agreement and ownership safety | 7, 8 |
| Failure recovery, stale/corrupt data and cancellation | 3, 4, 7, 8 |
| Evergreen docs, migrations, review and verified delivery | Every owner task, then 8 |
