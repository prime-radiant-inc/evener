#!/usr/bin/env python3
"""Behavior tests for the bookkeeping tool's call classifier.

Run: python3 -B tools/prompt-eval/memory-lab/test_bookkeeping.py
Each case is a tool call as the lab records it and the (kind, surfaces) classify must return."""
import importlib.machinery, importlib.util, json, os, sys, unittest

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
]


class ClassifyTest(unittest.TestCase):
    def test_cases(self):
        for tool_call, want in CASES:
            with self.subTest(tool=tool_call["tool"], args=tool_call["args"][:80]):
                self.assertEqual(bookkeeping.classify(tool_call), want)


if __name__ == "__main__":
    unittest.main()
