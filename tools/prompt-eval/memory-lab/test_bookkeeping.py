#!/usr/bin/env python3
"""Behavior tests for the bookkeeping tool's call classifier.

Run: python3 -B tools/prompt-eval/memory-lab/test_bookkeeping.py
Each case is a tool call as the lab records it and the (kind, surfaces) classify must return; the
compaction tests cover how a compacted session's calls are tagged and its reconstruction counted, and
the --context-window tests cover the trial's providers.toml."""
import argparse, importlib.machinery, importlib.util, json, os, stat, sys, tempfile, unittest
from unittest import mock

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
_loader = importlib.machinery.SourceFileLoader("bookkeeping", os.path.join(HERE, "bookkeeping"))
_spec = importlib.util.spec_from_loader("bookkeeping", _loader)
bookkeeping = importlib.util.module_from_spec(_spec)
_loader.exec_module(bookkeeping)


def call(tool, **args):
    return {"tool": tool, "args": json.dumps(args)}


def shell(command):
    return call("shell", command=command)


def patch(*headers):
    return call("apply_patch", patch="*** Begin Patch\n" + "\n@@\n+x\n".join(headers) + "\n*** End Patch")


LEDGER = ".superpowers/sdd/p/progress.md"
BRIEF = ".superpowers/sdd/p/brief.md"
PLAN = "/w/docs/superpowers/plans/2026-10-06-shop-helpers.md"
CASES = [
    # File tools and patches: classified by the files they change.
    (call("edit_file", file_path=LEDGER), ("write", ["ledger"])),
    (call("write_file", file_path=BRIEF), ("write", ["plan-artifacts"])),
    (call("read_file", file_path=LEDGER), ("read", ["ledger"])),
    (patch(f"*** Update File: /w/{LEDGER}"), ("write", ["ledger"])),
    (patch(f"*** Update File: /w/{LEDGER}", f"*** Add File: /w/{BRIEF}"), ("write", ["ledger", "plan-artifacts"])),
    (call("apply_patch", patch=f"*** Begin Patch\n*** Update File: /w/cart.go\n@@\n+// see {LEDGER}\n*** End Patch"),
     ("work", [])),
    # Shell writes: redirects (including fd-prefixed, &> and >&), tee and in-place edits.
    (shell(f"printf 'Task 1\\n' >> {LEDGER}"), ("write", ["ledger"])),
    (shell("echo hi>progress.md"), ("write", ["ledger"])),
    (shell("go test ./... 2>>progress.md"), ("write", ["ledger"])),
    (shell("go test ./... &>progress.md"), ("write", ["ledger"])),
    (shell("echo x >& progress.md"), ("write", ["ledger"])),
    (shell(f"cat <<'EOF' | tee -a {LEDGER}\nTask 2\nEOF"), ("write", ["ledger"])),
    (shell(f"tee -a {LEDGER} {BRIEF} < note"), ("write", ["ledger", "plan-artifacts"])),
    (shell(f"sed -i '' 's/a/b/' {LEDGER}"), ("write", ["ledger"])),
    (shell("sed -Ei 's/a/b/' progress.md"), ("write", ["ledger"])),
    (shell("sed --in-place 's/a/b/' progress.md"), ("write", ["ledger"])),
    (shell("perl -pi -e 's/a/b/' progress.md"), ("write", ["ledger"])),
    # Wrappers, including ones that take an argument.
    (shell("sudo tee -a progress.md < note"), ("write", ["ledger"])),
    (shell("sudo -u me sed -i 's/a/b/' progress.md"), ("write", ["ledger"])),
    (shell("timeout 5 tee progress.md < note"), ("write", ["ledger"])),
    (shell("nice -n 10 cat progress.md"), ("read", ["ledger"])),
    (shell("env LANG=C cat progress.md"), ("read", ["ledger"])),
    # Shell reads: a reader whose file arguments name the file.
    (shell(f"cat {LEDGER}"), ("read", ["ledger"])),
    (shell(f"cat {LEDGER} 2>&1 | tail -5"), ("read", ["ledger"])),
    (shell(f"cat {BRIEF}"), ("read", ["plan-artifacts"])),
    (shell(f"sed -i '' 's/x/y/' cart.go\nwc -l {LEDGER}"), ("read", ["ledger"])),
    (shell("grep -n Task progress.md"), ("read", ["ledger"])),
    (shell("grep -E 'a|b' progress.md"), ("read", ["ledger"])),
    # Names that are patterns, other files, or not touched at all.
    (shell("rg --files | grep progress.md"), ("work", [])),
    (shell("grep -e progress.md notes.txt"), ("work", [])),
    (shell("echo progress.md"), ("work", [])),
    (shell("echo x > progress.md.bak"), ("work", [])),
    (shell("cat docs/oldprogress.md"), ("work", [])),
    (shell("cp x progress.md~"), ("work", [])),
    (shell("perl -MList::Util -e 'print 1' progress.md"), ("work", [])),
    # Attached option values and other forms that only look like file operands.
    (shell("grep -eTask progress.md"), ("read", ["ledger"])),
    (shell("rg --regexp=Task progress.md"), ("read", ["ledger"])),
    (shell("grep --file=pats.txt progress.md"), ("read", ["ledger"])),
    (shell("grep -fpats.txt progress.md"), ("read", ["ledger"])),
    (shell("cat <<< progress.md"), ("work", [])),
    (shell("sed -i -f .superpowers/script.sed cart.go"), ("read", ["plan-artifacts"])),
    (shell("sed -i -e 's/a/b/' progress.md"), ("write", ["ledger"])),
    (shell("sed -ne '1,5p' progress.md"), ("read", ["ledger"])),
    (shell("sed --quiet '1p' progress.md"), ("read", ["ledger"])),
    # Short-option clusters, perl script files, and the end-of-options marker.
    (shell("grep -Ef progress.md notes.txt"), ("read", ["ledger"])),
    (shell("grep -neTask progress.md"), ("read", ["ledger"])),
    (shell("sed -nf .superpowers/script.sed cart.go"), ("read", ["plan-artifacts"])),
    (shell("perl -i -pe 's/a/b/' progress.md"), ("write", ["ledger"])),
    (shell("perl -i -p .superpowers/fix.pl cart.go"), ("read", ["plan-artifacts"])),
    (shell("grep -eTask -- -fprogress.md"), ("work", [])),
    (shell("sed -es/n/x/ progress.md"), ("work", [])),
    (shell("sed -e 's/a/b/' -- -n"), ("work", [])),
    (shell("perl -i -x script.pl progress.md"), ("write", ["ledger"])),
    (shell("perl -i -x/opt script.pl progress.md"), ("write", ["ledger"])),
    (shell("go test ./..."), ("work", [])),
    # task_list: status, notes, or both; an update counts by the keys it sets.
    (call("task_list", update=[{"id": 1, "status": "done", "notes": "commit abc"}]), ("write", ["tasks", "task-notes"])),
    (call("task_list", update=[{"id": 1, "status": "done"}]), ("write", ["tasks"])),
    (call("task_list", update=[{"id": 1, "notes": "flaky test seen"}]), ("write", ["task-notes"])),
    (call("task_list", update=[{"id": 2, "depends_on": []}]), ("write", ["tasks"])),
    (call("task_list"), ("read", ["tasks"])),
    # Memory and the whiteboard.
    (call("memory_edit", scope="project", file_path="MEMORY.md"), ("write", ["mem:project"])),
    (call("notes_agent_set", note="x"), ("write", ["whiteboard"])),
    # Read-backs, labeled by the surface they read.
    (call("memory_read", scope="project", file_path="MEMORY.md"), ("read", ["memory"])),
    (call("memory_search", scope="personal", pattern="x"), ("read", ["memory"])),
    (call("notes_read"), ("read", ["whiteboard"])),
    (call("read_transcript", transcript_ref="artifact:x"), ("read", ["transcript"])),
    (call("find_session_transcripts", query="final review"), ("read", ["transcript"])),
    (call("job_status", target="dlg_1"), ("read", ["jobs"])),
    (call("job_list", type=["delegate"]), ("read", ["jobs"])),
    (call("read_file", file_path=f"/w/{BRIEF}"), ("read", ["plan-artifacts"])),
    (call("read_file", file_path=PLAN), ("read", ["plan"])),
    (shell(f"sed -n '1,40p' {PLAN}"), ("read", ["plan"])),
    (shell(f"cat {LEDGER} {PLAN} {PLAN}"), ("read", ["ledger", "plan"])),
    (shell(f"head {BRIEF} {PLAN}"), ("read", ["plan-artifacts", "plan"])),
    (call("read_file", file_path="/w/docs/plans.md"), ("work", [])),
    (call("edit_file", file_path=PLAN), ("work", [])),
]


