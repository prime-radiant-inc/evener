# Web Rewrite Wave 1 — Foundation + Protocol Core (M0+M1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the new TypeScript frontend (toolchain, CI, hub serving behind a flag) and the
complete AppWire protocol core (generated types, client, reducer, stores) proven against a live
hub.

**Architecture:** New app lives in `cmd/serf-hub/frontend/` (Vite 8 + React 19 + TS strict),
embedded into `serf-hub` via `go:embed all:frontend/dist` and served only when `SERF_HUB_WEB=new`;
the legacy UI stays untouched until the M10 deletion wave. TS wire types are generated from the
`appwire` Go catalog with a drift test. One `AppwireClient` per browser window; one pure
`reducer.ts` turns notifications into `ThreadModel`s; zustand stores wire them together.

**Tech Stack:** react, react-dom, dockview+dockview-react (installed now, used in Wave 2),
zustand, @tanstack/react-virtual, react-router, marked, dompurify; dev: vite,
@vitejs/plugin-react, typescript, vitest, @testing-library/react, @testing-library/user-event,
jsdom, eslint, typescript-eslint.

## Global Constraints (from the spec — apply to every task)

- Node ≥ 22; `package-lock.json` committed; no dependencies beyond the list above without a
  written reason in the commit message; no postinstall scripts.
- TS `strict: true`; zero `any` outside `types.gen.ts`'s `unknown` mappings.
- No inline scripts ever (CSP will drop `unsafe-inline`).
- CSS Modules + `src/styles/tokens.css` custom properties only; no CSS-in-JS, no Tailwind.
- The legacy UI must keep working untouched in `SERF_HUB_WEB=legacy` (default) until M10:
  existing `cmd/serf-hub` Go tests keep passing unmodified except where a task says otherwise.
- Snapshot recovery is the truth model: on any doubt, re-`thread/read` and rebuild; never
  patch guessed state.
- Commit after every green test cycle. All commits on branch `worktree-webui-workspace-shell`.
- Run Go commands from the repo root (the worktree root); frontend commands from
  `cmd/serf-hub/frontend/`.

## Locked interfaces (later waves import these; do not rename)

- `frontend/src/protocol/types.gen.ts`: `MethodName`, `NotificationName`, `MethodTypes`
  (`{ [M in MethodName]: { params: …; result: … } }`), `NotificationTypes`
  (`{ [N in NotificationName]: payload }`), plus one interface per catalog type, names equal to
  the Go type names (`ThreadReadParams`, `Thread`, `Turn`, `ThreadItem`, …).
- `frontend/src/protocol/client.ts`: `AppwireClient`, `ConnectionState`, `AnyNotification`
  (Task 4 signatures).
- `frontend/src/protocol/reducer.ts`: `ThreadModel`, `TurnModel`, `ItemModel`,
  `hydrateThread(resp)`, `applyNotification(model, n)`, `prependOlderTurns(model, resp)`
  (Task 6 signatures).
- `frontend/src/stores/connection.ts`: `useConnectionStore` with `{ state, serverInfo, connect() }`.
- `frontend/src/stores/threads.ts`: `useThreadsStore` with
  `{ threads: Map<string, ThreadModel>, ensureThread(ref), releaseThread(ref), send(ref, input),
  interrupt(ref), queue(ref, input), steer(ref, input) }` and `AppwireClientLike` (Task 7).
- Go: `SERF_HUB_WEB` env var (`new` | anything-else=legacy); `/webassets/*` always serves the
  built app's hashed assets; page routes serve the SPA `index.html` only when `new`.

---

### Task 1: Frontend scaffold, Makefile targets, CI job

**Files:**
- Create: `cmd/serf-hub/frontend/package.json`, `vite.config.ts`, `tsconfig.json`,
  `index.html`, `src/main.tsx`, `src/App.tsx`, `src/styles/tokens.css`, `src/styles/global.css`,
  `src/App.test.tsx`, `.eslintrc.cjs` (or flat config), `frontend/.gitignore`,
  `frontend/dist/PLACEHOLDER`
