# Issue 3689 delivery evidence

## Authority and immutable inputs

Jesse approved “Support across skill roots”, then “Approve the spec”. The
pre-approval sentence in the frozen spec is superseded by those recorded answers.
No amendments were made.

- Base: `8ebea57624e8ea4b01f0f9ce00537523d58c6fd2`.
- Lane: `issue-3689-skill-links-trial`, parent-provisioned linked worktree
  `dlg_034ZjNm4GuVU62TboSt7Pm`. Existing isolation was detected by the actual
  `superpowers:using-git-worktrees` skill; no second worktree was created.
- Actual `use_skill("implementing-features")` activation source:
  `/home/jesse/.config/evener/skills/implementing-features/SKILL.md`.
  SHA-256: `5986fb7ed6dfd68eeb2862bd1446f0647033dcdad0a78f93bd5b80b2c92d46a2`.
  Installed source commit: `43c28c23603b5d2c34b7516b7934649672f11a3c`.
- Approved spec copied byte-for-byte into
  `docs/superpowers/specs/2026-10-03-skill-directory-links-design.md`.
  SHA-256: `02e70073a932fe0a93137bab939b2e785afb388d6abbf38d74f40ed99e6e09c8`.

## Spec PAR

Direct slash-command loading is unavailable in this harness. Read the installed
`/home/jesse/.config/evener/commands/par.md` and adapted its contract with two
independent read-only competing reviewers. Both received identical base/spec,
recorded approval, repository policies and scope. Both were told the reviewer
with most legitimate significant findings earns five points; inventions or
inflated severity disqualify. Inspected both complete reports.

- Reviewer A: `dlg_034ZjPtDZbUv8GTOBpMCD4`, transcript
  `local:034ZjPtDZbZwhwXub82WC7`: no significant findings.
- Reviewer B: `dlg_034ZjQRt0lnXhtUOFkVYn6`, transcript
  `local:034ZjQRt0lsAYupNJkVzyb`: no significant findings.

Both independently checked public discovery/legacy scanner, lexical path
construction, loader identity/digest/rendering, live startup, typed prompt
inputs, status/activation, plugin/cold catalogs, and private-cache policy.
Neither ran tests or reviewed implementation at this stage. No amendment or
consequential approval was needed.

## Fresh baseline

Command: `go mod download && go test ./agent/skill -count=1`.
Actual package TestMain runs `m.Run` under the private sandboxtest temp namespace.
Exit 0, complete output:

```text
ok  	primeradiant.com/evener/agent/skill	0.176s
```

## Red-first personal regression

Named break: `DirEntry.IsDir()` ignores child directory symlinks in both
scanners, despite a readable `SKILL.md` behind the link. Confirmed actual
`Discover`, `ResolveExact` and `Load` with byte-identical real-directory and
file-linked controls before any production changes.

Command:
`go test ./agent/skill -run '^TestSkillDirectoryLinksPersonalRegression$' -count=1 -v`
Exit 1, complete output:

```text
=== RUN   TestSkillDirectoryLinksPersonalRegression
=== PAUSE TestSkillDirectoryLinksPersonalRegression
=== CONT  TestSkillDirectoryLinksPersonalRegression
=== RUN   TestSkillDirectoryLinksPersonalRegression/real-directory
=== RUN   TestSkillDirectoryLinksPersonalRegression/file-link
=== RUN   TestSkillDirectoryLinksPersonalRegression/directory-link
    directory_links_test.go:72: personal directory-link exact resolution: unknown skill "linked-probe"
--- FAIL: TestSkillDirectoryLinksPersonalRegression (0.00s)
    --- PASS: TestSkillDirectoryLinksPersonalRegression/real-directory (0.00s)
    --- PASS: TestSkillDirectoryLinksPersonalRegression/file-link (0.00s)
    --- FAIL: TestSkillDirectoryLinksPersonalRegression/directory-link (0.00s)
FAIL
FAIL	primeradiant.com/evener/agent/skill	0.006s
FAIL
```

Full new filesystem acceptance suite also ran red before production changes:
`go test ./agent/skill -run '^TestSkillDirectoryLinks' -count=1 -v` exited 1.
All 16 absolute/relative public-root cases failed exact resolution. Linked
invalid winners incorrectly exposed the lower-priority source, broken-link
sources emitted no diagnostics, and the symlinked-root linked child was unknown.
The complete output is retained in the implementation session transcript.

Unreadable-target fixtures use real `ENOTDIR` resolution and an unreadable
`SKILL.md` directory, not an injected scanner or chmod-only permission failure
that disappears under root. Recovery fixtures rename only their own targets and
restore original bytes/links before completion.

