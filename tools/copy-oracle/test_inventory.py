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


class RoborevRound1(unittest.TestCase):
    def test_braces_inside_strings_do_not_merge_the_next_test(self):
        src = """func TestRefusesLiteralHeader(t *testing.T) {
	_, err := add(`x-api-key: ${PORTKEY`)
	if err == nil {
		t.Fatal("want refusal")
	}
	cfg := "{ unterminated"
	_ = cfg
}

func TestHelpShowsUsage(t *testing.T) {
	if !strings.Contains(help(), "Usage: evener") {
		t.Fatal("no usage")
	}
}
"""
        self.assertEqual([n for _, n in inventory.copy_only_tests(src, {11}, "go")], ["TestHelpShowsUsage"])

    def test_a_count_on_the_same_line_is_a_behavior_claim(self):
        ts = """test("renders Recommended in both themes", () => {
  render(<Specimen />);
  expect(screen.getAllByText("Recommended")).toHaveLength(THEMES);
});
"""
        self.assertEqual(inventory.copy_only_tests(ts, {3}, "ts"), [])
        go = """func TestRenderShowsTwoRows(t *testing.T) {
	out := render()
	if len(rows(out)) != 2 || !strings.Contains(out, "Ready") {
		t.Fatal(out)
	}
}
"""
        self.assertEqual(inventory.copy_only_tests(go, {3}, "go"), [])

    def test_the_name_comments_and_diagnostics_are_not_fixture_inputs(self):
        src = """func TestRenderShowsNoSessions(t *testing.T) {
	// The empty view says "no sessions yet".
	out := render(nil)
	if !strings.Contains(out, "no sessions yet") {
		t.Fatalf("want \"no sessions yet\", got %q", out)
	}
}
"""
        self.assertEqual([n for _, n in inventory.copy_only_tests(src, {4}, "go")], ["TestRenderShowsNoSessions"])
        ts = """test("renders No sessions yet", () => {
  render(<Empty />);
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});
"""
        self.assertEqual([n for _, n in inventory.copy_only_tests(ts, {3}, "ts")], ["renders No sessions yet"])


class RoborevRound2(unittest.TestCase):
    def test_a_variable_comparison_in_the_condition_is_a_behavior_claim(self):
        src = """func TestHelpShowsUsage(t *testing.T) {
	out, status := run("--help")
	if status != expectedStatus || !strings.Contains(out, "Usage: evener") {
		t.Fatal(out)
	}
}
"""
        self.assertEqual(inventory.copy_only_tests(src, {3}, "go"), [])

    def test_operators_after_a_string_still_split_at_the_right_place(self):
        src = """func TestHelpShowsUsage(t *testing.T) {
	out, status := run("--help")
	if !strings.Contains(out, "Usage: evener && more") || status != expectedStatus {
		t.Fatal(out)
	}
}
"""
        self.assertEqual(inventory.copy_only_tests(src, {3}, "go"), [])
        self.assertEqual([t.strip() for t in inventory.go_condition_terms(src.splitlines()[2])],
                         ['!strings.Contains(out, "Usage: evener && more")', "status != expectedStatus"])

    def test_expression_bodied_tests_do_not_swallow_their_neighbours(self):
        src = """test("shows the hint", () => expect(screen.getByText("Try again")).toBeTruthy());

test("sends the request", async () => {
  await send();
  expect(fake.calls).toHaveLength(1);
});

test("renders the label", () =>
  expect(screen.getByText("Retry")).toBeTruthy());
"""
        found = inventory.copy_only_tests(src, {1, 9}, "ts")
        self.assertEqual([n for _, n in found], ["shows the hint", "renders the label"])


