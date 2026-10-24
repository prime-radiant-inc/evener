# Issue #4694: browserguard whole-package lint

Status: frozen for independent PAR review. Jesse authorized implementation after spec PAR in the task brief.

## Goal and scope

Make `golangci-lint run --build-tags browserguard ./cmd/evener-hub` exit zero without changing browser fixture assertions or behavior. Work in the isolated `issue-4694-browserguard-lint` lane, initially based on `16ff3909af79adfadbdca3f450a636814eb3ef9e`.

Only these seven files under `cmd/evener-hub/` and this specification may change. A shared test helper is permitted only if strictly required by a finding; no production code or linter configuration changes are permitted.

## Baseline inventory

The exact acceptance command exited 1 at the base with golangci-lint 2.13.1 (Go 1.27.0), producing 72 findings. Counts below come from that run, not changed-lines lint.

| Fixture | Categories and counts | Total |
| --- | --- | ---: |
| `background_jobs_browser_test.go` | modernize 3, perfsprint 3 | 6 |
| `background_transcripts_browser_test.go` | perfsprint 1, tagliatelle 4 | 5 |
| `cascade_browser_test.go` | errorlint 1, modernize 7, perfsprint 2, staticcheck 1, tagliatelle 1 | 12 |
| `job_output_paging_browser_test.go` | gocritic 1, modernize 1, revive 1 | 3 |
| `onboarding_browser_test.go` | copyloopvar 1, gocritic 2, staticcheck 1, tagliatelle 1, usestdlibvars 1 | 6 |
| `onboarding_oauth_browser_test.go` | gocritic 2, modernize 5, usestdlibvars 13 | 20 |
| `skill_composer_browser_test.go` | gocritic 1, modernize 7, perfsprint 1, tagliatelle 11 | 20 |

Totals: modernize 23, tagliatelle 17, usestdlibvars 14, perfsprint 7, gocritic 6, staticcheck 2, errorlint 1, revive 1, copyloopvar 1.

## Changes and preservation requirements

- Use HTTP status constants, literal errors, string builders/concatenation and current range, slices and strings idioms where equivalent.
- Wrap both errors in the mixed-provider diagnostic; retain its human-readable text.
- Simplify switches/conditions and remove redundant loop capture without changing the predicates, iteration order or exercised cases.
- Split `filepath.Join` components and bound each opened onboarding capture file to its own function lifetime.
- Ensure the paging producer closes its resources before its intentional successful process exit. Retain exit status 0 and byte-for-byte stdout: returning to the Go test runner would append PASS and corrupt the output oracle. Keep existing failure cleanup.
- Put context first in the paging journey helper and its caller.
- Preserve every JSON key exchanged with JavaScript and every evidence field. The root snake_case naming rule is wrong for these fixed lowerCamel browser wire contracts. Do not rename keys, drop tags, switch to untyped decoding, or change JavaScript to appease it. Only for confirmed wire fields, use narrowly scoped `nolint:tagliatelle` with a field-specific explanation, following the documented neighboring precedent in `background_transcripts_browser_test.go` (for example `entryRows`, `activationReads` and `renderedOutputRegions`). Verify the producer's exact spelling before adding each exception. Document these exceptions in the commit.

The relevant product rows are S23 Development and qualification, with S01 launch, S02 browser workspace and S16 model catalog as exercised surfaces. Their production behavior and recovery ownership do not change. Read `AGENTS.md`, `docs/product/README.md`, `docs/product/subsystems.md` and `docs/developing-evener/testing.md`; no UI changes require the design-system guide.

## Non-goals

No production/UI changes, new assertions, test matrix changes, weakened checks, timing changes, new labels or broad linter suppressions. Do not run full local suites or browser lanes. Unrelated findings belong in separate GitHub issues, not this PR. No product contract changes require evergreen documentation edits.

## Verification and delivery

1. Retain the exact tagged lint failure as the red check; review every transformation against the original fixture, including JSON producer keys, resource cleanup and stdout behavior.
2. Run spec PAR with two independent competing reviewers on the immutable spec and resolve evidenced findings before implementation.
3. Implement, then run implementation PAR with two independent competing reviewers on a frozen base/head diff, this spec and behavior evidence. Resolve legitimate findings.
4. Run simplify-code, preserving behavior. Commit, then run roborev branch review and settle findings under repository policy.
5. Run only the authorized targeted local verification (normal build dependency setup is permitted):

   ```sh
   golangci-lint run --build-tags browserguard ./cmd/evener-hub
   go test -tags browserguard ./cmd/evener-hub -run '^$' -count=1
   make build
   ```

   All three must exit zero. The Go command proves tagged compilation, not runtime browser behavior. CI owns full-suite/browser evidence.
6. Merge `origin/main` locally before final push if needed; rerun affected checks/reviews if the head changes. Push and open a PR against main that closes #4694. Shepherd current-head checks and read matching RoboRev review bodies and own per-commit findings. Admin squash merge only when required checks are green and findings are low or conclusively refuted. Verify GitHub's merged state and merge SHA.
7. Report PR URL, merge SHA, changed files, actual command results, every new suppression and its wire-contract justification, refuted findings and limits of verification. Preserve build deliverables; remove only owned disposable scratch.
