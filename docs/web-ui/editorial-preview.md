# Editorial full-app preview

This development-only entry renders the **real AppShell, routing, stores, panes,
transcript engine and widgets** against a deterministic `FakeClient` RPC boundary.
It is not a live hub or a provider execution test. Fixture sends and question answers
stay in this browser's in-memory client; reload resets fixture session data. Real
browser-local preferences persist. Never enter credentials in this preview.

## Launch from the integrated deliverable worktree

```sh
cd cmd/evener-hub/frontend
npm ci
./node_modules/.bin/vite --config scripts/editorial-preview.vite.config.mjs \
  --host 0.0.0.0 --port 5197 --strictPort --clearScreen false
```

Check that 5197 is free before launch; `--strictPort` fails rather than changing the
URL. Parent owns the final detached process, launch log, listener/PID verification
and teardown of its obsolete preview. Expected landing URL: **http://m5:5197/**.
A phone may use the same host/port. This lane's browser runner starts and stops its
own private Vite and Chrome; it never uses shared Chrome or a live service.

The dedicated Vite config explicitly removes the **resolved** inherited proxy,
limits filesystem serving to this frontend (including its own `npm ci` install),
allows `m5`, and denies hub endpoints. App-route reloads load the fixture entry.
`/index.html` is still the real router's NotFound route; “Go home” recovers. No
production router was changed. Vite 8 retains its same-origin root development
WebSocket with HMR updates disabled; this is not `/rpc` or a hub connection.

## Actual-UI panel tasks

Use a private browser profile. Compare light/dark, M/XL text, 360/390px phones,
wide desktop and a narrow desktop split. Theme/font controls are at
`/settings/theme`; choose them normally, then reload to check persistence.

| Role | Route and concrete tasks | Source coverage |
|---|---|---|
| Phone workflow | `/`; use Sessions drawer/rail to open **Editorial fixture parent**, type a fixture-only message and Send; open **Editorial fixture question**, choose Tool evidence and Send answers; return to parent. | AppShell, StackHost, rail, real Composer and AskDock; scripted reflected mutation receipt + authored reply + navigation invalidation. |
| Tool evidence/failure | `/s/local%3Aeditorial-parent`; distinguish completed/failed/in-progress shell rows. Disclose **Reading long native source evidence**: first disclose action if hidden, then activate the action-line chevron to open native numbered output. Click **Show 106 earlier lines** to expose the long line 31, then scroll the real transcript to inspect its wrapping without losing adjacent controls. | ToolRow's action/body disclosures plus CodeBlock's retained-tail disclosure; real shell and numbered source renderers; long retained source wraps within its code block; failure remains expanded. |
| Collaborators | Parent; compare **Idle · reported**, **Running** with a previous authored report, and **Status unavailable**. Use independent **Open transcript**, inspect the distinct child content, then Back/parent rail. Also open **Editorial fixture child** directly from rail. | Owning stable diagnostics projections deliberately disagree with old launch receipts. Distinct child/ref snapshots; real read-only Transcript and session navigation. No launch receipt supplies current state. |
| Nested collaborators | At `/s/local%3Aeditorial-parent`, use **Open transcript** on **Inspect the independent child transcript**, then on **Inspect editorial-grandchild independently**. Check grandchild-authored evidence, no composer, and Back to the immediate child. Separately load `/s/local%3Aeditorial-child` and repeat; from grandchild open **Inspect editorial-great-grandchild independently**, then Back to grandchild. Reopen a descendant, open Sessions, and select **Editorial fixture resumed**: the explicit new route must win. | Real trusted-pointer Open/Back/new-route automation; exact target and read-only focus, original parent stays main, immediate parent retained, no intermediate location prefetch or workspace mutation. Six distinct snapshots, truthful immediate-parent and top-level-owner navigation. Inline collaborator reports can legitimately show descendant prose before opening; inspect the transcript's own authored message, not just matching body text. |
| Settings/keyboard | `/settings/theme` and `/settings/credentials`; change preferences/reload, open **+ Add provider instance**, submit empty form to see validation, Escape and verify trigger focus. Do not save credentials or test a provider. In parent, focus Session actions, Enter, Escape; open Details/Tasks/Activity panels. | Real settings sections, provider form, Menu/Dialog, preference persistence, actual dock split. Activity is explicitly partial and derived from known owner projections only. |

The read-only child workflow and all geometry assertions are acceptance checks,
not implied endorsements. Consult the task-4 report for current gate results and
any remaining production findings before using this as a reviewed preview.

For the three hands-on panel roles: **phone workflow** owns the phone row plus
Send/answer and direct-child Open/Back; **tool/delegate** owns native evidence,
lifecycle distinctions and both nested-route paths; **workspace/accessibility**
owns keyboard/settings, actual Details/Tasks/Activity, and wide versus narrow
pane comparison. Automation is not the still-separate hands-on panel endorsement.

## Verification

```sh
npx vitest run src/dev/editorial-preview
node --test scripts/editorial-preview*.test.mjs
node scripts/editorial-preview-browser.mjs
```

The private browser runner writes screenshots, computed geometry, RPC errors,
network requests, task outcomes and process identities to ignored
`.superpowers/sdd/2026-09-09-tufte-webui/task-4-evidence/` at the repository root.
Override its destination with `EDITORIAL_EVIDENCE=/absolute/path`.
It exits nonzero on any collected geometry failure; collecting the other cases
never turns a failed assertion green. Run repository `make test-web`,
`EVENER_HUB_ADDR=http://127.0.0.1:1 make test-web-browser` (all five unchanged guards),
and frontend `npm run build` after integration.

The original matrix is 16 configurations (360×800, 390×844, 1280×1000 narrow
split, 1440×1000 wide; light/dark × M/XL), with evidence and collaborator samples
for 32 measurements. Wide checks require an actual ≥880px transcript and nonempty
timestamps; narrow timestamps stay static. Desktop collaborator geometry is
captured atomically with positive visible/hit-tested lifecycle/Open readiness.
Phone Send, generic Open and Open transcript keep both 44px dimension floors;
the independent mutation regression rejects a 1×1 Open transcript target.
Earlier-lines native disclosure uses trusted pointer events, and native scroll
evidence retains the complete 90×`long-path/` token and visible hit-tested text.

## Coverage limits

This validates deterministic frontend interactions, not real provider execution,
live transport, credentials, session daemon persistence or a hands-on usability
panel's endorsement. Only the named fixture session refs/RPCs are supported;
unexpected RPCs reject and are recorded. Spawn catalogs and directory validation
are fixture-backed, but starting a provider session, file/document/image fetching,
credential writes/tests, job-output execution and unrelated settings RPCs are not
simulated. Existing real-component widget/type/surface galleries and five browser
guards supplement this full-shell fixture; they do not substitute for its tasks.
Parent owns independent review, broad Go gates, hands-on panel/fixes/retests and
the eventual unmerged PR.