class ClassifyTest(unittest.TestCase):
    def test_cases(self):
        for tool_call, want in CASES:
            with self.subTest(tool=tool_call["tool"], args=tool_call["args"][:80]):
                self.assertEqual(bookkeeping.classify(tool_call), want)


class ContextWindowConfigTest(unittest.TestCase):
    """memory-lab run --context-window: the trial's providers.toml is the user's plus one override row."""

    def user_file(self, user_toml):
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        path = os.path.join(root.name, "providers.toml")
        if user_toml is not None:
            with open(path, "w") as f:
                f.write(user_toml)
        self.enterContext(mock.patch.dict(os.environ, {"EVENER_PROVIDERS_CONFIG": path}))
        return path

    def config(self, user_toml, model="lunar/flash-bg.1"):
        import tomllib
        self.user_file(user_toml)
        return tomllib.loads(bookkeeping.lab.context_window_config(model, 44000))

    def test_adds_a_row_after_the_users_own_config(self):
        got = self.config('default = "lunar"\n[providers]\n  [providers.lunar]\n    base = "openai"\n')
        self.assertEqual(got["default"], "lunar")
        self.assertEqual(got["providers"]["lunar"]["base"], "openai")
        self.assertEqual(got["providers"]["lunar"]["models"]["flash-bg.1"], {"context_window": 44000})

    def test_overrides_an_existing_models_row_in_place(self):
        got = self.config('[providers.lunar.models."flash-bg.1"]\ncontext_window = 9\nmax_output_tokens = 7\n')
        self.assertEqual(got["providers"]["lunar"]["models"]["flash-bg.1"],
                         {"context_window": 44000, "max_output_tokens": 7})

    def test_extends_inline_tables(self):
        got = self.config('[providers.lunar]\nbase = "openai"\nmodels = {}\nheaders = { "X-A" = "b" }\n')
        self.assertEqual(got["providers"]["lunar"]["models"], {"flash-bg.1": {"context_window": 44000}})
        self.assertEqual(got["providers"]["lunar"]["headers"], {"X-A": "b"})

    def test_keeps_every_value_type(self):
        user = ('when = 2026-10-08T01:02:03Z\nratio = 0.5\non = true\nn = -3\nlist = [1, "two", { a = 1 }]\n'
                's = "quote \\" tab \\t uni é"\n[providers.lunar]\nbase = "openai"\n')
        import tomllib
        want = tomllib.loads(user)
        got = self.config(user)
        del got["providers"]["lunar"]["models"]
        self.assertEqual(got, want)

    def test_canonicalizes_the_model_ref_like_the_cli(self):
        got = self.config('[providers.openai]\nbase = "openai"\n', model=" OPENAI / gpt-5.5 ")
        self.assertEqual(got["providers"]["openai"]["models"], {"gpt-5.5": {"context_window": 44000}})

    def test_a_missing_user_file_means_no_user_layer(self):
        got = self.config(None)
        self.assertEqual(got, {"providers": {"lunar": {"models": {"flash-bg.1": {"context_window": 44000}}}}})

    def test_an_unreadable_user_file_exits_with_its_path(self):
        path = self.user_file(None)
        os.mkdir(path)  # a directory: open fails with an error other than "missing"
        with self.assertRaises(SystemExit) as e:
            bookkeeping.lab.context_window_config("lunar/flash-bg.1", 44000)
        self.assertIn(path, str(e.exception.code))

    def test_a_malformed_user_file_exits_with_its_path(self):
        path = self.user_file("[providers\n")
        with self.assertRaises(SystemExit) as e:
            bookkeeping.lab.context_window_config("lunar/flash-bg.1", 44000)
        self.assertIn(path, str(e.exception.code))


