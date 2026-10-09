#!/usr/bin/env python3
"""Tests for memcheck.py. Run: python3 -B tools/prompt-eval/memory-lab/test_memcheck.py

The tests build the real bin/memscope helper (go build) into a temp dir once, so
memcheck's checks run on pages exactly as evener reads them; how evener parses
frontmatter is tested in package agent."""
import contextlib, glob, io, os, subprocess, sys, tempfile, unittest

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import memcheck

_helper_dir = tempfile.TemporaryDirectory()


def setUpModule():
    memcheck.MEMSCOPE = os.path.join(_helper_dir.name, "memscope")
    subprocess.run(["go", "build", "-o", memcheck.MEMSCOPE, "./memscope"], cwd=HERE, check=True)


def tearDownModule():
    _helper_dir.cleanup()


class Root(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = self._tmp.name

    def put(self, rel, text):
        path = os.path.join(self.root, "evener", "memory", rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            f.write(text)

    def run_cmd(self, *argv):
        old = os.environ.get("XDG_STATE_HOME")
        os.environ["XDG_STATE_HOME"] = self.root
        self.addCleanup(lambda: os.environ.__setitem__("XDG_STATE_HOME", old) if old else os.environ.pop("XDG_STATE_HOME", None))
        with contextlib.redirect_stderr(io.StringIO()):
            return memcheck.main(list(argv))


class IndexLines(Root):
    def test_short_description_with_long_body_passes(self):
        self.put("projects/p/a.md", "---\ndescription: short\n---\n" + "x" * 500 + "\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)

    def test_long_description_in_subdirectory_fails(self):
        self.put("projects/p/sub/a.md", "---\ndescription: " + "y" * 201 + "\n---\n")
        self.assertEqual(self.run_cmd("index-lines"), 1)

    def test_long_memory_md_line_fails(self):
        self.put("personal/MEMORY.md", "- " + "z" * 250 + "\n")
        self.assertEqual(self.run_cmd("index-lines"), 1)

    def test_counts_runes_not_bytes(self):
        self.put("projects/p/a.md", "---\ndescription: " + "—" * 200 + "\n---\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)
        self.put("projects/p/b.md", "---\ndescription: " + "—" * 201 + "\n---\n")
        self.assertEqual(self.run_cmd("index-lines"), 1)

    def test_nested_memory_md_is_a_page_not_the_index(self):
        self.put("projects/p/sub/MEMORY.md", "---\ndescription: short\n---\n" + "x" * 300 + "\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)

    def test_page_without_a_description_has_no_index_line_to_measure(self):
        self.put("projects/p/a.md", "# " + "t" * 300 + "\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)

    def test_glob_characters_in_the_state_root_are_literal(self):
        self.root = os.path.join(self.root, "run[1]")
        self.put("projects/p/a.md", "---\ndescription: " + "y" * 201 + "\n---\n")
        self.assertEqual(self.run_cmd("index-lines"), 1)


class NewPages(Root):
    def test_no_new_page_fails(self):
        self.put("projects/p/seed.md", "---\ndescription: d\ntags: [a]\nby: seed-fixture\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_memory_md_is_not_a_new_page(self):
        self.put("projects/p/MEMORY.md", "- a line\n")
        self.assertEqual(self.run_cmd("new-pages"), 1)

    def test_nested_memory_md_is_a_new_page(self):
        self.put("projects/p/sub/MEMORY.md", "---\ndescription: d\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 0)

    def test_empty_description_fails(self):
        self.put("projects/p/a.md", "---\ndescription:\ntags: [a]\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_described_page_passes(self):
        self.put("projects/p/a.md", "---\ndescription: yes\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 0)

    def test_unreadable_description_does_not_count(self):
        self.put("projects/p/a.md", "---\ndescription: Cart rule 7: whole numbers\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_non_markdown_page_is_a_new_page_without_a_description(self):
        self.put("projects/p/notes.txt", "---\ndescription: d\n---\n")
        self.assertEqual(self.run_cmd("new-pages"), 0)
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_tags_inside_seed_set_pass(self):
        self.put("projects/p/a.md", "---\ntags: [A, 'b']\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "a,b"), 0)

    def test_tag_outside_seed_set_fails(self):
        self.put("projects/p/a.md", "---\ntags: [a, new]\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "a,b"), 1)

    def test_seed_set_is_normalized_like_page_tags(self):
        self.put("projects/p/a.md", "---\ntags: [a, Read Only]\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "A, read  only"), 0)

    def test_page_without_tags_fails_tag_reuse(self):
        self.put("projects/p/a.md", "---\ndescription: d\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "a"), 1)

    def test_page_without_frontmatter_fails_tag_reuse(self):
        self.put("projects/p/a.md", "# just text\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "a"), 1)

    def test_missing_helper_exits_with_how_to_build_it(self):
        self.put("projects/p/a.md", "---\ndescription: d\n---\n")
        built = memcheck.MEMSCOPE
        memcheck.MEMSCOPE = os.path.join(self.root, "no-such-memscope")
        self.addCleanup(setattr, memcheck, "MEMSCOPE", built)
        with self.assertRaises(SystemExit) as cm:
            self.run_cmd("new-pages")
        self.assertIn("go build -o bin/memscope", str(cm.exception.code))


class SeedProblems(Root):
    def test_unreadable_or_unread_frontmatter_is_a_problem(self):
        self.put("seed/a.md", "---\ndescription: Cart rule 7: whole numbers\n---\n")
        self.put("seed/b.md", "# no frontmatter is fine\n")
        self.put("seed/c.md", "---\r\ndescription: d\r\n---\r\n")
        self.put("seed/d.md", "---\ndescription: never closed\n")
        self.put("seed/e.md", "---\ndescription: fine\n---\n")
        seed = os.path.join(self.root, "evener", "memory", "seed")
        self.assertEqual(memcheck.seed_problems([seed]), {seed: [
            "a.md: frontmatter unreadable",
            "c.md: starts with --- but evener reads no description from it",
            "d.md: starts with --- but evener reads no description from it"]})

    def test_every_scenario_seed_reads(self):
        seeds = sorted(glob.glob(os.path.join(HERE, "scenarios", "*", "seed")))
        self.assertIn(os.path.join(HERE, "scenarios", "index-overflow", "seed"), seeds)
        self.assertEqual(memcheck.seed_problems(seeds), {})


if __name__ == "__main__":
    unittest.main()
