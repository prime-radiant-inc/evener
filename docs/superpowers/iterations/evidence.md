# Voice planning evidence

This record covers planning only. Nothing here claims that voice is implemented,
that a live provider passed, or that an iPhone passed a physical gate. Execution
status belongs in [roadmap.md](roadmap.md); this file records source coverage and
review/validation evidence.

## Authority and source

- Approved input: `docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md`,
  commit `f5e5516c19b6f1ae48bd45d2df68f34b1be12061`, read in full (lines 1–585).
- Jesse's subsequent approval and “build it” supersede the record's historical
  “proposed” label and last approval request. The approved spec is unchanged.
- The current baseline already includes manager support from `9f431d5f20`.
  The design's historical separate-manager-branch statement is not a new merge
  prerequisite. Voice does not introduce another agent or broader authority.
- The approved microphone/background, ambient-voice, proactive locked speech and
  paraphrase contracts need implementation and qualification, not reapproval.
- Older hubs/daemons are capability-gated out of voice while healthy text remains
  usable. No broad compatibility project or guessed RPC fallback is in scope.
- `docs/superpowers/iterations/` was absent before this work. Standard workflow
  paths are safe; no unrelated iteration state was overwritten.
- Root `AGENTS.md`, `docs/superpowers/README.md`, product ownership/compatibility
  guidance, and testing rules informed the plan. Human instructions prohibit
  commits, production changes, installs and remote operations in this planning unit.

## Extraction method

The approved architecture spec is outside the workflow's taxonomy directories.
Contract behavior defaults to integration proof, app controls to app-level proof,
and assembled voice journeys/physical measurements to e2e proof. Implementation
choices alone are not invented user stories. All requirements remain unimplemented.

The inspected `chunk_spec.py` produced 13 complete section payloads. Three leaf
helpers received exact staged chunks: 0–5 (lines 1–243), 6–7 (245–419), and 8–12
(421–585). Exact staging avoids prompt truncation; the helpers verify their end
ranges. The first helper's condensed brief was corrected with the complete staged
source before aggregation. Extraction outputs are temporary inputs, not a second
backlog. Paired omission review compares source against those outputs before
aggregation; paired scope review checks the resulting roadmap and story boundaries.

## Source coverage ledger

Every normative chunk has both acceptance criteria and observable scenarios. Source bibliography is non-normative. Broad source ranges are navigation aids, not a proof substitute; the paired review checked the actual text.

