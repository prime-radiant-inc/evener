# Task 1 Report: Frontend scaffold, Makefile targets, CI job

Status: **DONE**
Commit: 97aff0ff6436e57e4d0059ad28c2ea34de41dafa — "webui: scaffold TypeScript frontend
(vite+react+vitest) with make/CI wiring"

(Note: this file previously held a report from an unrelated, already-merged earlier SDD wave
— "Plan B web UI layout + CSS consolidation" / branch `webui-joy-b`, task 1 = "Width scale —
`--measure` tokens". That content is gone now; this report is entirely about the current
Workspace-shell rewrite Wave 1, Task 1.)

## What I implemented

Exactly the file set the brief specified, in `cmd/serf-hub/frontend/`:

- `package.json` — name `serf-hub-frontend`, `private: true`, `type: module`, scripts
  `dev|build|typecheck|test|lint` verbatim from the brief.
- `vite.config.ts` — Vite config with the hub dev proxy (`/rpc` ws, `/api`, `/auth`, `/doc`,
  `/s` image-only bypass) and `build: { assetsDir: "webassets", outDir: "dist", emptyOutDir: true }`.
- `tsconfig.json` — strict TS config targeting `src/`.
- `index.html`, `src/main.tsx`, `src/App.tsx` — entry point and placeholder shell component.
- `src/styles/tokens.css`, `src/styles/global.css` — minimal starting styles (M2 owns real tokens).
- `src/App.test.tsx` — the one specified test.
- `eslint.config.js` — flat config (per controller decision, not `.eslintrc.cjs`).
- `frontend/.gitignore`, `frontend/dist/PLACEHOLDER`.
- `Makefile` — added `build-web`, `test-web`; `build-hub` now depends on `build-web`.
- `.github/workflows/ci.yml` — new `web` job; `build-and-test` now runs `make build-web`
  (with Node setup) before its Go build/test steps.

Packages installed at their current (July 2026) versions via plain `npm install`, no hand-pins:
React 19.2.7, Vite 8.1.5, TypeScript 6.0.3, vitest 4.1.10, ESLint 10.7.0, typescript-eslint 8.65.0
— see `package.json`/`package-lock.json` for the full set.

## Controller decisions applied (deviations from the brief's literal code samples)

1. **`vite.config.ts`**: `import { defineConfig } from "vitest/config"` (not `"vite"`), so the
   `test` key typechecks.