class TrialStateTest(unittest.TestCase):
    """make_state_dir writes the trial's providers.toml, and fixture_env points every session at it."""

    def setUp(self):
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        self.root = os.path.realpath(root.name)
        os.makedirs(os.path.join(self.root, "config"))
        # The launcher's cwd: relative config paths resolve against it, never against a trial workspace.
        cwd = os.getcwd()
        os.chdir(self.root)
        self.addCleanup(os.chdir, cwd)

    def environ(self, **values):
        """Patches os.environ for this test: each name set to its value, or unset when the value is None.
        patch.dict restores the whole environment, removals included, at cleanup."""
        self.enterContext(mock.patch.dict(os.environ, {k: v for k, v in values.items() if v is not None}))
        for k, v in values.items():
            if v is None:
                os.environ.pop(k, None)

    def run_trial(self, work_root, providers="config/providers.toml", credentials=None):
        self.environ(EVENER_PROVIDERS_CONFIG=providers, EVENER_CREDENTIALS_CONFIG=credentials)
        trial = os.path.join(self.root, "trial")
        os.makedirs(trial)
        work = os.path.join(self.root, "shop-abc")
        args = argparse.Namespace(work_root=os.path.join(self.root, "wr") if work_root else None,
                                  providers_config="[x]\ny = 1\n")
        bookkeeping.lab.make_state_dir(args, trial, work)
        xdg = os.path.join(trial, "xdg")
        return xdg, bookkeeping.lab.fixture_env(work, xdg)

    def check(self, work_root):
        xdg, env = self.run_trial(work_root, providers=os.path.join(self.root, "config", "providers.toml"))
        real = os.path.join(os.path.realpath(xdg), "providers.toml")
        if work_root:
            self.assertTrue(os.path.islink(xdg))
            self.assertEqual(real, os.path.join(self.root, "wr", ".state", "shop-abc", "providers.toml"))
        self.assertEqual(env["EVENER_PROVIDERS_CONFIG"], real)
        with open(real) as f:
            self.assertEqual(f.read(), "[x]\ny = 1\n")
        self.assertEqual(stat.S_IMODE(os.stat(real).st_mode), 0o600)
        self.assertEqual(env["EVENER_CREDENTIALS_CONFIG"], os.path.join(self.root, "config", "credentials.toml"))

    def test_under_work_root(self):
        self.check(work_root=True)

    def test_inside_the_trial(self):
        self.check(work_root=False)

    def test_relative_config_paths_resolve_against_the_launch_dir(self):
        _, env = self.run_trial(work_root=True, providers="config/providers.toml")
        self.assertEqual(env["EVENER_CREDENTIALS_CONFIG"], os.path.join(self.root, "config", "credentials.toml"))

    def test_a_relative_credentials_path_resolves_against_the_launch_dir(self):
        _, env = self.run_trial(work_root=True, credentials="secrets/credentials.toml")
        self.assertEqual(env["EVENER_CREDENTIALS_CONFIG"], os.path.join(self.root, "secrets", "credentials.toml"))

    def test_a_whitespace_providers_config_means_no_user_layer(self):
        # cmdutil.ProvidersConfigPath: present and blank after TrimSpace is no user layer, and credentials
        # then come from <config-root>/credentials.toml.
        self.environ(EVENER_PROVIDERS_CONFIG="  ", EVENER_CREDENTIALS_CONFIG=None, XDG_CONFIG_HOME=self.root)
        self.assertIsNone(bookkeeping.lab.user_providers_config())
        self.assertEqual(bookkeeping.lab.user_credentials_config(),
                         os.path.join(self.root, "evener", "credentials.toml"))

    def test_no_override_without_context_window(self):
        trial = os.path.join(self.root, "trial")
        os.makedirs(trial)
        bookkeeping.lab.make_state_dir(argparse.Namespace(work_root=None, providers_config=None), trial,
                                       os.path.join(self.root, "w"))
        env = bookkeeping.lab.fixture_env(os.path.join(self.root, "w"), os.path.join(trial, "xdg"))
        self.assertEqual(env.get("EVENER_PROVIDERS_CONFIG"), os.environ.get("EVENER_PROVIDERS_CONFIG"))