| Chunk | Source lines | Stories | Scenarios | Classification |
|---|---|---|---|---|
| (preamble) | 1–13 | STORY-0001, STORY-0002, STORY-0009, STORY-0010, STORY-0020, STORY-0028 | JOURNEY-0007, SCENARIO-0001, SCENARIO-0003, SCENARIO-0008 | covered |
| 1. Intent and scope | 15–59 | STORY-0001, STORY-0002, STORY-0007, STORY-0008, STORY-0010, STORY-0011, STORY-0012, STORY-0013, STORY-0020, STORY-0021, STORY-0028 | JOURNEY-0001, JOURNEY-0003, JOURNEY-0005, JOURNEY-0006, JOURNEY-0007, SCENARIO-0001, SCENARIO-0003, SCENARIO-0006, SCENARIO-0009, SCENARIO-0014, SCENARIO-0016 | covered |
| 2. Approaches and choice | 61–78 | STORY-0002, STORY-0028 | JOURNEY-0007, SCENARIO-0003, SCENARIO-0016 | covered |
| 3. What exists and what must change | 80–95 | STORY-0001, STORY-0002, STORY-0006, STORY-0007, STORY-0008, STORY-0009, STORY-0010, STORY-0020, STORY-0021, STORY-0028 | JOURNEY-0001, JOURNEY-0005, SCENARIO-0001, SCENARIO-0003, SCENARIO-0005, SCENARIO-0006, SCENARIO-0008, SCENARIO-0009, SCENARIO-0014, SCENARIO-0016 | covered |
| 4. Ownership and transport | 97–171 | STORY-0001, STORY-0002, STORY-0006, STORY-0010, STORY-0011, STORY-0012, STORY-0020, STORY-0021, STORY-0028 | JOURNEY-0002, JOURNEY-0006, JOURNEY-0007, SCENARIO-0001, SCENARIO-0002, SCENARIO-0003, SCENARIO-0004, SCENARIO-0005, SCENARIO-0011, SCENARIO-0012, SCENARIO-0013, SCENARIO-0015, SCENARIO-0016 | covered |
| 5. Listening and conversation states | 173–243 | STORY-0007, STORY-0008, STORY-0009, STORY-0010, STORY-0011, STORY-0012, STORY-0013, STORY-0021, STORY-0028 | JOURNEY-0003, JOURNEY-0004, JOURNEY-0005, JOURNEY-0006, JOURNEY-0007, SCENARIO-0006, SCENARIO-0007, SCENARIO-0008, SCENARIO-0009, SCENARIO-0010, SCENARIO-0011, SCENARIO-0013, SCENARIO-0014, SCENARIO-0015 | covered |
| 6. Turn admission and durable intent | 245–368 | STORY-0003, STORY-0004, STORY-0005, STORY-0014, STORY-0015, STORY-0016, STORY-0017, STORY-0022, STORY-0023, STORY-0034 | JOURNEY-0008, JOURNEY-0009, JOURNEY-0010, JOURNEY-0011, JOURNEY-0012, JOURNEY-0013, SCENARIO-0017, SCENARIO-0018, SCENARIO-0019, SCENARIO-0020, SCENARIO-0021, SCENARIO-0022, SCENARIO-0023, SCENARIO-0024, SCENARIO-0025, SCENARIO-0026, SCENARIO-0027, SCENARIO-0028, SCENARIO-0029, SCENARIO-0030, SCENARIO-0057, SCENARIO-0058 | covered |
| 7. Barge-in, correction and cancellation | 370–419 | STORY-0018, STORY-0019, STORY-0020 | JOURNEY-0008, JOURNEY-0013, JOURNEY-0014, SCENARIO-0025, SCENARIO-0028, SCENARIO-0031, SCENARIO-0032, SCENARIO-0033 | covered |
| 8. Recovery, privacy and authorization | 421–453 | STORY-0024, STORY-0025, STORY-0026, STORY-0036 | SCENARIO-0034, SCENARIO-0035, SCENARIO-0036, SCENARIO-0037, SCENARIO-0038, SCENARIO-0039, SCENARIO-0040, SCENARIO-0041, SCENARIO-0051, SCENARIO-0052 | covered |
| 9. Runtime options and cost | 455–482 | STORY-0025, STORY-0027, STORY-0029 | JOURNEY-0015, SCENARIO-0038, SCENARIO-0042, SCENARIO-0043, SCENARIO-0049 | covered |
| 10. Qualification and delivery gates | 484–536 | STORY-0029, STORY-0030, STORY-0031, STORY-0032, STORY-0033, STORY-0034, STORY-0035, STORY-0036, STORY-0037, STORY-0038, STORY-0039 | JOURNEY-0015, JOURNEY-0016, JOURNEY-0017, JOURNEY-0018, JOURNEY-0019, JOURNEY-0020, JOURNEY-0021, JOURNEY-0022, JOURNEY-0023, JOURNEY-0024, SCENARIO-0044, SCENARIO-0045, SCENARIO-0046, SCENARIO-0047, SCENARIO-0048, SCENARIO-0050, SCENARIO-0051, SCENARIO-0053, SCENARIO-0054, SCENARIO-0055, SCENARIO-0056 | covered |
| 11. Remaining decisions and evidence | 538–559 | STORY-0029, STORY-0030, STORY-0031, STORY-0032, STORY-0033, STORY-0034, STORY-0039 | JOURNEY-0015, JOURNEY-0017, JOURNEY-0019, JOURNEY-0020, JOURNEY-0024, SCENARIO-0048, SCENARIO-0049, SCENARIO-0055 | covered |
| Sources | 561–585 | — | — | non-normative, bibliography only |

## Omission review and dependency consolidation

Two independent read-only reviewers compared all 13 source chunks against all
three extraction outputs. Both found the same Serious omission and no other
Critical/Serious omissions: a late output could be stamped with a newer global
consumption state despite originating before the correction. The requirement now
freezes exact originating model-request provenance, and a dedicated delayed-output
scenario falsifies that error. A separate restored-question scenario covers full
batch identity and identical provenance after restore. No physical gate was
replaced with simulation.