- Modify: `Makefile` (add `build-web`, `test-web`; make `build-hub` depend on `build-web`),
  `.github/workflows/ci.yml` (add Node job; make the Go build job run `make build-web` first)

**Interfaces:**
- Consumes: nothing.
- Produces: `make build-web`, `make test-web`, `npm run dev|build|test|typecheck|lint` inside
  `frontend/`; `frontend/dist/` build output; CI gating.

- [ ] **Step 1: Scaffold the package.** From `cmd/serf-hub/frontend/`:

```bash
npm init -y
npm install react react-dom dockview dockview-react zustand @tanstack/react-virtual react-router marked dompurify
npm install -D vite @vitejs/plugin-react typescript vitest @testing-library/react @testing-library/user-event jsdom eslint typescript-eslint @types/react @types/react-dom @types/dompurify
```

Then edit `package.json`: set `"name": "serf-hub-frontend"`, `"private": true`,
`"type": "module"`, and scripts:

```json
{
  "scripts": {
    "dev": "vite",
    "build": "tsc --noEmit && vite build",
    "typecheck": "tsc --noEmit",
    "test": "vitest run",
    "lint": "eslint src"
  }
}
```

- [ ] **Step 2: Configs.**

`vite.config.ts`:

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server proxies every hub-owned route to a locally running serf-hub.
// Cookies are port-agnostic on localhost, so the /auth?token= capability flow
// works through the proxy unchanged.
const hub = process.env.SERF_HUB_ADDR ?? "http://127.0.0.1:9180";

