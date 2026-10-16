# Skill Description Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Load skills with the reported unquoted-description mistake while preserving controls, source bytes and useful sessions.

**Architecture:** Keep the shared YAML parser strict. Add a skill-only fallback used by discovery and activation, and give startup skill warnings explicit classification at their session owner. Existing client projections carry these fields.

**Tech Stack:** Go, yaml.v3, existing scripted-provider test harness.

**Spec:** `docs/superpowers/specs/2026-10-16-skill-description-recovery-design.md`

## Global Constraints

- No installed-file writes, live hub restart, new dependencies or shared YAML-parser relaxation.
- Names and invocation controls retain their current validation.
- Tests use structured contracts and fixture data, never production prose as an oracle.
- All edits stay in this worktree. Leave the main checkout's untracked native script untouched.
- Full suites run in CI; local checks target changed packages.

## Review Focus

- An invalid control beside a recoverable description must still make the skill unavailable.
- YAML comments, nested descriptions and scalar syntax must not be reinterpreted as prose.
- Activation must agree with discovery and hash original bytes, not repaired metadata.
- A healthy session must complete input with a malformed sibling installed.
- Repair and rediscovery must restore the broken skill without rewriting installed files.

## Files and ownership

- `agent/skill/description.go` (new): skill-specific document parse and recovery.
- `agent/skill/metadata.go`, `agent/skill/load.go`: consume the shared skill parser.
- `agent/skill/description_test.go` (new): parser, discovery, loading and repair evidence.
- `agent/skill/fuzz_ap_skills_test.go`: independent body oracle for newly accepted inputs.
- `agent/session_init.go`: construct classified skill diagnostics.
- `agent/events/payloads.go`: machine-readable code for non-advisory skill diagnostics.
- `agent/session_skill_description_test.go` (new): real-session journey and warnings.
- `agent/session_skills_test.go`: adapt touched compatibility-warning expectations to structured assertions.
- `internal/appoverlay/notices_test.go`: projection preservation test if existing coverage does not prove the contract.
- `docs/skills.md`, `docs/product/subsystems.md`: evergreen behavior and ownership.

## Task 1: Recover description scalars through activation

**Interface:** `parseSkillDocument(data []byte) (frontmatter.Document, bool, error)` in `agent/skill/description.go`; the bool indicates description recovery. `Parse` and `Load` retain their public signatures and existing missing-delimiter normalization.

- [ ] Add `TestSkillDescriptionRecovery` with both reported description shapes and flags. Assert successful parsing, exact fixture description, original identity, `UserInvocable == false`, and diagnostic category `unquoted_description` with field `description`.
- [ ] Add table cases for LF, CRLF and BOM; unchanged valid plain/quoted/block/commented values; malformed quoted/collection/tag/anchor values, duplicate keys, nested descriptions, continuation lines, unrelated YAML failures and invalid booleans. Recovered descriptions never make invalid controls available.
- [ ] Add recovery-refusal fixtures with `...` followed by `user-invocable: false`, invalid trailing garbage, or a second YAML document. Assert failure and unavailability; also cover a harmless terminal `...` with no trailing content.
- [ ] Add `TestSkillDescriptionDiscoveryLoadAndRepair`: discover recovered, healthy and broken siblings; load/render recovered instructions; compare original bytes and SHA-256; correct only the test-owned broken file and rediscover its availability.
- [ ] Run `go test ./agent/skill -run 'TestSkillDescription' -count=1` and retain the failing behavioral evidence.
- [ ] Implement the narrow fallback: first strict parse; on error, inspect framed metadata for one eligible top-level plain description line with colon-space. Quote its prose while preserving YAML comments, then decode the mapping with `yaml.NewDecoder` and require the next decode to return `io.EOF`. `yaml.Unmarshal` alone does not validate trailing content. Decline ambiguous syntax or continuation. Never rewrite source bytes. Use the helper in both Parse and Load, and emit the recovery diagnostic after obtaining identity.
- [ ] Update the existing fuzz body's oracle to split the body independently after established delimiter normalization, rather than requiring strict YAML reparsing of an accepted source. Add a recovered-description seed.
- [ ] Run `go test ./agent/skill ./agent/internal/frontmatter -count=1` and commit only Task 1 files after inspecting the staged diff.

## Task 2: Keep skill problems local and actionable

**Interface:** `skillDiagnosticWarning(d skill.Diagnostic) events.WarningData` in `agent/session_init.go`. Compatibility categories use `events.WarningCodePluginCompatibility`; other categories use new `events.WarningCodeSkillDiagnostic = "skill_diagnostic"`.

- [ ] Add `TestSkillDescriptionSessionJourney`: a real Session discovers a recovered plugin skill and a broken sibling; a scripted provider calls `use_skill`, receives the fixture's complete structured instructions, and completes a turn. Assert original user input preservation, activation events, warning codes, no error event and unchanged source files.
- [ ] Cover successful recovery as compatibility detail, malformed YAML and invalid controls as ordinary skill diagnostics, and collision as a diagnostic that does not label the available winner unavailable. Use structured category/code and availability assertions; review the supplied title/hint in the diff.
- [ ] Add or extend a projection test with sentinel input fields to prove `internal/appoverlay.warningAnnouncement` retains supplied title/hint/code as warning data, not an error. Do not pin production copy.
- [ ] Run `go test ./agent -run 'TestSkillDescription|TestInitPlugins_SkillCompatibility' -count=1` and show the failure before changing diagnostic production.
- [ ] Build explicit warning payloads in the session owner. Use a generic skill-diagnostic title for non-advisory categories and guidance to the source/field plus next discovery, without suggesting daemon trouble. Add recovery to advisory categories. Retain ordinary visibility for unavailable skills.
- [ ] Update the skill guide and subsystem map with recovery, immutable sources, diagnostics and existing rediscovery boundaries.
- [ ] Run `go test ./agent -run 'TestSkillDescription|TestInitPlugins_SkillCompatibility|TestUseSkill|TestSkillCatalog' -count=1`, `go test ./internal/appoverlay -count=1`, and the Task 1 packages. Format and lint changed Go packages with the pinned tooling before committing explicit paths.

## Final review and delivery

- [ ] Perform two independent adversarial reviews against the spec and frozen diff; resolve evidenced findings with regression tests.
- [ ] Run the simplify-code workflow and affected checks.
- [ ] Commit intended files, create the PR, shepherd current-head checks and reviews, and merge under repository policy.
- [ ] Verify merged content, preserve the user's main-checkout script, and clean owned scratch/worktree. Report evidence and any limits. Deployment remains separate.
