# Codex Model-List Client Version Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Default Codex model discovery to `client_version=0.888.0` and expose a clearable provider-specific override in Advanced connection settings.

**Architecture:** Carry `codex_client_version` on the layered registry transport and substitute the `client_version` query only for Codex Responses model listing. Carry the authored value through the existing instance list/edit protocol and form draft, without changing completion requests.

**Tech Stack:** Go, TOML, AppWire-generated TypeScript, React, existing Input/Disclosure widgets.

**Spec:** `docs/superpowers/specs/2026-11-17-codex-model-client-version-design.md`

## Global Constraints

- Write each regression test before production code and watch it fail for the intended reason.
- Use scripted HTTP boundaries for default tests; live Codex verification remains manual and opt-in.
- No new widgets or raw controls; no completion-request change; no compatibility machinery.
- Run `make generate` after AppWire changes and Biome on touched TypeScript.

## Review Focus

- A custom models endpoint already has a query: replace only `client_version` and retain others (Task 1).
- Clearing an authored override must inherit `0.888.0`, not omit the required query (Tasks 1–2).
- A non-Codex Responses endpoint must not gain the Codex query (Task 1).
- A non-Codex provider must neither show nor accept this field (Task 2).
- A save or rename that does not declare the field must preserve its prior value (Task 2).

### Task 1: Registry and model discovery

**Files:**
- Modify: `llm/registry/types.go`, `schema.go`, `merge.go`, `write.go`, transport cloning and relevant tests
- Modify: `llm/registry/data/providers_overlay.toml`, `llm/providers/responses/models.go`, `models_test.go`
- Update: affected registry goldens via the repository's sanctioned generator

**Interfaces:** `Transport.CodexClientVersion string`, TOML/JSON name `codex_client_version`.

- [ ] Add failing tests for default `0.888.0`, Codex-base inheritance, config round trip, preserving another query parameter, and no behavior change for non-Codex Responses listings.
- [ ] Run `go test ./llm/registry ./llm/providers/responses` and confirm the new cases fail for the missing default/override.
- [ ] Add the transport field to schema, merge, clone and writer paths; set the curated default; replace the query using `net/url` only when `Transport.Auth == AuthOAuthOpenAICodex`.
- [ ] Regenerate affected goldens, rerun both packages, and inspect output for pristine results.
- [ ] Commit these tested registry/discovery changes with the required provenance trailer.

### Task 2: Provider settings and AppWire

**Files:**
- Modify: `appwire/types_auth_commands.go`, `cmd/evener-hub/app_instances.go` and focused tests
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/{instanceEdit.ts,InstanceSheet.tsx,instanceLanding.ts}` and focused tests
- Generate: `appwire-client/typescript/types.gen.ts`

**Interfaces:** wire fields `codexClientVersion` and `clearCodexClientVersion`; form draft field `codexClientVersion`.

- [ ] Add failing Go tests proving a Codex edit writes and round-trips the value, clear restores inheritance, and a non-Codex edit cannot set it.
- [ ] Add failing frontend tests for the Codex-only field in connection settings, its set/clear mutation, and preservation during unrelated saves.
- [ ] Run the focused tests and confirm the intended failures.
- [ ] Implement authored-entry serialization, create/edit set and clear with the existing pair validation, and regenerate the TypeScript schema with `make generate`.
- [ ] Implement the draft, set/clear diff, save/rebase field tracking, and Codex-only `Input` inside `ConnectionSettingsDisclosure`.
- [ ] Run focused Go tests, frontend vitest/typecheck and Biome on touched files; commit intended files with the required trailer.

### Task 3: Documentation, verification and review

**Files:** Update `docs/llm-providers.md`, the S15 row in `docs/product/subsystems.md`, and the temporary design/plan lifecycle as the repository requires for the PR.

- [ ] Document the key, default, Codex-only scope, and clearing behavior.
- [ ] Run targeted Go suites and `make test-web` (one heavy gate at a time); run `make lint` once before pushing.
- [ ] Manually verify the live signed-in Codex roster with `0.888.0`, including both `gpt-6.2-sol` and `gpt-6.2-astra`.
- [ ] Run `/simplify-code` on the finished change, then one review and a final requirement/diff check.
- [ ] Remove scratch artifacts and report the changed behavior, evidence, and anything unverified.
