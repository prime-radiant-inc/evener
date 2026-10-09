#!/usr/bin/env python3
"""Tests for memcheck.py. Run: python3 -B tools/prompt-eval/memory-lab/test_memcheck.py"""
import contextlib, glob, io, os, sys, tempfile, unittest

sys.dont_write_bytecode = True
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import memcheck


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

    def test_other_key_ending_in_description_is_not_the_description(self):
        self.put("projects/p/a.md", "---\nx_description: " + "y" * 300 + "\ndescription: ok\n---\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)

    def test_body_line_starting_description_is_not_the_description(self):
        self.put("projects/p/a.md", "---\ndescription: ok\n---\ndescription: " + "y" * 300 + "\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)

    def test_folded_description_joins_its_lines(self):
        self.put("projects/p/a.md", "---\ndescription: >\n  " + "a" * 150 + "\n  " + "b" * 100 + "\n---\n")
        self.assertEqual(self.run_cmd("index-lines"), 1)

    def test_quoted_description_drops_its_quotes(self):
        self.put("projects/p/a.md", '---\ndescription: "' + "a" * 200 + '"\n---\n')
        self.assertEqual(self.run_cmd("index-lines"), 0)

    def test_dashes_after_a_body_are_not_frontmatter(self):
        self.put("projects/p/a.md", "# Title\n\n---\ndescription: " + "y" * 300 + "\n---\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)


    def test_nested_memory_md_is_a_page_not_the_index(self):
        self.put("projects/p/sub/MEMORY.md", "---\ndescription: short\n---\n" + "x" * 300 + "\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)

    def test_typed_description_is_no_index_line(self):
        self.put("projects/p/a.md", "---\ndescription: 42\n---\n")
        self.assertEqual(self.run_cmd("index-lines"), 0)

    def test_symlinked_page_is_not_read(self):
        outside = os.path.join(self.root, "outside.md")
        with open(outside, "w", encoding="utf-8") as f:
            f.write("---\ndescription: " + "y" * 300 + "\n---\n")
        self.put("projects/p/real.md", "---\ndescription: ok\n---\n")
        os.symlink(outside, os.path.join(self.root, "evener", "memory", "projects", "p", "link.md"))
        self.assertEqual(self.run_cmd("index-lines"), 0)

class NewPages(Root):
    def test_no_new_page_fails(self):
        self.put("projects/p/seed.md", "---\ndescription: d\ntags: [a]\nby: seed-fixture\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_memory_md_is_not_a_new_page(self):
        self.put("projects/p/MEMORY.md", "- a line\n")
        self.assertEqual(self.run_cmd("new-pages"), 1)

    def test_empty_description_fails(self):
        self.put("projects/p/a.md", "---\ndescription:\ntags: [a]\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_x_description_key_does_not_count(self):
        self.put("projects/p/a.md", "---\nx_description: yes\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_body_description_line_does_not_count(self):
        self.put("projects/p/a.md", "---\ntags: [a]\n---\ndescription: yes\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_described_page_passes(self):
        self.put("projects/p/a.md", "---\ndescription: yes\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 0)

    def test_tags_inside_seed_set_pass_in_flow_and_block_form(self):
        self.put("projects/p/a.md", "---\ntags: [A, 'b']\n---\n")
        self.put("projects/p/b.md", "---\ntags:\n- a\n- \"b\"\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "a,b"), 0)

    def test_tag_outside_seed_set_fails(self):
        self.put("projects/p/a.md", "---\ntags: [a, new]\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "a,b"), 1)

    def test_page_without_tags_fails_tag_reuse(self):
        self.put("projects/p/a.md", "---\ndescription: d\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "a"), 1)

    def test_page_without_frontmatter_fails_tag_reuse(self):
        self.put("projects/p/a.md", "# just text\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "a"), 1)


    def test_scalar_tag_is_one_tag(self):
        self.put("projects/p/a.md", "---\ntags: Coupons\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "coupons"), 0)

    def test_tag_whitespace_becomes_dashes(self):
        self.put("projects/p/a.md", "---\ntags: [Seed  Fixture]\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "seed-fixture"), 0)

    def test_unreadable_description_does_not_count(self):
        self.put("projects/p/a.md", "---\ndescription: Cart rule 7: whole numbers\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_description_ending_in_colon_is_unreadable(self):
        self.put("projects/p/a.md", "---\ndescription: Cart rules:\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_quoted_description_with_colon_counts(self):
        self.put("projects/p/a.md", "---\ndescription: 'Cart rule 7: whole numbers'\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 0)

    def test_nested_memory_md_is_a_new_page(self):
        self.put("projects/p/sub/MEMORY.md", "---\ndescription: d\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 0)

    def test_description_yaml_reads_as_null_bool_number_or_date_does_not_count(self):
        for value in ("null", "~", "false", "TRUE", "42", "0x1F", "1e3", ".inf", "2026-09-24"):
            with self.subTest(value=value):
                self.put("projects/p/a.md", f"---\ndescription: {value}\n---\n")
                self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_quoted_or_word_description_that_looks_typed_counts(self):
        for value in ('"null"', "'42'", "yes", "1.2.3"):
            with self.subTest(value=value):
                self.put("projects/p/a.md", f"---\ndescription: {value}\n---\n")
                self.assertEqual(self.run_cmd("new-pages", "--require-description"), 0)

    def test_non_markdown_page_is_a_new_page_without_a_description(self):
        self.put("projects/p/notes.txt", "---\ndescription: d\n---\n")
        self.assertEqual(self.run_cmd("new-pages"), 0)
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_hidden_files_are_not_pages(self):
        self.put("projects/p/.draft.md", "---\ndescription: d\n---\n")
        self.put("projects/p/.git/x.md", "---\ndescription: d\n---\n")
        self.assertEqual(self.run_cmd("new-pages"), 1)

    def test_glob_characters_in_the_state_root_are_literal(self):
        self.root = os.path.join(self.root, "run[1]")
        self.put("projects/p/a.md", "---\ndescription: d\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--require-description"), 0)

    def test_flow_collection_description_does_not_count(self):
        for value in ("[x]", "{a: b}"):
            with self.subTest(value=value):
                self.put("projects/p/a.md", f"---\ndescription: {value}\n---\n")
                self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_null_tag_items_are_dropped(self):
        self.put("projects/p/a.md", "---\ntags: [pricing, null, ~]\n---\n")
        self.assertEqual(self.run_cmd("new-pages", "--tags-subset-of", "pricing"), 0)

    def test_crlf_or_spaced_delimiter_is_no_frontmatter(self):
        for text in ("---\r\ndescription: d\r\n---\r\n", "--- \ndescription: d\n---\n"):
            with self.subTest(text=text):
                self.put("projects/p/a.md", text)
                self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_symlinked_dir_is_not_walked(self):
        target = os.path.join(self.root, "elsewhere")
        os.makedirs(target)
        with open(os.path.join(target, "a.md"), "w", encoding="utf-8") as f:
            f.write("---\ndescription: d\n---\n")
        os.makedirs(os.path.join(self.root, "evener", "memory", "projects", "p"))
        os.symlink(target, os.path.join(self.root, "evener", "memory", "projects", "p", "linked"))
        self.assertEqual(self.run_cmd("new-pages"), 1)

    def test_unterminated_or_trailing_quoted_description_is_unreadable(self):
        for value in ('"unterminated', "'unterminated", '"a" trailing'):
            with self.subTest(value=value):
                self.put("projects/p/a.md", f"---\ndescription: {value}\n---\n")
                self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_comment_after_a_typed_value_leaves_no_description(self):
        for value in ("42 # comment", "null # c", "# only a comment"):
            with self.subTest(value=value):
                self.put("projects/p/a.md", f"---\ndescription: {value}\n---\n")
                self.assertEqual(self.run_cmd("new-pages", "--require-description"), 1)

    def test_comment_after_a_string_drops_the_comment(self):
        for value, tag in (("a plain # " + "c" * 300, "plain"), ('"q" # c', "quoted"), ("a#b", "hash")):
            with self.subTest(tag=tag):
                self.put("projects/p/a.md", f"---\ndescription: {value}\n---\n")
                self.assertEqual(self.run_cmd("new-pages", "--require-description"), 0)
                self.assertEqual(self.run_cmd("index-lines"), 0)

class SeedFrontmatter(unittest.TestCase):
    def test_quoted_description_word_tags_and_date_pass(self):
        self.assertIsNone(memcheck.seed_frontmatter_problem(
            '---\ndescription: "Cart rule 7: whole numbers"\ntags: [cart, tests]\nupdated: 2026-09-24\nby: seed-fixture\n---\n# T\n'))

    def test_page_without_frontmatter_passes(self):
        self.assertIsNone(memcheck.seed_frontmatter_problem("# Just a page\n\nkey: value\n"))

    def test_plain_value_with_colon_space_fails(self):
        self.assertIsNotNone(memcheck.seed_frontmatter_problem("---\ndescription: Cart rule 7: whole numbers\n---\n"))

    def test_plain_sentence_and_folded_value_pass(self):
        self.assertIsNone(memcheck.seed_frontmatter_problem("---\ndescription: a plain sentence\n---\n"))
        self.assertIsNone(memcheck.seed_frontmatter_problem("---\ndescription: >\n  folded: text\n---\n"))

    def test_crlf_frontmatter_fails(self):
        self.assertIsNotNone(memcheck.seed_frontmatter_problem("---\r\ndescription: d\r\n---\r\n"))

    def test_plain_tag_with_colon_space_fails(self):
        self.assertIsNotNone(memcheck.seed_frontmatter_problem("---\ntags: a: b\n---\n"))

    def test_unclosed_frontmatter_fails(self):
        self.assertIsNotNone(memcheck.seed_frontmatter_problem("---\ndescription: \"x\"\n"))

    def test_every_scenario_seed_page_passes(self):
        scenarios = os.path.join(os.path.dirname(os.path.abspath(__file__)), "scenarios")
        pages = glob.glob(os.path.join(scenarios, "*", "seed", "**", "*.md"), recursive=True)
        self.assertIn(os.path.join(scenarios, "index-overflow", "seed", "coupon-stacking.md"), pages)
        problems = [(p, memcheck.seed_frontmatter_problem(memcheck.read(p))) for p in pages]
        self.assertEqual([pp for pp in problems if pp[1]], [])


if __name__ == "__main__":
    unittest.main()