The 33 initial story cards / 151 ACs were consolidated into five epic themes.
Dependency splits happened before the first stable IDs: Talk versus local wake,
committed presentation versus pending questions and full live qualification,
intake versus recovery UI, speech controls versus named-job correction, first turn
versus persistent disconnect journey, and lock/acoustic qualification versus full
physical transport recovery. The initial first-scenario-only backlink output was
replaced by each story's focused proof-seam-qualified scenario set. Backlinks are
navigation; semantic coverage comes from scenario observables and source review.

The approved source remains unchanged. The Realtime completed-response refinement
and runtime/native file maps were supplied by the parent's inspected protocol and
source scouts. In particular, the final request history after skill rebuilding is
the consumption seam, accepted/incorporated input is not consumed input, and the
existing live pending-question helper is not durable batch identity. Package/type
names for a new frontier encoding remain builder choices; bounded lineage must not
become a quadratic copy of full history on every output.

Current artifacts: **39 stories, 154 ACs, 82 scenarios (24 journeys and 58 surface/contract/recovery cards), five epics**. None is a passing behavior result.

## Scope review

The first independent scope pair returned REVISE / APPROVE. One reviewer found a
Serious reversed expectation in SCENARIO-0027: greater sequence/newer run was said
to make unconsumed input eligible. It now explicitly **must not**. The roadmap also
names the complete native module map, replaces the approved iOS no-microphone
contract while preserving camera/Android restrictions, and keeps typed-admission
speech retirement in the first slice. The corpus was aligned with the roadmap's
two sentinels rather than labeling every journey a sentinel.

A fresh independent pair reviewed the current artifacts, including that corpus
correction. Both returned **APPROVE**, with no Serious/Critical findings, across
citation integrity, scope creep, downstream constraints, scenario coverage and
story splitting. Reports: `local:034rxIKNSDfpdGluwR6KHs` and
`local:034rxIyBwglsYqf4jcfljb`. This approves the implementation scope only.

## Validation observed on 2026-11-04

All four scripts were read before execution. Run from the worktree root:

```bash
skills=/home/jesse/.config/evener/plugins/cache/prime-radiant-marketplace/iterative-development/c05889aeb28f1f2c93f88232236e6ed906d32a6f/skills
python3 "$skills/extracting-requirements/scripts/validate_requirements_index.py" docs/superpowers/iterations/requirements/
python3 "$skills/extracting-requirements/scripts/validate_scenarios.py" docs/superpowers/iterations/behavior-scenarios.md docs/superpowers/iterations/requirements/
python3 "$skills/scoping-the-simplest-core/scripts/check_citations.py" docs/superpowers/iterations/roadmap.md docs/superpowers/iterations/requirements/
python3 "$skills/scoping-the-simplest-core/scripts/validate_roadmap.py" docs/superpowers/iterations/roadmap.md
```

Observed output, exit 0:

```text
OK: docs/superpowers/iterations/requirements
OK: scenarios valid
OK: all 39 cited stories exist in requirements
OK: docs/superpowers/iterations/roadmap.md
```

These scripts validate required headings, story references, unique scenario IDs
and roadmap format. They do not prove semantic coverage or product behavior.
An independent inline Python inventory also checked unique story IDs, exactly one
delivery assignment per story, resolving references on all 146 observable ACs,
one corpus row per scenario, and exactly two sentinels. Its output was:

```text
5 epics; 39 unique stories; 154 ACs (146 observable); 82 scenarios (24 journeys, 58 other cards)
All stories assigned exactly once; all observable ACs have resolving scenario refs; corpus covers all scenarios exactly once; 2 sentinels
```

`git diff --exit-code HEAD -- docs/superpowers/specs/2026-11-04-voice-chief-of-staff-design.md`
exited 0 with no output. The approved source is unchanged. At the planning handoff,
all nine deliverable files were untracked and uncommitted. The parent subsequently
authorized one local commit of these nine reviewed files with normal hooks and no
push. All production stories remain pending. No production code, dependency
installation, remote operation, live inference or physical test was performed by
this unit.
