"""Tests for inventory.py's copy-only test detection. Run: python3 -B tools/copy-oracle/test_inventory.py"""
import importlib.util, os, unittest

spec = importlib.util.spec_from_file_location("inventory", os.path.join(os.path.dirname(__file__), "inventory.py"))
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


def lines_containing(text, *needles):
    return {n for n, line in enumerate(text.splitlines(), 1) if any(x in line for x in needles)}


class TypeScriptCopyOnly(unittest.TestCase):
    SRC = '''import { render, screen } from "@testing-library/react";

test("shows the empty title", () => {
  render(<Empty />);
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});

test("starting a session sends turn/start", async () => {
  render(<Composer />);
  await user.click(screen.getByRole("button", { name: "Send" }));
  expect(fake.calls).toHaveLength(1);
  expect(screen.getByText("Sending…")).toBeTruthy();
});

it.each([1, 2])("renders hint %s", (n) => {
  render(<Hint n={n} />);
  expect(screen.queryByText("Try again")).not.toBeNull();
});

test("no assertions at all", () => {
  render(<Thing />);
});
'''

    def test_flags_only_tests_whose_every_check_is_copy(self):
        hits = lines_containing(self.SRC, '"No sessions yet"', '"Sending…"', '"Try again"')
        found = inventory.copy_only_tests(self.SRC, hits, "ts")
        self.assertEqual([name for _, name in found], ["shows the empty title", "renders hint %s"])

    def test_reports_the_line_the_test_starts_on(self):
        hits = lines_containing(self.SRC, '"No sessions yet"')
        self.assertEqual(inventory.copy_only_tests(self.SRC, hits, "ts")[0][0], 3)


class GoCopyOnly(unittest.TestCase):
    SRC = '''package x

func TestUsageNamesTheFlags(t *testing.T) {
	out, err := run("--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Usage: evener") {
		t.Fatalf("usage = %q", out)
	}
}

func TestRejectsEmptyName(t *testing.T) {
	_, err := create("")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestCountsRows(t *testing.T) {
	rows := load()
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
}
'''

    def test_setup_error_guards_do_not_count_as_claims(self):
        hits = lines_containing(self.SRC, '"Usage: evener"', '"name is required"')
        found = inventory.copy_only_tests(self.SRC, hits, "go")
        self.assertEqual([name for _, name in found], ["TestUsageNamesTheFlags"])

    def test_expecting_an_error_is_a_real_claim(self):
        hits = lines_containing(self.SRC, '"name is required"')
        self.assertEqual(inventory.copy_only_tests(self.SRC, hits, "go"), [])


class Refinements(unittest.TestCase):
    def test_an_expected_error_on_the_same_line_is_a_behavior_claim(self):
        src = """func TestRejectsEmptyTarget(t *testing.T) {
	if _, err := configure(""); err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatal(err)
	}
}
"""
        self.assertEqual(inventory.copy_only_tests(src, {2}, "go"), [])

    def test_text_the_test_feeds_in_is_fixture_not_copy(self):
        src = """test("edit_file body renders the diff lines", () => {
  const args = JSON.stringify({ old_string: "old line", new_string: "new line" });
  render(<Body args={args} />);
  expect(screen.getByText("old line")).toBeTruthy();
  expect(screen.getByText("new line")).toBeTruthy();
});
"""
        self.assertEqual(inventory.copy_only_tests(src, {4, 5}, "ts"), [])

    def test_text_used_only_to_locate_an_attribute_check_is_not_a_copy_claim(self):
        src = """test("scopes the dark pane", () => {
  render(<ThemeFlip />);
  expect(screen.getByText("Dark").parentElement?.getAttribute("data-theme")).toBe("dark");
});
"""
        self.assertEqual(inventory.copy_only_tests(src, {3}, "ts"), [])

    def test_a_behavior_named_test_with_only_a_copy_oracle_is_reported_apart(self):
        src = """test("resetGeneration rejects invalidation waiters", async () => {
  r.resetGeneration("h");
  expect(await settled).toMatch(/generation mismatch/);
});
"""
        self.assertEqual(inventory.copy_only_tests(src, {3}, "ts"), [])
        self.assertEqual([n for _, n in inventory.copy_only_tests(src, {3}, "ts", purpose="behavior")],
                         ["resetGeneration rejects invalidation waiters"])


if __name__ == "__main__":
    unittest.main()