export default defineConfig({
  plugins: [react()],
  build: { assetsDir: "webassets", outDir: "dist", emptyOutDir: true },
  server: {
    proxy: {
      "/rpc": { target: hub, ws: true },
      "/api": hub,
      "/auth": hub,
      "/doc": hub,
      "/s": { target: hub, bypass: (req) => (req.url?.includes("/images/") ? undefined : req.url) },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: [],
  },
});
```

(The `/s` proxy forwards only `/s/{ref}/images/*` to the hub; other `/s/...` URLs stay in the
SPA for client routing. `bypass` returning the URL serves index.html from Vite.)

`tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "noEmit": true,
    "skipLibCheck": true,
    "types": ["vitest/globals"]
  },
  "include": ["src"]
}
```

`index.html` (no inline scripts):

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>serf</title>
    <link rel="manifest" href="/manifest.webmanifest" />
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

`src/main.tsx`:

```tsx
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles/tokens.css";
import "./styles/global.css";

createRoot(document.getElementById("root")!).render(<App />);
```

`src/App.tsx` (placeholder until Task 8):

```tsx
export function App() {
  return <main>serf workspace shell — wave 1</main>;
}
```

`src/styles/tokens.css` starts with only `:root { color-scheme: dark light; }` (M2 owns real
tokens); `global.css` only a margin reset. `frontend/.gitignore`:

```
node_modules/
dist/*
!dist/PLACEHOLDER
```

`frontend/dist/PLACEHOLDER` contains one line: `run make build-web` (keeps `go:embed` of
`frontend/dist` valid before the first build).

- [ ] **Step 3: Failing test, then green.** `src/App.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import { App } from "./App";

test("renders the shell placeholder", () => {
  render(<App />);
  expect(screen.getByText(/workspace shell/i)).toBeTruthy();
});
```

Run: `npm run test` → PASS (1 test). Run `npm run build` → emits `dist/index.html` +
`dist/webassets/*`.

- [ ] **Step 4: Make targets.** In the top-level `Makefile`, following its existing style:

```make
build-web:
	cd cmd/serf-hub/frontend && npm ci && npm run build

test-web:
	cd cmd/serf-hub/frontend && npm ci && npm run typecheck && npm run test && npm run lint
```

and add `build-web` as a prerequisite of the existing `build-hub` target. Run `make build-web`
and `make test-web`; both must exit 0.

- [ ] **Step 5: CI.** In `.github/workflows/ci.yml`, add a `web` job: checkout,
  `actions/setup-node` with `node-version: 22` and npm cache keyed on
  `cmd/serf-hub/frontend/package-lock.json`, then `make test-web && make build-web`. Add
  `make build-web` (with the same Node setup) before the Go build/test steps of the existing
  job(s) so `go:embed all:frontend/dist` (Task 2) always has a real build in CI.

- [ ] **Step 6: Commit.**

```bash
git add cmd/serf-hub/frontend Makefile .github/workflows/ci.yml
git commit -m "webui: scaffold TypeScript frontend (vite+react+vitest) with make/CI wiring"
```

---

### Task 2: Hub serves the SPA behind SERF_HUB_WEB=new

**Files:**
- Create: `cmd/serf-hub/webnext.go`, `cmd/serf-hub/webnext_test.go`
- Modify: `cmd/serf-hub/web.go` (route registration only, in `Handler()`)

**Interfaces:**
- Consumes: `frontend/dist` (Task 1).
- Produces: `newWebEnabled() bool`; `/webassets/*` (always registered);
  SPA fallback for `/`, `/new`, `/s/`, `/settings`, `/credentials`, `/thread/` when enabled.

- [ ] **Step 1: Failing tests.** `webnext_test.go` (follow the existing `web_test.go` style for
  constructing a `WebServer`; reuse its test helpers for auth cookies):

```go
func TestWebNextServesSPAWhenEnabled(t *testing.T) {
	t.Setenv("SERF_HUB_WEB", "new")
	s := newTestWebServer(t) // existing helper pattern in web_test.go
	for _, path := range []string{"/", "/new", "/s/some-ref", "/settings/theme", "/thread/x"} {
		rr := authedGet(t, s, path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: code=%d", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), `id="root"`) {
			t.Fatalf("%s: not the SPA shell", path)
		}
	}
}

func TestWebNextLegacyDefaultUnchanged(t *testing.T) {
	s := newTestWebServer(t)
	rr := authedGet(t, s, "/")
	if strings.Contains(rr.Body.String(), `id="root"`) {
		t.Fatal("legacy default must serve the old shell")
	}
}

func TestWebAssetsServedWithImmutableCache(t *testing.T) {
	// dist contains PLACEHOLDER at minimum; write a fake hashed asset into the
	// dev override dir or assert on the embedded PLACEHOLDER path.
	s := newTestWebServer(t)
	rr := authedGet(t, s, "/webassets/../dist/PLACEHOLDER") // must NOT escape
	if rr.Code == http.StatusOK {
		t.Fatal("path escape must not serve")
	}
}

func TestWebNextWithoutBuildServes503(t *testing.T) {
	t.Setenv("SERF_HUB_WEB", "new")
	// force the no-index case via the test seam below
	s := newTestWebServerWithDist(t, fstest.MapFS{"PLACEHOLDER": &fstest.MapFile{Data: []byte("x")}})
	rr := authedGet(t, s, "/")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503 run-make-build-web page", rr.Code)
	}
}
```

Run: `go test ./cmd/serf-hub/ -run TestWebNext -v` → FAIL (helpers/handler missing).

- [ ] **Step 2: Implement `webnext.go`.**

```go
package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

//go:embed all:frontend/dist
var frontendDistFS embed.FS

// newWebEnabled reports whether the rewritten SPA serves the page routes.
// Default is the legacy UI until the M9 cutover flips it.
func newWebEnabled() bool { return os.Getenv("SERF_HUB_WEB") == "new" }

// distFS returns the built frontend, a fs.FS seam so tests can substitute one.
var distFS = func() fs.FS {
	sub, err := fs.Sub(frontendDistFS, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// serveSPAIndex serves the app shell for every page route: client routing owns
// the path. 503s with instructions when the frontend was never built.
func serveSPAIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("serf-hub web app not built: run `make build-web` and rebuild\n"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// webassetsHandler serves the hashed Vite output. Hashed filenames are
// immutable by construction, so far-future caching is correct.
func webassetsHandler(dist fs.FS) http.Handler {
	inner := http.StripPrefix("/webassets/", http.FileServerFS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		inner.ServeHTTP(w, r)
	})
}
```

(Adjust the `StripPrefix` inner path so `/webassets/x.js` maps to `dist/webassets/x.js` — Vite
emits into `dist/webassets/`; i.e. strip `/` only. Get this right against a real `make
build-web` output and pin it with the test.)

- [ ] **Step 3: Register routes in `Handler()`** (`web.go`), next to the existing `/assets/`
  registration: always `mux.Handle("/webassets/", webassetsHandler(distFS()))`; and in each page
  handler (`handleIndex`, `handleSession`, `handleSettings`, `handleCredentials`,
  `handleThreadDocument`) add at the top:

```go
if newWebEnabled() {
	serveSPAIndex(w, r, distFS())
	return
}
```

This keeps route registration and auth-guard behavior identical between modes.

- [ ] **Step 4: Green + legacy suite.** `go test ./cmd/serf-hub/ -run 'TestWebNext|TestWeb' -v`
  → all PASS, plus a full `go test ./cmd/serf-hub/` to prove the legacy suite is untouched.

- [ ] **Step 5: Commit.**

```bash
git add cmd/serf-hub/webnext.go cmd/serf-hub/webnext_test.go cmd/serf-hub/web.go
git commit -m "hub: serve rewritten SPA behind SERF_HUB_WEB=new; /webassets immutable assets"
```

---

### Task 3: appwirets — TS types generated from the Go catalog

**Files:**
- Create: `internal/appwirets/emit.go`, `internal/appwirets/emit_test.go`,
  `internal/appwirets/main/main.go` (the `go run`-able entry),
  `cmd/serf-hub/frontend/src/protocol/types.gen.ts` (generated, committed)
- Modify: `appwire/doc.go` (add the `go:generate` line beside the appwiredoc one)

**Interfaces:**
- Consumes: `appwire.Methods` (`MethodSpec{Name, Params, Result, Scope, Summary}`),
  `appwire.Notifications` (`NotificationSpec{Name, Payload, Summary}`).
- Produces: `types.gen.ts` exporting: one interface per Go type (Go name = TS name);
  `MethodName`/`NotificationName` string-literal unions; `MethodTypes`; `NotificationTypes`.
  Anonymous inline notification payloads get names derived from the wire name
  (`thread/started` → `ThreadStartedPayload`).

- [ ] **Step 1: Failing emitter unit test.** `emit_test.go`:

```go
func TestEmitStruct(t *testing.T) {
	type Inner struct {
		A string `json:"a"`
	}
	type Sample struct {
		Inner
		Name  string            `json:"name"`
		Opt   *int              `json:"opt,omitempty"`
		Tags  []string          `json:"tags,omitempty"`
		Meta  map[string]string `json:"meta,omitempty"`
		Raw   any               `json:"raw"`
		Skip  string            `json:"-"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := `export interface Sample {
  a: string;
  name: string;
  opt?: number;
  tags?: string[];
  meta?: Record<string, string>;
  raw: unknown;
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
```

Mapping rules (implement exactly): string→`string`; all int/uint/float→`number`; bool→`boolean`;
slice→`T[]` (`[]byte`→`string`, Go's JSON base64); map→`Record<K, V>`; pointer→unwrap +
`?` if omitempty else `T | null`; `any`/`json.RawMessage`→`unknown`; embedded structs flatten;
`json:"-"` skipped; nested named structs recurse and emit once (topological, alphabetical).
Anonymous structs emit under the caller-provided name. `omitempty`→optional `?`.

- [ ] **Step 2: Implement `emit.go`** until the unit test passes, then a catalog walker:

```go
// EmitCatalog renders the full types.gen.ts from the appwire catalog.
func EmitCatalog() string
```

It walks `appwire.Methods` + `appwire.Notifications`, collects every reachable named type,
emits interfaces, then the unions and lookup tables:

```ts
export type MethodName = "initialize" | "ping" | /* … every catalog name … */;
export type NotificationName = "thread/started" | /* … */;
export interface MethodTypes { "thread/read": { params: ThreadReadParams; result: ThreadReadResponse }; /* … */ }
export interface NotificationTypes { "item/agentMessage/delta": AgentMessageDeltaParams; /* … */ }
export type AnyNotification =
  { [K in NotificationName]: { method: K; params: NotificationTypes[K] } }[NotificationName];
```

Header line: `// Code generated by internal/appwirets; DO NOT EDIT. Run make generate.`

- [ ] **Step 3: Generator entry + go:generate + drift test.**
`internal/appwirets/main/main.go` writes `EmitCatalog()` to the `-out` path. In
`appwire/doc.go`, beside the existing appwiredoc directive:

```go
//go:generate go run primeradiant.com/serf/internal/appwirets/main -out ../cmd/serf-hub/frontend/src/protocol/types.gen.ts
```

Drift test in `emit_test.go`:

```go
func TestGeneratedFileCurrent(t *testing.T) {
	want := appwirets.EmitCatalog()
	got, err := os.ReadFile("../../cmd/serf-hub/frontend/src/protocol/types.gen.ts")
	if err != nil || string(got) != want {
		t.Fatal("types.gen.ts stale: run `make generate`")
	}
}
```

- [ ] **Step 4: Generate + typecheck.** `make generate`, then from `frontend/`:
`npm run typecheck` → the generated file compiles clean. `go test ./internal/appwirets/ -v` →
PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/appwirets appwire/doc.go cmd/serf-hub/frontend/src/protocol/types.gen.ts
git commit -m "appwire: generate TypeScript protocol types from the catalog (drift-tested)"
```

---

### Task 4: AppwireClient — socket, RPC, notifications

**Files:**
- Create: `frontend/src/protocol/transport.ts`, `frontend/src/protocol/client.ts`,
  `frontend/src/protocol/errors.ts`, `frontend/src/protocol/client.test.ts`,
  `frontend/src/protocol/testing/fakeSocket.ts`

**Interfaces:**
- Consumes: `types.gen.ts`.
- Produces (locked):

```ts
// transport.ts
export interface WebSocketLike {
  send(data: string): void;
  close(code?: number): void;
  onopen: (() => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number }) => void) | null;
  onerror: (() => void) | null;
}
export function rpcURLFromLocation(loc: { protocol: string; host: string }): string; // ws(s)://host/rpc