def event(kind, session="root", **data):
    return json.dumps({"kind": kind, "session_id": session, "data": data})


def events_file(test, lines):
    with tempfile.NamedTemporaryFile("w", suffix=".ndjson", delete=False) as f:
        f.write("\n".join(lines) + "\n")
    test.addCleanup(os.remove, f.name)
    return f.name


class CompactionTest(unittest.TestCase):
    def test_parse_events_tags_calls_with_compactions_and_output(self):
        lines = [event("SESSION_START", context_window_size=40000),
                 event("TOOL_CALL_START", tool_name="read_file", call_id="a", arguments_json="{}"),
                 event("TOOL_CALL_END", tool_name="read_file", call_id="a", output="x" * 400),
                 # One fold: a checkpoint followed at once by summarize is one compaction.
                 event("CONTEXT_COMPACTION", layer="observation_mask"),
                 event("CONTEXT_COMPACTION", layer="checkpoint"),
                 event("CONTEXT_COMPACTION", layer="summarize"),
                 event("TOOL_CALL_START", tool_name="notes_read", call_id="b", arguments_json="{}"),
                 # Masking alone drops no history: not a compaction.
                 event("CONTEXT_COMPACTION", layer="observation_mask"),
                 event("TOOL_CALL_START", tool_name="task_list", call_id="c", arguments_json="{}"),
                 event("CONTEXT_COMPACTION", layer="checkpoint")]
        with tempfile.NamedTemporaryFile("w", suffix=".ndjson", delete=False) as f:
            f.write("\n".join(lines) + "\n")
        self.addCleanup(os.remove, f.name)
        calls, _, _, _ = bookkeeping.lab.parse_events(f.name)
        self.assertEqual([(c["compactions_before"], c["output_chars"]) for c in calls], [(0, 400), (1, 0), (1, 0)])
        self.assertEqual(bookkeeping.lab.context_record(f.name),
                         (40000, [["observation_mask", "checkpoint", "summarize"], ["checkpoint"]]))

    def test_output_chars_counts_an_empty_output_as_empty(self):
        lines = [event("TOOL_CALL_START", tool_name="shell", call_id="a", arguments_json="{}"),
                 event("TOOL_CALL_END", tool_name="shell", call_id="a", output="", error="x" * 10),
                 event("TOOL_CALL_START", tool_name="memory_read", call_id="b", arguments_json="{}"),
                 event("TOOL_CALL_END", tool_name="memory_read", call_id="b", error="no such file")]
        with tempfile.NamedTemporaryFile("w", suffix=".ndjson", delete=False) as f:
            f.write("\n".join(lines) + "\n")
        self.addCleanup(os.remove, f.name)
        calls, _, _, _ = bookkeeping.lab.parse_events(f.name)
        self.assertEqual([c["output_chars"] for c in calls], [0, len("no such file")])

    def test_reconstruction_counts_reads_in_the_window_after_each_compaction(self):
        def at(c, compactions, chars=0):
            return dict(c, compactions_before=compactions, output_chars=chars)
        work = shell("go test ./...")
        calls = ([at(call("notes_read"), 0, 80)] +  # before any compaction: not counted
                 [at(call("notes_read"), 1, 400), at(call("read_file", file_path=PLAN), 1, 4000)] +
                 [at(work, 1)] * 8 +
                 [at(call("task_list"), 1, 40)] +  # the 11th call after the compaction: outside the window
                 [at(call("read_file", file_path=f"/w/{LEDGER}"), 2, 800)])
        found, windows = bookkeeping.reconstruction(calls)
        self.assertEqual({s: v for s, v in found.items() if v[0]},
                         {"whiteboard": [1, 100], "plan": [1, 1000], "ledger": [1, 200]})
        self.assertEqual(windows, 2)

    def test_reconstruction_windows_end_at_the_next_compaction(self):
        def at(c, compactions):
            return dict(c, compactions_before=compactions, output_chars=40)
        # Two compactions three calls apart: the ledger read after the second belongs to its window only.
        calls = [at(call("notes_read"), 1), at(shell("go test ./..."), 1), at(shell("go vet"), 1),
                 at(call("read_file", file_path=f"/w/{LEDGER}"), 2)]
        found, windows = bookkeeping.reconstruction(calls)
        self.assertEqual(({s: v[0] for s, v in found.items() if v[0]}, windows), ({"whiteboard": 1, "ledger": 1}, 2))

    def test_a_compaction_after_the_last_call_measures_no_window(self):
        calls = [dict(call("notes_read"), compactions_before=0, output_chars=4)]
        self.assertEqual(bookkeeping.reconstruction(calls)[1], 0)

    def test_delegate_events_in_the_stream_are_not_the_root_sessions(self):
        path = events_file(self, [
            event("SESSION_START", context_window_size=44000),
            event("TOOL_CALL_START", tool_name="read_file", call_id="a", arguments_json="{}"),
            event("SESSION_START", session="dlg", context_window_size=999),
            event("TOOL_CALL_START", session="dlg", tool_name="shell", call_id="d1", arguments_json="{}"),
            event("CONTEXT_COMPACTION", session="dlg", layer="checkpoint"),
            event("TOOL_CALL_END", session="dlg", tool_name="read_file", call_id="a", output="x" * 50),
            event("TOOL_CALL_START", tool_name="notes_read", call_id="b", arguments_json="{}")])
        calls, _, _, _ = bookkeeping.lab.parse_events(path)
        self.assertEqual([(c["tool"], c["compactions_before"], c["output_chars"]) for c in calls],
                         [("read_file", 0, 0), ("notes_read", 0, 0)])
        self.assertEqual(bookkeeping.lab.context_record(path), (44000, []))

    def test_the_window_is_the_root_sessions_even_when_it_reports_none(self):
        path = events_file(self, [event("SESSION_START"), event("SESSION_START", context_window_size=999)])
        self.assertEqual(bookkeeping.lab.context_record(path), (None, []))

    def test_calls_without_a_call_id_get_no_output(self):
        path = events_file(self, [
            event("TOOL_CALL_START", tool_name="shell", arguments_json="{}"),
            event("TOOL_CALL_START", tool_name="shell", arguments_json="{}"),
            event("TOOL_CALL_END", tool_name="shell", output="x" * 30)])
        calls, _, _, _ = bookkeeping.lab.parse_events(path)
        self.assertEqual([c["output_chars"] for c in calls], [0, 0])


if __name__ == "__main__":
    unittest.main()