2. **`tsconfig.json`**: no `"types": ["vitest/globals"]`.
3. **`src/App.test.tsx`**: explicit `import { test, expect } from "vitest";` instead of relying
   on globals (required by decision #2 — vitest doesn't inject globals unless
   `test.globals: true` is set, which we didn't set).
4. **ESLint**: flat config (`eslint.config.js`), typescript-eslint recommended setup.

## A brief gap I found and fixed, with evidence (not invented)

Even with the brief's literal `tsconfig.json`, `tsc --noEmit` failed:

```
src/main.tsx(3,8): error TS2882: Cannot find module or type declarations for side-effect import of './styles/tokens.css'.
src/main.tsx(4,8): error TS2882: Cannot find module or type declarations for side-effect import of './styles/global.css'.
```

`main.tsx`'s two CSS side-effect imports (both specified verbatim by the brief) have no ambient
module declaration without Vite's client types. I confirmed `node_modules/vite/client.d.ts` ships
`declare module '*.css' {}` and added `"types": ["vite/client"]` to `tsconfig.json`
`compilerOptions` — no new dependency (it ships inside the already-installed `vite` package).
This is the standard fix for every Vite+TS project; `npm run build`/`npm run typecheck` are clean
after it.

## ESLint flat config: grounded in the installed package, not memory

Per CLAUDE.md's "never invent technical details" rule, before writing `eslint.config.js` I read
the actually-installed packages rather than relying on possibly-stale training knowledge:

- `node_modules/typescript-eslint/dist/config-helper.d.ts` shows `tseslint.config()` is now
  `@deprecated` in favor of ESLint core's own `defineConfig()`.
- `node_modules/eslint/lib/types/config-api.d.ts` confirms `eslint` (v10.7.0, already a brief
  dependency) re-exports `defineConfig` from the `"eslint/config"` subpath — no new package needed
  (`@eslint/js` is NOT installed and NOT required, since `tseslint.configs.recommended` is
  self-contained).
- `node_modules/@eslint/config-helpers/dist/esm/index.js` (`extendConfig`/`extendConfigFiles`)
  confirms that setting `files` on the top-level `defineConfig({...})` object correctly propagates
  onto every sub-config pulled in via `extends: [tseslint.configs.recommended]`, since
  `@typescript-eslint/eslint-plugin`'s recommended/base configs declare no `files` of their own
  (grepped for it — no matches).

Final config:

```js
import { defineConfig } from "eslint/config";
import tseslint from "typescript-eslint";

export default defineConfig({
  files: ["src/**/*.ts", "src/**/*.tsx"],
  extends: [tseslint.configs.recommended],
});
```

I did not just trust the exit code — I empirically verified ESLint actually scans `.tsx`/`.ts`
files (not silently matching zero files) by dropping a throwaway `src/__lint_probe.ts` with an
unused variable, confirming `npm run lint` caught it (`@typescript-eslint/no-unused-vars`, exit 1),
then deleting the probe file.

## `global.css`: "only a margin reset"

The brief specifies `tokens.css`'s exact content but only describes `global.css` as "only a margin
reset" without exact code. I used the smallest literal reading:

```css
body {
  margin: 0;
}
```

## `dist/PLACEHOLDER` and `emptyOutDir: true` — expected, not a bug

`vite build`'s `emptyOutDir: true` (specified verbatim by the brief) deletes everything in `dist/`
before writing output, including the committed `dist/PLACEHOLDER`. I verified this happens on every
build. This matches the brief's own stated purpose for the file — "keeps go:embed of frontend/dist
valid **before the first build**" — once a real build exists, `dist/` has real content and
`go:embed` is satisfied regardless of whether `PLACEHOLDER` itself is still present on disk. Net
effect: after running `make build-web` locally, `git status` will show `dist/PLACEHOLDER` as
deleted until you either restore it or just don't stage that deletion. This is inherent to the
pattern as specified (not something I introduced or could sensibly avoid — `emptyOutDir: false`
would instead leave stale hashed bundles accumulating forever). Flagging it here so it isn't a
surprise later; I recreated the file before my own commit so it stays tracked in git.

## `package-lock.json` name drift — found and fixed

`npm init -y` initially set `package.json`'s `"name"` to `"frontend"`; I ran the two `npm install`
commands (which generated `package-lock.json`) *before* editing `package.json`'s `name` to
`"serf-hub-frontend"`. `npm ci` (used by `make build-web`/`make test-web`) does not rewrite the
lockfile, so the stale `"name": "frontend"` in `package-lock.json` survived several successful
`npm ci` runs silently. I caught this on final review (inspecting the lockfile's JSON directly),
fixed it with a plain `npm install` (refreshes lockfile metadata without changing any resolved
dependency version — confirmed via `git diff`, a 2-line diff touching only the two `name` fields),
then re-ran the full verification pass and re-committed.

## TDD evidence (Step 3: "Failing test, then green")

I sequenced this as genuine RED→GREEN: wrote `src/App.test.tsx` (importing `./App`) while
`src/App.tsx` did not yet exist, ran the suite, confirmed a real failure, then created `App.tsx`
and confirmed the suite passed. (The brief's own listed file order groups `App.tsx` under "Step 2:
Configs" with its final placeholder text already decided — content-wise there's no room for a
"wrong then right" cycle once that exact text is given, so the genuine RED had to come from
module existence rather than assertion content.)

RED:

```
$ npm run test

> serf-hub-frontend@1.0.0 test
> vitest run

 RUN  v4.1.10 .../cmd/serf-hub/frontend

 ❯ src/App.test.tsx (0 test)

⎯⎯⎯⎯⎯⎯ Failed Suites 1 ⎯⎯⎯⎯⎯⎯⎯

 FAIL  src/App.test.tsx [ src/App.test.tsx ]
Error: Failed to resolve import "./App" from "src/App.test.tsx". Does the file exist?
  Plugin: vite:import-analysis
  ...
 Test Files  1 failed (1)
      Tests  no tests
```

GREEN (after creating `src/App.tsx`):

```
$ npm run test

> serf-hub-frontend@1.0.0 test
> vitest run

 RUN  v4.1.10 .../cmd/serf-hub/frontend

 Test Files  1 passed (1)
      Tests  1 passed (1)
   Duration  389ms (transform 14ms, setup 0ms, import 81ms, tests 12ms, environment 215ms)
```

## Full verification (final pass, after the package-lock.json fix, right before committing)

`npm run test`:

```
 Test Files  1 passed (1)
      Tests  1 passed (1)
```

`npm run build`:

```
> tsc --noEmit && vite build

vite v8.1.5 building client environment for production...
✓ 17 modules transformed.
dist/index.html                      0.45 kB │ gzip:  0.27 kB
dist/webassets/index-DGhJFrDq.css    0.18 kB │ gzip:  0.12 kB
dist/webassets/index-DcmX0Qvp.js   190.44 kB │ gzip: 60.00 kB
✓ built in 68ms
```

`make build-web` (from repo root): exit 0, `npm ci` (200 packages, 0 vulnerabilities) +
`npm run build` succeeded, emitted `dist/index.html` + `dist/webassets/*`.

`make test-web` (from repo root): exit 0, `npm ci` + `npm run typecheck` (clean) +
`npm run test` (1 passed) + `npm run lint` (clean, no output).

Bonus regression check (not required by the brief, but I touched the shared `build-hub` line so I
verified it): `make build-hub` — exits 0, runs `build-runtime` then `build-web` in that order
(matches `build-hub: build-runtime build-web`), and produces real `./serf` and `./serf-hub`
binaries at repo root (both already gitignored, confirmed via `git status`).

`.github/workflows/ci.yml` parses as valid YAML (`python3 -c "import yaml; yaml.safe_load(...)"`) —
jobs `web` and `build-and-test` both present.

## Files changed

Created (`cmd/serf-hub/frontend/`): `.gitignore`, `dist/PLACEHOLDER`, `eslint.config.js`,
`index.html`, `package-lock.json`, `package.json`, `src/App.test.tsx`, `src/App.tsx`,
`src/main.tsx`, `src/styles/global.css`, `src/styles/tokens.css`, `tsconfig.json`,
`vite.config.ts`.

Modified: `Makefile` (added `build-web`, `test-web` targets + their two names in `.PHONY`;
`build-hub` now depends on `build-web`), `.github/workflows/ci.yml` (new `web` job; `build-and-test`
runs `make build-web` with Node setup before its Go steps).

Not touched (correctly out of scope): `.github/workflows/binaries.yml` — it runs `make dist`,
which builds `serf-hub` and would eventually want a real frontend build too once Task 2 adds the
`go:embed` directive, but the brief scopes CI changes to `ci.yml` only, and Task 2 (not this task)
is what makes `frontend/dist` load-bearing for any Go build. Noting this so it isn't lost track of,
not acting on it.

## Self-review

- **Completeness**: every checkbox in the brief done — package scaffold, configs, RED→GREEN test,
  make targets + build-hub dependency, CI job + pre-Go-build step, commit. Verified `npm run test`,
  `npm run build`, `make build-web`, `make test-web` all exit 0 as the task instructions require.
- **Quality**: names match the brief exactly where specified; the two judgment calls
  (`global.css` content, ESLint flat-config shape) are minimal and grounded in the installed
  toolchain, not guessed.
- **Discipline (YAGNI)**: no dependencies beyond the brief's two `npm install` lines (confirmed
  `@eslint/js` was deliberately NOT added — not needed, would have been an unlisted dependency).
  No files beyond the brief's list. Did not touch `binaries.yml` (out of scope). Did not add
  type-checked ESLint rules, React-specific lint plugins, or any other unrequested tooling.
- **Testing**: the one specified test exercises real `render()`/`screen.getByText()` behavior
  against the real `App` component (no mocking). Output is pristine — no stray console warnings
  in any of the `npm run test` runs.

No open concerns beyond what's documented above (all resolved, not deferred).

## Fix wave 1

### What changed

1. **Created `.npmrc`** with `ignore-scripts=true` to enforce the project's no-postinstall-scripts constraint at npm install time. This prevents any install hooks from executing (the only candidate is the optional `fsevents` package, which degrades gracefully to polling file-watch when skipped).

2. **Removed boilerplate from `package.json`**:
   - Deleted `"description": ""`
   - Deleted `"main": "index.js"`
   - Deleted `"keywords": []`
   - Deleted `"author": ""`
   
   All other fields retained exactly as they were.

### Commands run and results

All commands executed in `cmd/serf-hub/frontend/`:

- `npm ci` — installed 200 packages; no install-script execution observed (`.npmrc` constraint confirmed active).
- `npm run build` — `tsc --noEmit && vite build` exited 0; produced `dist/index.html` (0.45 kB gzip) and `dist/webassets/{index-*.css,index-*.js}` (60.00 kB gzip).
- `npm run test` — vitest 1 file passed, 1 test passed, 358ms total.
- `npm run lint` — eslint on `src/` exited 0 (clean, no violations).
- `npm run typecheck` — `tsc --noEmit` exited 0 (clean, no type errors).

All exit codes 0; no errors or warnings.