class RoborevRound3(unittest.TestCase):
    def test_a_machine_token_comparison_is_not_a_copy_term(self):
        src = """func TestHelpShowsUsage(t *testing.T) {
	status, out := run()
	if status != "running" || !strings.Contains(out, "Usage: evener") {
		t.Fatal(out)
	}
}
"""
        self.assertEqual(inventory.copy_only_tests(src, {3}, "go"), [])

    def test_parentheses_in_regex_literals_do_not_end_or_extend_a_test(self):
        early_end = """test("shows the retry label", async () => {
  expect(screen.getByText(/Retry[)]/)).toBeTruthy();
  await send();
  expect(fake.calls).toHaveLength(1);
});
"""
        self.assertEqual(inventory.copy_only_tests(early_end, {2}, "ts"), [])
        absorbs = """test("shows the open paren label", () => {
  expect(screen.getByText(/Open[(]/)).toBeTruthy();
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
"""
        found = inventory.copy_only_tests(absorbs, {2, 6}, "ts")
        self.assertEqual([n for _, n in found], ["shows the open paren label", "renders the hint"])

    def test_a_semicolon_in_a_trailing_comment_does_not_split_the_condition(self):
        src = """func TestHelpShowsUsage(t *testing.T) {
	out := help()
	if !strings.Contains(out, "Usage: evener") { // check; wording
		t.Fatal(out)
	}
}
"""
        self.assertEqual([n for _, n in inventory.copy_only_tests(src, {3}, "go")], ["TestHelpShowsUsage"])


class JsxIsNotARegex(unittest.TestCase):
    def test_inline_jsx_closing_tags_do_not_merge_neighbouring_tests(self):
        src = """test("renders the body", () => render(<div>body</div>));

test("shows the hint", () => {
  render(<span>/tmp/path</span>);
  expect(screen.getByText("Try again")).toBeTruthy();
});
"""
        blocks = inventory.test_blocks(src, "ts")
        self.assertEqual([name for _, name, _ in blocks], ["renders the body", "shows the hint"])
        self.assertEqual([n for _, n in inventory.copy_only_tests(src, {5}, "ts")], ["shows the hint"])


class FastFollow(unittest.TestCase):
    def test_an_apostrophe_in_jsx_text_does_not_merge_neighbouring_tests(self):
        src = """test("renders the advice", () => render(<span>Don't retry</span>));

test("shows the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
"""
        blocks = inventory.test_blocks(src, "ts")
        self.assertEqual([name for _, name, _ in blocks], ["renders the advice", "shows the hint"])

    def test_text_supplied_in_an_if_initializer_is_fixture(self):
        src = """func TestRenderShowsTheLabel(t *testing.T) {
	if got := render("Try again"); got != "Try again" {
		t.Fatalf("got %q", got)
	}
}
"""
        self.assertEqual(inventory.copy_only_tests(src, {2}, "go"), [])


class FastFollowRound2(unittest.TestCase):
    def test_a_string_after_a_keyword_is_still_a_string(self):
        src = """test("shows the retry label", () => {
  const label = () => { return "Retry("; };
  expect(screen.getByText(label())).toBeTruthy();
  expect(fake.calls).toHaveLength(1);
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
"""
        blocks = inventory.test_blocks(src, "ts")
        self.assertEqual([name for _, name, _ in blocks], ["shows the retry label", "renders the hint"])

    def test_only_condition_literals_are_compared_with_supplied_text(self):
        unrelated = """func TestRenderShowsEmpty(t *testing.T) {
	if got := render("empty"); got != "No sessions yet" {
		t.Fatalf("got %q", got)
	}
}
"""
        self.assertEqual([n for _, n in inventory.copy_only_tests(unrelated, {2}, "go")], ["TestRenderShowsEmpty"])
        passthrough = """func TestRenderShowsTheLabel(t *testing.T) {
	if got := render("Try again"); got != "Try again" {
		t.Fatalf("got %q", got)
	}
}
"""
        self.assertEqual(inventory.copy_only_tests(passthrough, {2}, "go"), [])


class TypePositionStrings(unittest.TestCase):
    def test_a_literal_type_after_as_or_satisfies_is_a_string(self):
        for kw in ("as", "satisfies"):
            src = f"""test("shows the retry label", () => {{
  const label = value {kw} "Retry(";
  expect(fake.calls).toHaveLength(1);
}});

test("renders the hint", () => {{
  expect(screen.getByText("Try again")).toBeTruthy();
}});
"""
            blocks = inventory.test_blocks(src, "ts")
            self.assertEqual([name for _, name, _ in blocks], ["shows the retry label", "renders the hint"], kw)


if __name__ == "__main__":
    unittest.main()