// errors.ts
export class WireError extends Error { code: number; data?: unknown; serfErrorInfo?: string }
export class RequestTimeoutError extends Error {}

// client.ts
export type ConnectionState = "idle" | "connecting" | "ready" | "reconnecting" | "closed";
// AnyNotification comes from types.gen.ts (Task 3 emits it); client.ts re-exports it.
export interface AppwireClientOptions {
  url: string;
  socketFactory?: (url: string) => WebSocketLike;   // default: real WebSocket
  now?: () => number;                                // default Date.now, tests inject
  clientInfo?: { name: string; version: string };
}
export class AppwireClient {
  constructor(opts: AppwireClientOptions);
  connect(): Promise<InitializeResponse>;   // idempotent; initialize+initialized handshake
  close(): void;                            // -> "closed", no reconnect
  request<M extends MethodName>(m: M, params: MethodTypes[M]["params"],
    opts?: { timeoutMs?: number }): Promise<MethodTypes[M]["result"]>; // default 30_000ms
  onNotification(cb: (n: AnyNotification) => void): () => void;
  onStateChange(cb: (s: ConnectionState) => void): () => void;
  onReady(cb: () => void): () => void;      // fires on every ready incl. after reconnect
  get state(): ConnectionState;
}
```

- [ ] **Step 1: fakeSocket.** `testing/fakeSocket.ts`: implements `WebSocketLike`; records
`sent: string[]`; test helpers `open()`, `receive(obj: unknown)` (JSON-stringifies into
`onmessage`), `closeFromServer(code)`. Auto-replies to `initialize` when
`autoInitialize = true`.

- [ ] **Step 2: Failing tests** (`client.test.ts`, vitest fake timers):

```ts
test("connect performs initialize handshake then notifies ready", async () => { /* … */ });
test("request resolves the matching id and types the result", async () => { /* … */ });
test("request rejects with WireError on error response", async () => { /* … */ });
test("request rejects with RequestTimeoutError after timeoutMs", async () => { /* … */ });
test("notifications dispatch to subscribers with method+params", async () => { /* … */ });
test("requests before ready are rejected except initialize/ping", async () => { /* … */ });
```

Each test constructs `new AppwireClient({ url: "ws://x/rpc", socketFactory: () => fake })`,
drives the fake, and asserts on `fake.sent` frames (`JSON.parse` them; ids increment from 1).

- [ ] **Step 3: Implement client.ts** minimal to green: pending-request map
`Map<number, {resolve, reject, timer}>`; `handleMessage` branches on `id` presence (mirror
`appwire.js:224-240` semantics); notification fan-out; state machine transitions.

- [ ] **Step 4: Run** `npm run test -- protocol/client` → PASS; `npm run typecheck` → clean.

- [ ] **Step 5: Commit.** `git add src/protocol && git commit -m "webui: AppwireClient with typed RPC and notification dispatch"`

---

### Task 5: Heartbeat, reconnect, resubscribe hooks

**Files:**
- Modify: `frontend/src/protocol/client.ts`
- Create: `frontend/src/protocol/reconnect.test.ts`

**Interfaces:**
- Consumes: Task 4 client.
- Produces: heartbeat (app-level `ping` every 20s, 10s timeout → force close); automatic
  reconnect with backoff 250ms → 5s cap (`reconnecting` state); `onReady` refires after each
  successful re-handshake. `close()` disarms everything.

- [ ] **Step 1: Failing tests** (fake timers + fakeSocket):

```ts
test("sends ping every 20s while ready", …);
test("no pong within 10s force-closes and enters reconnecting", …);
test("reconnect backs off 250ms,500ms,…,5s cap and re-initializes", …);
test("onReady fires again after reconnect", …);
test("close() stops heartbeat and reconnection permanently", …);
```

- [ ] **Step 2: Implement** (constants `HEARTBEAT_INTERVAL_MS = 20_000`,
`HEARTBEAT_TIMEOUT_MS = 10_000`, `RECONNECT_BASE_MS = 250`, `RECONNECT_MAX_MS = 5_000` — same
values as today's `appwire.js`).

- [ ] **Step 3: Green, typecheck, commit.**
`git commit -m "webui: client heartbeat + reconnect with capped backoff"`

---

### Task 6: ThreadModel + reducer + golden fixtures

**Files:**
- Create: `frontend/src/protocol/model.ts`, `frontend/src/protocol/reducer.ts`,
  `frontend/src/protocol/reducer.test.ts`,
  `frontend/src/protocol/fixtures/basic-turn.jsonl`,
  `frontend/src/protocol/fixtures/streaming-with-reset.jsonl`,
  `frontend/src/protocol/fixtures/tool-and-jobs.jsonl`,
  `frontend/src/protocol/fixtures/queue-and-status.jsonl`

**Interfaces:**
- Consumes: `types.gen.ts` (`Thread`, `Turn`, `ThreadItem`, notification payloads).
- Produces (locked):

```ts
// model.ts
export interface ItemModel {
  id: string; turnId: string; type: string;            // wire ThreadItem.type verbatim
  text: string;                                        // settled text
  pendingText?: string[];                              // in-flight delta chunks (join on complete)
  toolName?: string; callId?: string; argumentsJSON?: string;
  output?: string; images?: string[]; outputImages?: string[];
  status?: string; source?: string;
  reasoningSummaries?: string[][];                     // per summaryIndex chunk lists
  startedAt?: string; completedAt?: string;
}
export interface TurnModel {
  id: string; status: string; items: ItemModel[];
  startedAt?: string; completedAt?: string; durationMs?: number;
  usage?: unknown; cost?: unknown; error?: unknown;
}
export interface ThreadModel {
  ref: string; threadId: string; name: string;
  status: ThreadStatus; modelProvider: string; model: string;
  reasoningEffort?: string; askPending: boolean;
  turns: TurnModel[]; activeTurnId?: string;
  queue: QueueState | null; tasks: { total: number; done: number } | null;
  olderCursor?: string; lastFrameAt: number;           // liveness input
}

