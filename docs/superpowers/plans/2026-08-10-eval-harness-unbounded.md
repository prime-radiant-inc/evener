# Eval Harness Unbounded Rounds Implementation Plan

> **For Codex:** Use the executing-plans workflow to implement each task in order and verify every checkpoint.

**Goal:** Remove the Harbor 100-round ceiling, make the bundled explorer inherit the selected model, and rerun only the five Terminal-Bench 2.1 tasks whose failures could plausibly change.

**Architecture:** Keep both changes at their configuration sources. Evener's built-in agent frontmatter controls explorer inheritance; Harbor's adapter passes Evener's existing `0` unlimited-round value. Harbor retains ownership of the outer task timeout.

**Tech Stack:** Go, embedded Markdown agent definitions, Python, pytest, Harbor, Terminal-Bench 2.1.

---

### Task 1: Make explorer inherit the selected model

**Files:**
- Modify: `agent/subagent_model_selection_test.go`
- Modify: `agent/builtin_agents_test.go`
- Modify: `internal/bundled/agents/explorer.md`

1. Add a behavior test that loads the bundled explorer and verifies parent-model inheritance and explicit-model selection.
2. Run the focused test and confirm it fails because explorer is pinned.
3. Remove the explorer `model` field and update the loader assertion.
4. Run the focused tests and confirm they pass.
5. Commit the Evener change.

### Task 2: Make Harbor Evener rounds unlimited

**Files:**
- Modify: `tests/test_serf_agent.py`
- Modify: `src/harbor_runner/serf_agent.py`
- Modify: `src/harbor_runner/cli.py`

1. Update the adapter test to construct the default adapter and assert parsed argv contains `--max-rounds 0`.
2. Run the focused test and confirm it fails with the old default.
3. Change both adapter and CLI defaults from 100 to 0.
4. Run the focused and full Harbor test suites.
5. Commit the Harbor change.

### Task 3: Verify and deploy

1. Run Evener's focused tests and repository gate.
2. Run Harbor's full test suite.
3. Build the exact Linux amd64 Evener binary and record its checksum and commit.
4. Deploy the tested binary and runner configuration to Magic Kingdom.

### Task 4: Run the targeted eval cohort

1. Launch one Luna-max attempt for each of `train-fasttext`, `extract-moves-from-video`, `build-pov-ray`, `install-windows-3.11`, and `dna-insert`.
2. Preserve all trajectories and logs; do not submit to Terminal-Bench.
3. Monitor each task through grading or infrastructure failure.
4. Compare results to the original failures and report the causal evidence.
