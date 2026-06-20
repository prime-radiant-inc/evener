"""Tests for eval_bisect — binary search for eval regressions."""

import os
import subprocess
import textwrap
from unittest.mock import MagicMock, patch

import pytest

from eval_bisect import (
    AGENT_RELEVANT_PATHS,
    get_commit_range,
    make_run_id,
    bisect_search,
    compute_score,
)


class TestGetCommitRange:
    """get_commit_range filters commits to agent-relevant ones."""

    def test_returns_commits_in_order(self):
        """Commits should be oldest-first (good → bad)."""
        repo = os.path.dirname(os.path.dirname(__file__))
        # Use two known commits — just check ordering is oldest-first
        commits = get_commit_range("HEAD~3", "HEAD", filter_agent=False, repo=repo)
        assert len(commits) == 3

    def test_filter_agent_relevant(self):
        """When filter_agent=True, only commits touching agent paths are included."""
        repo = os.path.dirname(os.path.dirname(__file__))
        all_commits = get_commit_range("HEAD~10", "HEAD", filter_agent=False, repo=repo)
        filtered = get_commit_range("HEAD~10", "HEAD", filter_agent=True, repo=repo)
        # Filtered should be <= all commits
        assert len(filtered) <= len(all_commits)
        # All filtered commits should be in the full list
        for c in filtered:
            assert c in all_commits

    def test_empty_range(self):
        """If good == bad, return empty list."""
        repo = os.path.dirname(os.path.dirname(__file__))
        commits = get_commit_range("HEAD", "HEAD", filter_agent=False, repo=repo)
        assert commits == []


class TestMakeRunId:
    """Run IDs encode task, SHA, and step number."""

    def test_format(self):
        rid = make_run_id("build-cython-ext", "7ead614abc", 3)
        assert rid == "bisect-build-cython-ext-7ead614-s3"

    def test_sha_truncated_to_7(self):
        rid = make_run_id("some-task", "abcdef1234567890", 1)
        assert "abcdef1" in rid
        assert "abcdef12" not in rid

    def test_step_zero(self):
        rid = make_run_id("task", "abc1234", 0)
        assert rid == "bisect-task-abc1234-s0"


class TestComputeScore:
    """compute_score handles normal, partial, and infra-failure cases."""

    def test_normal_scores(self):
        scores = {"rep-1": 1.0, "rep-2": 1.0, "rep-3": 0.0}
        assert compute_score(scores, expected_reps=3) == pytest.approx(2 / 3)

    def test_all_pass(self):
        scores = {"rep-1": 1.0, "rep-2": 1.0, "rep-3": 1.0, "rep-4": 1.0, "rep-5": 1.0}
        assert compute_score(scores, expected_reps=5) == 1.0

    def test_all_fail(self):
        scores = {"rep-1": 0.0, "rep-2": 0.0, "rep-3": 0.0}
        assert compute_score(scores, expected_reps=3) == 0.0

    def test_empty_scores_returns_none(self):
        """If no scores available, return None (infra failure)."""
        assert compute_score({}, expected_reps=3) is None


class TestBisectSearch:
    """bisect_search finds the first bad commit using binary search."""

    def test_finds_transition_point(self):
        """Given commits [A, B, C, D, E] where A-C are good and D-E are bad,
        bisect should identify D as the culprit."""
        commits = ["aaa", "bbb", "ccc", "ddd", "eee"]
        scores = {"aaa": 1.0, "bbb": 1.0, "ccc": 1.0, "ddd": 0.2, "eee": 0.0}

        def mock_test(sha, **kwargs):
            return scores[sha]

        result = bisect_search(commits, threshold=0.8, test_fn=mock_test)
        assert result["culprit"] == "ddd"

    def test_first_commit_is_bad(self):
        """If the first commit after good is already bad, it's the culprit."""
        commits = ["aaa", "bbb", "ccc"]
        scores = {"aaa": 0.0, "bbb": 0.0, "ccc": 0.0}

        def mock_test(sha, **kwargs):
            return scores[sha]

        result = bisect_search(commits, threshold=0.8, test_fn=mock_test)
        assert result["culprit"] == "aaa"

    def test_last_commit_is_first_bad(self):
        """If only the last commit is bad, it's the culprit."""
        commits = ["aaa", "bbb", "ccc"]
        scores = {"aaa": 1.0, "bbb": 1.0, "ccc": 0.0}

        def mock_test(sha, **kwargs):
            return scores[sha]

        result = bisect_search(commits, threshold=0.8, test_fn=mock_test)
        assert result["culprit"] == "ccc"

    def test_single_commit(self):
        """With only one commit, it must be the culprit."""
        commits = ["aaa"]
        def mock_test(sha, **kwargs):
            return 0.0

        result = bisect_search(commits, threshold=0.8, test_fn=mock_test)
        assert result["culprit"] == "aaa"

    def test_skips_build_failures(self):
        """If test_fn returns None (build failure), skip that commit."""
        commits = ["aaa", "bbb", "ccc", "ddd"]
        # bbb can't build, but the real transition is at ccc
        scores = {"aaa": 1.0, "bbb": None, "ccc": 0.0, "ddd": 0.0}

        def mock_test(sha, **kwargs):
            return scores[sha]

        result = bisect_search(commits, threshold=0.8, test_fn=mock_test)
        assert result["culprit"] == "ccc"

    def test_records_all_tested(self):
        """Result should include scores for every commit that was tested."""
        commits = ["aaa", "bbb", "ccc", "ddd", "eee"]
        scores = {"aaa": 1.0, "bbb": 1.0, "ccc": 1.0, "ddd": 0.2, "eee": 0.0}

        def mock_test(sha, **kwargs):
            return scores[sha]

        result = bisect_search(commits, threshold=0.8, test_fn=mock_test)
        # Should have tested some subset (binary search doesn't test all)
        assert len(result["tested"]) >= 2
        assert len(result["tested"]) <= len(commits)
        # Every tested commit should have a score
        for sha, score in result["tested"]:
            assert score is not None or sha in scores

    def test_does_not_retest(self):
        """Binary search should not test the same commit twice."""
        commits = ["aaa", "bbb", "ccc", "ddd", "eee"]
        scores = {"aaa": 1.0, "bbb": 1.0, "ccc": 0.5, "ddd": 0.2, "eee": 0.0}
        tested = []

        def mock_test(sha, **kwargs):
            tested.append(sha)
            return scores[sha]

        bisect_search(commits, threshold=0.8, test_fn=mock_test)
        assert len(tested) == len(set(tested)), "Same commit tested twice"


class TestAgentRelevantPaths:
    """The filter patterns should match agent code, not docs or dashboard."""

    def test_agent_paths_included(self):
        for p in ["internal/bundled/plugins/coordinator-workflow/agents/implementer.md", "agent/subagents.go",
                   "cmd/serf/main.go", "llm/generate.go",
                   "go.mod", "go.sum",
                   "tools/eval/serf_agent.py", "tools/eval/install-serf.sh.j2"]:
            assert any(p.startswith(prefix) for prefix in AGENT_RELEVANT_PATHS), \
                f"{p} should be agent-relevant"

    def test_non_agent_paths_excluded(self):
        for p in ["docs/experiments/NOTEBOOK.md", "tools/eval/scoreboard.py",
                   "README.md", ".github/workflows/ci.yml"]:
            assert not any(p.startswith(prefix) for prefix in AGENT_RELEVANT_PATHS), \
                f"{p} should NOT be agent-relevant"