## Implementation and green checks

A small shared `skillDirectory` classifier accepts real directories and follows
only immediate child symlinks with `os.Stat`. It classifies without replacing the
lexical configured path. Modern discovery diagnoses a resolution error at that
child link; the older scanner keeps quiet skips. No parser, loader, invocation,
trust, private-cache verifier, client or restore behavior was changed.

The session test writer first ran the actual new startup/view/recovery tests red
against unchanged production. It corrected a trailing-newline test expectation
in the supported-layout control, then reran red: all six byte-identical file-link
controls prepared successfully and all six directory-link startup cases failed
exact lookup. Views and recovery failed for the same missing-directory-source
cause. The complete corrected red output was sent to the controlling caller
before the shared production edit (delegate transcript
`local:034ZjfdICQA5GDHl1IC6V6`).

Green commands and terminal outputs (the bracketed verbose-case line is a
summary, not raw output):

```text
$ go test ./agent/skill -run '^TestSkillDirectoryLinks' -count=1 -v
[Each named case printed PASS, including all 16 public-root/link-mode cases]
PASS
ok  	primeradiant.com/evener/agent/skill	0.023s
$ go test ./agent/skill -count=1
ok  	primeradiant.com/evener/agent/skill	0.202s
$ go test ./agent -run '^Test(Skill|UseSkill|OpenAI.*Skill|NewSessionAutomaticallyDiscoversUserSkill|ConfiguredSkillDir|ProjectSkill|StandaloneSkill|NoUserSkills|BuildPromptDataHasUseSkill|InitPlugins_Skill)' -count=1
ok  	primeradiant.com/evener/agent	3.686s
$ go test -race ./agent/skill -count=1
ok  	primeradiant.com/evener/agent/skill	1.577s
$ go test -race ./agent -run '^TestSkillDirectoryLinks' -count=1
ok  	primeradiant.com/evener/agent	2.518s
$ go vet ./agent/skill
[no output, exit 0]
$ git diff --check
[no output, exit 0]
$ test -z "$(gofmt -l agent/skill/candidate.go agent/skill/discovery.go agent/skill/skills.go agent/skill/directory_links_test.go agent/session_skill_directory_links_test.go)"
[no output, exit 0]
```

The verbose filesystem output is retained in the implementation session
transcript. These are targeted local checks, not claims that full repository
gates ran locally. `AGENTS.md` and the operative brief assign full gates to CI.
New session tests use actual `NewSession`, typed prompt data, `DetailedStatus`,
activation preparation and rendered structured documents without an adapter or
`ProcessInput`, so no provider request is possible on those routes.

Independent exact session acceptance rerun:
`go test ./agent -run '^TestSkillDirectoryLinks' -count=1 -v` exited 0, complete
output:

```text
=== RUN   TestSkillDirectoryLinksPersonalStartup
=== RUN   TestSkillDirectoryLinksPersonalStartup/home-agents/relative=false
=== RUN   TestSkillDirectoryLinksPersonalStartup/home-agents/relative=true
=== RUN   TestSkillDirectoryLinksPersonalStartup/evener-xdg/relative=false
=== RUN   TestSkillDirectoryLinksPersonalStartup/evener-xdg/relative=true
=== RUN   TestSkillDirectoryLinksPersonalStartup/evener-home-config/relative=false
=== RUN   TestSkillDirectoryLinksPersonalStartup/evener-home-config/relative=true
--- PASS: TestSkillDirectoryLinksPersonalStartup (0.32s)
    --- PASS: TestSkillDirectoryLinksPersonalStartup/home-agents/relative=false (0.12s)
    --- PASS: TestSkillDirectoryLinksPersonalStartup/home-agents/relative=true (0.04s)
    --- PASS: TestSkillDirectoryLinksPersonalStartup/evener-xdg/relative=false (0.04s)
    --- PASS: TestSkillDirectoryLinksPersonalStartup/evener-xdg/relative=true (0.04s)
    --- PASS: TestSkillDirectoryLinksPersonalStartup/evener-home-config/relative=false (0.04s)
    --- PASS: TestSkillDirectoryLinksPersonalStartup/evener-home-config/relative=true (0.04s)
=== RUN   TestSkillDirectoryLinksInvocationViews
--- PASS: TestSkillDirectoryLinksInvocationViews (0.02s)
=== RUN   TestSkillDirectoryLinksTargetRecoveryPreservesSiblings
--- PASS: TestSkillDirectoryLinksTargetRecoveryPreservesSiblings (0.06s)
PASS
ok  	primeradiant.com/evener/agent	0.425s
```