// reducer.ts — all pure; unknown notification methods return `model` unchanged
export function hydrateThread(resp: ThreadReadResponse, ref: string, now: number): ThreadModel;
export function applyNotification(model: ThreadModel, n: AnyNotification, now: number): ThreadModel;
export function prependOlderTurns(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel;
export function notificationTargetsThread(n: AnyNotification, model: ThreadModel): boolean; // ref/threadId match
```

- [ ] **Step 1: Fixture format.** Each `.jsonl`: first line
`{"hydrate": <ThreadReadResponse>, "ref": "..."}`, then one wire notification object per line.
Author `basic-turn.jsonl` by hand from the protocol doc's shapes: hydrate an idle thread with
one prior turn → `turn/started` (inline `{threadId, ref, turn}`) → `item/started`
(agentMessage) → three `item/agentMessage/delta` → `item/completed` → `turn/completed`.
The other three fixtures cover: `item/agentMessage/reset` mid-stream +
`item/reasoning/summaryTextDelta` with two summaryIndexes; a `commandExecution` item with
`item/toolOutput/delta` + `serf/job/started`/`finished` + `serf/steering/injected`;
`thread/queueChanged` + `thread/status/changed` + `thread/model/changed` +
`serf/task/updated` + `serf/thread/name/changed`.

- [ ] **Step 2: Failing golden test.**

```ts
for (const f of ["basic-turn", "streaming-with-reset", "tool-and-jobs", "queue-and-status"]) {
  test(`fixture ${f} reduces to the expected model`, async () => {
    const lines = readFixture(f);
    let model = hydrateThread(lines[0].hydrate, lines[0].ref, 1000);
    for (const [i, n] of lines.slice(1).entries())
      model = applyNotification(model, n as AnyNotification, 1000 + i);
    expect(model).toMatchSnapshot();
  });
}
test("delta accumulates into pendingText chunks and joins on completion", …);
test("agentMessage/reset discards the in-flight item", …);
test("notification for a different thread is ignored (same object returned)", …);
test("prependOlderTurns keeps order and advances olderCursor", …);
test("askPending flips from thread snapshot and item lifecycle", …);
```

- [ ] **Step 3: Implement `reducer.ts`.** Semantics to mirror from the old client (behavior,
not code): `item/started` inserts an in-flight item into `activeTurnId`'s turn; deltas append
to `pendingText` (never string-concat per delta); `item/completed` replaces the in-flight item
with the settled wire item (`text` from the payload wins over accumulated chunks);
`agentMessage/reset` removes the in-flight item; `turn/completed` settles status/usage/cost and
clears `activeTurnId`; every applied notification stamps `lastFrameAt = now`. Snapshot-recovery
rule: `hydrateThread` REPLACES the model wholesale (no merge).

- [ ] **Step 4: Green.** `npm run test -- protocol/reducer` → PASS (review the first snapshot
output by eye before accepting it: turns count, item text joins, statuses).

- [ ] **Step 5: Commit.**
`git commit -m "webui: ThreadModel reducer with golden notification fixtures"`

---

### Task 7: Stores — connection, threads, optimistic pending

**Files:**
- Create: `frontend/src/stores/connection.ts`, `frontend/src/stores/threads.ts`,
  `frontend/src/stores/threads.test.ts`, `frontend/src/protocol/testing/fakeClient.ts`

**Interfaces:**
- Consumes: client (Tasks 4-5), reducer (Task 6).
- Produces (locked):

```ts
// fakeClient.ts — what stores are tested against; the seam stores depend on
export interface AppwireClientLike {
  request: AppwireClient["request"];
  onNotification: AppwireClient["onNotification"];
  onReady: AppwireClient["onReady"];
  onStateChange: AppwireClient["onStateChange"];
  get state(): ConnectionState;
}

// connection.ts
export const useConnectionStore: /* zustand */ {
  state: ConnectionState; serverInfo?: ServerInfo;
  connect(client: AppwireClientLike): void;   // idempotent; wires listeners
};

// threads.ts
export const useThreadsStore: {
  threads: Map<string, ThreadModel>;
  ensureThread(ref: string): Promise<void>;   // refcounted: hydrate + subscribe (additive)
  releaseThread(ref: string): void;           // unsubscribes at zero panes (thread/read subscribe:false is NOT sent; just stop tracking)
  send(ref: string, text: string, images?: string[]): Promise<void>;   // turn/start; on Conflict -> throws ConflictError
  steer(ref: string, text: string): Promise<void>;
  queue(ref: string, text: string): Promise<void>;
  interrupt(ref: string): Promise<void>;
};
export class ConflictError extends Error {}
```

Note: full optimistic-pending reconciliation (local echo entries matched to server `item/…`
notifications, today's `pending.js`) intentionally lands with the composer in Wave 3 (M5); it
will EXTEND this store's interface (an added `pending` field), never change these signatures.

- [ ] **Step 1: Failing tests** with `fakeClient` (scripted request handlers + manual
notification injection): `ensureThread` hydrates and routes matching notifications through the
reducer; second `ensureThread(ref)` doesn't re-read; after simulated reconnect (`onReady`
refire), every tracked ref re-hydrates and re-subscribes additively
(`replaceSubscription: false`); `send` maps WireError code for conflict (`error.code` from a
scripted rejection) to `ConflictError`; notifications for untracked refs are dropped.

- [ ] **Step 2: Implement.** zustand vanilla stores with a react hook export; subscription calls
are exactly `thread/read {ref, includeTurns: true, itemsView: "full", subscribe: true,
replaceSubscription: false, turnLimit: 40}` on ensure and on every `onReady`.

- [ ] **Step 3: Green, typecheck, commit.**
`git commit -m "webui: connection + threads stores over the protocol core"`

---

### Task 8: Dev harness page — live end-to-end proof

**Files:**
- Modify: `frontend/src/App.tsx`
- Create: `frontend/src/dev/DevHarness.tsx`, `frontend/src/dev/DevHarness.test.tsx`

**Interfaces:**
- Consumes: everything above.
- Produces: a temporary raw UI (Wave 2 replaces it): connection state line; `thread/list`
  results as clickable rows; the selected thread's `ThreadModel` rendered as `<pre>` JSON that
  updates live. No styling beyond monospace.

- [ ] **Step 1: Component test** with fakeClient: renders threads from a scripted
`thread/list`, clicking a row calls `ensureThread` and shows the model JSON, an injected
`item/agentMessage/delta` updates the JSON text.

- [ ] **Step 2: Implement; green; typecheck.**

- [ ] **Step 3: Live verification (manual, run and paste results into the task report).**

```bash
make build-hub && SERF_HUB_WEB=new <hub binary> &  # binary path: check `make -n build-hub`; do not guess
cd cmd/serf-hub/frontend && npm run dev            # open the printed URL, /auth?token=… first
# in another terminal: spawn a real session via the CLI against the same hub and send a prompt
```

Expected: harness lists the session; opening it shows turns; a live prompt streams deltas into
the JSON view without reload; killing the hub shows `reconnecting` then recovery after restart.

- [ ] **Step 4: Commit.** `git commit -m "webui: dev harness proves protocol core live against hub"`

---

### Task 9: Wave gate

- [ ] `make test-web` → 0 failures; `go test ./cmd/serf-hub/ ./internal/appwirets/` → PASS;
  `make lint && make test` (all modules) → PASS; `make build-hub` embeds a real build.
- [ ] Re-run the legacy smoke: default (no env) hub serves the old UI; `web_test.go` untouched.
- [ ] Commit any stragglers; write `docs/superpowers/plans/wave1-report.md` with what shipped,
  deviations, and interface changes (if any — update THIS plan's Locked Interfaces section in
  the same commit).
