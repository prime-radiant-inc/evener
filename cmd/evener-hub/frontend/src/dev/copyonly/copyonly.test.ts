// @vitest-environment node
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, test } from "vitest";
import { contractShaped, regexCore, Scanner, scanRepo } from "./copyonly";

const HERE = dirname(fileURLToPath(import.meta.url));

/** The names of the targets in one test file, scanned against a production corpus of prod. */
function targetNames(src: string, prod: string[], file = "a.test.tsx"): string[] {
  return new Scanner(prod.join("\n")).scanFile(file, src).map((t) => t.test);
}

const FLAG_SRC = `import { render, screen } from "@testing-library/react";

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
`;

describe("targets", () => {
  test.each<[string, string, string[], string[]]>([
    [
      "only tests whose every check is copy",
      FLAG_SRC,
      ["No sessions yet", "Sending…", "Try again"],
      ["shows the empty title", "renders hint %s"],
    ],
    [
      "text the test feeds in is fixture, not copy",
      `test("edit_file body renders the diff lines", () => {
  const args = JSON.stringify({ old_string: "old line", new_string: "new line" });
  render(<Body args={args} />);
  expect(screen.getByText("old line")).toBeTruthy();
  expect(screen.getByText("new line")).toBeTruthy();
});
`,
      ["old line", "new line"],
      [],
    ],
    [
      "text used only to locate an attribute check is not a copy claim",
      `test("scopes the dark pane", () => {
  render(<ThemeFlip />);
  expect(screen.getByText("Dark").parentElement?.getAttribute("data-theme")).toBe("dark");
});
`,
      ["Dark"],
      [],
    ],
    [
      "a behavior-named test with only a copy oracle is not a target",
      `test("resetGeneration rejects invalidation waiters", async () => {
  r.resetGeneration("h");
  expect(await settled).toMatch(/generation mismatch/);
});
`,
      ["generation mismatch"],
      [],
    ],
    [
      "the same copy-only body under a wording name is a target",
      `test("shows the generation mismatch", async () => {
  r.resetGeneration("h");
  expect(await settled).toMatch(/generation mismatch/);
});
`,
      ["generation mismatch"],
      ["shows the generation mismatch"],
    ],
    [
      "the name is not a fixture input",
      `test("renders No sessions yet", () => {
  render(<Empty />);
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});
`,
      ["No sessions yet"],
      ["renders No sessions yet"],
    ],
    [
      "expression-bodied tests do not swallow their neighbours",
      `test("shows the hint", () => expect(screen.getByText("Try again")).toBeTruthy());

test("sends the request", async () => {
  await send();
  expect(fake.calls).toHaveLength(1);
});

test("renders the label", () =>
  expect(screen.getByText("Retry")).toBeTruthy());
`,
      ["Try again", "Retry"],
      ["shows the hint", "renders the label"],
    ],
    [
      "a paren in a regex literal does not end a test early",
      `test("shows the retry label", async () => {
  expect(screen.getByText(/Retry[)]/)).toBeTruthy();
  await send();
  expect(fake.calls).toHaveLength(1);
});
`,
      ["Retry"],
      [],
    ],
    [
      "a paren in a regex literal does not absorb the next test",
      `test("shows the open paren label", () => {
  expect(screen.getByText(/Open[(]/)).toBeTruthy();
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
      ["Open", "Try again"],
      ["shows the open paren label", "renders the hint"],
    ],
    [
      "inline JSX closing tags do not merge neighbouring tests",
      `test("renders the body", () => render(<div>body</div>));

test("shows the hint", () => {
  render(<span>/tmp/path</span>);
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
      ["Try again"],
      ["shows the hint"],
    ],
    [
      "an apostrophe in JSX text does not merge neighbouring tests",
      `test("renders the advice", () => render(<span>Don't retry</span>));

test("shows the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
      ["Try again"],
      ["shows the hint"],
    ],
    [
      "JSX text the test renders is fixture",
      `test("shows the advice", () => {
  render(<span>Don't retry</span>);
  expect(screen.getByText("Don't retry")).toBeTruthy();
});
`,
      ["Don't retry"],
      [],
    ],
    [
      "a string after a keyword is still a string",
      `test("shows the retry label", () => {
  const label = () => { return "Retry("; };
  expect(screen.getByText(label())).toBeTruthy();
  expect(fake.calls).toHaveLength(1);
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
      ["Try again"],
      ["renders the hint"],
    ],
    [
      "a literal type after as does not blur boundaries",
      `test("shows the retry label", () => {
  const label = value as "Retry(";
  expect(fake.calls).toHaveLength(1);
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
      ["Try again"],
      ["renders the hint"],
    ],
    [
      "a literal type after satisfies does not blur boundaries",
      `test("shows the retry label", () => {
  const label = value satisfies "Retry(";
  expect(fake.calls).toHaveLength(1);
});

test("renders the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
      ["Try again"],
      ["renders the hint"],
    ],
    [
      "an imported copy constant is a copy claim",
      `import { EMPTY_MESSAGE } from "./Empty";

test("shows the empty message", () => {
  expect(screen.getByText(EMPTY_MESSAGE)).toBeTruthy();
});
`,
      [],
      ["shows the empty message"],
    ],
    [
      "a constant imported from test support is not copy",
      `import { EMPTY_MESSAGE } from "./testing/fixtures";

test("shows the empty message", () => {
  expect(screen.getByText(EMPTY_MESSAGE)).toBeTruthy();
});
`,
      [],
      [],
    ],
    [
      "a literal found only in tests is fixture",
      `test("shows the empty title", () => {
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});
`,
      [],
      [],
    ],
    [
      "a contract-shaped literal is not copy",
      `test("shows the running status", () => {
  expect(screen.getByTestId("status").textContent).toBe("session_running");
});
`,
      ["session_running"],
      [],
    ],
    [
      "a trailing comment naming a count is not a check",
      `test("shows the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy(); // toHaveLength(1)
});
`,
      ["Try again"],
      ["shows the hint"],
    ],
    [
      "a commented-out check is not a check",
      `test("shows the hint", () => {
  expect(screen.getByText("Try again")).toBeTruthy();
  // expect(fake.calls).toHaveLength(1);
});
`,
      ["Try again"],
      ["shows the hint"],
    ],
    [
      "a check in a string is not a check",
      `test("shows the hint", () => {
  const note = "expect(fake.calls).toHaveLength(1)";
  expect(screen.getByText("Try again")).toBeTruthy();
});
`,
      ["Try again"],
      ["shows the hint"],
    ],
    [
      "a multi-line test.each table supplies its rows",
      `test.each([
  ["Retry"],
])("renders the %s label", () => {
  expect(screen.getByText("Retry")).toBeTruthy();
});
`,
      ["Retry"],
      [],
    ],
    [
      "a one-line test.each table supplies its rows",
      `test.each([["Retry"]])("renders the %s label", () => {
  expect(screen.getByText("Retry")).toBeTruthy();
});
`,
      ["Retry"],
      [],
    ],
    [
      "a tagged-template test.each table supplies its rows",
      `test.each\`
  label
  \${"Retry"}
\`("renders the $label label", () => {
  expect(screen.getByText("Retry")).toBeTruthy();
});
`,
      ["Retry"],
      [],
    ],
    [
      "a tagged-template test.each declaration is a test",
      `test.each\`
  n
  \${1}
\`("renders the $n label", () => {
  expect(screen.getByText("Retry")).toBeTruthy();
});
`,
      ["Retry"],
      ["renders the $n label"],
    ],
    [
      "a multi-line declaration's name is not a fixture input",
      `test(
  "renders Try again",
  () => {
    expect(screen.getByText("Try again")).toBeTruthy();
  },
);
`,
      ["Try again"],
      ["renders Try again"],
    ],
    [
      "a standalone by-text query is a claim",
      `test("shows the hint", () => {
  screen.getByText("Try again");
});
`,
      ["Try again"],
      ["shows the hint"],
    ],
    [
      "a role query is behavior",
      `test("shows the alert", () => {
  expect(screen.getByRole("alert")).toHaveTextContent("Couldn't load models");
});
`,
      ["Couldn't load models"],
      [],
    ],
    [
      "a spy assertion is behavior",
      `test("shows the hint", () => {
  expect(onShow).toHaveBeenCalledWith("Try again");
});
`,
      ["Try again"],
      [],
    ],
  ])("%s", (_name, src, prod, want) => {
    expect(targetNames(src, prod)).toEqual(want);
  });

  test("reports the line the test starts on", () => {
    const got = new Scanner("No sessions yet").scanFile("a.test.tsx", FLAG_SRC);
    expect(got).toEqual([{ file: "a.test.tsx", test: "shows the empty title", line: 3 }]);
  });

  test("reads .ts files without JSX", () => {
    const src = `test("shows the cast label", () => {
  const n = <number>count;
  expect(screen.getByText("Try again")).toBeTruthy();
});
`;
    expect(targetNames(src, ["Try again"], "a.test.ts")).toEqual(["shows the cast label"]);
  });
});

describe("counts, structure and state are behavior", () => {
  test.each([
    `expect(screen.getAllByText("Recommended")).toHaveLength(2);`,
    `expect(screen.getAllByText("Recommended").length).toBe(2);`,
    `expect(screen.getAllByText("Recommended").length).toEqual(2);`,
    `expect(screen.queryAllByText("Recommended").length).toBeGreaterThanOrEqual(1);`,
    `expect(rowsMatching(screen.getByText("Recommended"))).toBe(2);`,
    `expect(rowsMatching(screen.getByText("Recommended"))).toStrictEqual(-1);`,
    `expect(screen.getByText("Recommended").tagName).toBe("BUTTON");`,
    `expect(screen.getByText("Recommended").hasAttribute("disabled")).toBe(true);`,
    `expect(screen.getByText("Recommended").classList.contains("active")).toBe(true);`,
    `expect(screen.getByText("Recommended").dataset.state).toBe("open");`,
    `expect((screen.getByText("Recommended") as HTMLInputElement).checked).toBe(true);`,
    `expect(screen.getByText("Recommended")).toHaveAttribute("aria-current");`,
    `expect.soft(screen.getAllByText("Recommended")).toHaveLength(2);`,
    `await expect.poll(() => screen.queryAllByText("Recommended")).toHaveLength(2);`,
    `await expect.poll(() => screen.queryAllByText("Recommended").length).toBe(2);`,
    `(expect(screen.getAllByText("Recommended"))).toHaveLength(2);`,
    `(expect(screen.getAllByText("Recommended")) as Assertion<HTMLElement[]>).toHaveLength(2);`,
    `(expect(screen.getAllByText("Recommended")) satisfies unknown as Assertion<HTMLElement[]>).toHaveLength(2);`,
  ])("%s", (check) => {
    const src = `test("renders Recommended in both themes", () => {\n  render(<Specimen />);\n  ${check}\n});\n`;
    expect(targetNames(src, ["Recommended"])).toEqual([]);
  });

  test.each([
    `expect(screen.getByText("Recommended")).toBeTruthy();`,
    `expect.soft(screen.getByText("Recommended")).toBeTruthy();`,
    `await expect.poll(() => screen.queryByText("Recommended")).not.toBeNull();`,
    `await expect.poll(() => status.textContent).toBe("Recommended");`,
  ])("the same test with a copy-only check is a target: %s", (check) => {
    const src = `test("renders Recommended in both themes", async () => {\n  render(<Specimen />);\n  ${check}\n});\n`;
    expect(targetNames(src, ["Recommended"])).toEqual(["renders Recommended in both themes"]);
  });
});

describe("declaration forms give the same targets", () => {
  const neighbours = `
test("sends the request", async () => {
  await send();
  expect(fake.calls).toHaveLength(1);
});

test("shows the label", () => {
  expect(screen.getByText("Retry")).toBeTruthy();
});
`;
  const body = `expect(screen.getByText("Try again")).toBeTruthy();`;
  test.each<[string, string, string]>([
    ["one line", "renders the hint", `test("renders the hint", () => {\n  ${body}\n});\n`],
    ["name on the next line", "renders the hint", `test(\n  "renders the hint",\n  () => {\n    ${body}\n  },\n);\n`],
    [
      "modifier, name on the next line",
      "renders the hint",
      `it.only(\n  "renders the hint",\n  () => {\n    ${body}\n  },\n);\n`,
    ],
    ["chained modifiers", "renders the hint", `test.concurrent.skip("renders the hint", () => {\n  ${body}\n});\n`],
    ["each on one line", "renders hint %s", `it.each([1, 2])("renders hint %s", (n) => {\n  ${body}\n});\n`],
    [
      "each over several lines",
      "renders hint %s",
      `test.each([\n  [1, "one"],\n  [2, "two"],\n])(\n  "renders hint %s",\n  (n) => {\n    ${body}\n  },\n);\n`,
    ],
    [
      "each table closing alone",
      "renders hint %s",
      `test.each(\n  cases\n)("renders hint %s", (n) => {\n  ${body}\n});\n`,
    ],
    ["typed each", "renders hint %s", `test.each<[number]>([[1], [2]])("renders hint %s", (n) => {\n  ${body}\n});\n`],
    [
      "nested generic each",
      "renders hint %s",
      `it.each<Array<[string, number]>>([\n  ["a", 1],\n])(\n  "renders hint %s",\n  (n) => {\n    ${body}\n  },\n);\n`,
    ],
    [
      "multi-line type arguments",
      "renders hint %s",
      `test.each<\n  [string, number]\n>([\n  ["a", 1],\n])("renders hint %s", (n) => {\n  ${body}\n});\n`,
    ],
    [
      "function type in the type arguments",
      "renders hint %s",
      `test.each<[(n: number) => string]>([[String]])("renders hint %s", (f) => {\n  ${body}\n});\n`,
    ],
  ])("%s", (_form, name, src) => {
    const got = new Scanner(["Try again", "Retry"].join("\n")).scanFile("a.test.tsx", src + neighbours);
    expect(got.map((t) => t.test)).toEqual([name, "shows the label"]);
    expect(got[0]?.line).toBe(1);
  });
});

test("regexCore is the longest run of literal text", () => {
  expect(regexCore("Retry[)]")).toBe("Retry");
  expect(regexCore("Usage: \\w+ evener")).toBe("Usage: ");
  expect(regexCore("no sessions yet\\.")).toBe("no sessions yet.");
  expect(regexCore("^(a|b) longer phrase")).toBe(" longer phrase");
});

test("contractShaped tells machine contracts from copy", () => {
  const cases: [string, boolean][] = [
    ["ab", true],
    ["123", true],
    ["session_running", true],
    ["--json", true],
    ["Welcome", false],
    ["No sessions yet", false],
    ["cat > file", true],
    ["export type X", true],
    ["<tag attr=", true],
  ];
  for (const [lit, want] of cases) expect([lit, contractShaped(lit)]).toEqual([lit, want]);
});

describe("inventory", () => {
  const SRC = `import { EMPTY_MESSAGE } from "./Empty";
import { FIXTURE_LABEL } from "./testing/fixtures";

test("renders the empty state", () => {
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});

test("retries after a failure", async () => {
  await user.click(screen.getByText("Retry"));
  fireEvent.click(screen.getByTitle("Close panel"));
  expect(screen.getByText(EMPTY_MESSAGE)).toBeTruthy();
  expect(screen.getByText(FIXTURE_LABEL)).toBeTruthy();
});

test("sends the request", () => {
  // expect(screen.getByText("Commented copy")).toBeTruthy();
  expect(status.textContent).toBe(
    "Saved to disk",
  );
  expect(screen.getByText("only in this test")).toBeTruthy();
  expect(screen.getByText("session_running")).toBeTruthy();
});
`;
  const prod = ["No sessions yet", "Retry", "Close panel", "Saved to disk", "Commented copy", "session_running"];

  test("lists every copy hit by category, comments excluded and split assertions included", () => {
    const got = new Scanner(prod.join("\n")).inventoryFile("src/a.test.tsx", SRC);
    expect(got.hits).toEqual([
      { file: "src/a.test.tsx", line: 5, category: "ts-assert", literal: "No sessions yet" },
      { file: "src/a.test.tsx", line: 9, category: "ts-locator", literal: "Retry" },
      { file: "src/a.test.tsx", line: 10, category: "ts-locator", literal: "Close panel" },
      { file: "src/a.test.tsx", line: 11, category: "ts-constant", literal: "EMPTY_MESSAGE" },
      { file: "src/a.test.tsx", line: 17, category: "ts-assert", literal: "Saved to disk" },
    ]);
    // "only in this test" is fixture data; "session_running" is contract-shaped.
    expect(got.fixtures).toBe(1);
  });

  test("lists copy-only tests named for behavior as incidental", () => {
    const got = new Scanner(prod.join("\n")).inventoryFile("src/a.test.tsx", SRC);
    expect(got.copyOnly).toEqual([
      { file: "src/a.test.tsx", line: 4, test: "renders the empty state", purpose: "copy" },
    ]);
    const behavior = `test("retries after a failure", () => {\n  expect(screen.getByText("Retry")).toBeTruthy();\n});\n`;
    expect(new Scanner("Retry").inventoryFile("src/b.test.tsx", behavior).copyOnly).toEqual([
      { file: "src/b.test.tsx", line: 1, test: "retries after a failure", purpose: "behavior" },
    ]);
  });

  test("runs under node and prints the inventory as JSON for evener dev copy-oracle", () => {
    const root = mkdtempSync(join(tmpdir(), "copyonly-cli-"));
    try {
      const front = "cmd/evener-hub/frontend/src/";
      mkdirSync(join(root, front), { recursive: true });
      writeFileSync(join(root, `${front}Empty.tsx`), `export const Empty = () => <p>No sessions yet</p>;\n`);
      writeFileSync(join(root, `${front}Empty.test.tsx`), SRC);
      execFileSync("git", ["-C", root, "init", "-q"], { encoding: "utf8" });
      execFileSync("git", ["-C", root, "add", "--", "."], { encoding: "utf8" });
      const out = execFileSync("node", [join(HERE, "copyonly.ts"), "--root", root, "--path", front], {
        encoding: "utf8",
      });
      const report = JSON.parse(out) as {
        hits: { category: string }[];
        fixtures: Record<string, number>;
        copy_only: { test: string; purpose: string }[];
      };
      expect(report.hits.map((h) => h.category)).toEqual(["ts-assert", "ts-constant"]);
      // Retry, Close panel, Saved to disk and the test-only text; the comment is not code.
      expect(report.fixtures).toEqual({ "cmd/evener-hub/frontend/src": 4 });
      expect(report.copy_only).toEqual([
        { file: `${front}Empty.test.tsx`, line: 4, test: "renders the empty state", purpose: "copy" },
      ]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});

test("scanRepo reads tracked tests in the TypeScript trees against their production sources", () => {
  const root = mkdtempSync(join(tmpdir(), "copyonly-"));
  try {
    const write = (path: string, body: string) => {
      mkdirSync(dirname(join(root, path)), { recursive: true });
      writeFileSync(join(root, path), body);
    };
    const copyTest = (name: string, phrase: string) =>
      `test("${name}", () => {\n  expect(screen.getByText("${phrase}")).toBeTruthy();\n});\n`;
    const front = "cmd/evener-hub/frontend/src/";
    write(`${front}Empty.tsx`, `export const Empty = () => <p>Nothing to show</p>;\n`);
    write(`${front}Empty.test.tsx`, copyTest("shows nothing", "Nothing to show"));
    write(`${front}helper.d.ts`, `declare const x: "Declared phrase";\n`);
    write(`${front}Declared.test.tsx`, copyTest("shows the declared phrase", "Declared phrase"));
    write(`${front}testing/fake.ts`, `export const f = "Fake phrase";\n`);
    write(`${front}Fake.test.tsx`, copyTest("shows the fake phrase", "Fake phrase"));
    write(`${front}dev/Gallery.tsx`, `export const g = "Gallery phrase";\n`);
    write(`${front}Gallery.test.tsx`, copyTest("shows the gallery phrase", "Gallery phrase"));
    write("other/src/Other.tsx", `export const o = "Other tree phrase";\n`);
    write("other/src/Other.test.tsx", copyTest("shows the other phrase", "Other tree phrase"));
    write("mobile-native/node_modules/dep/index.ts", `export const d = "Vendored phrase";\n`);
    write("mobile-native/src/Dep.test.ts", copyTest("shows the vendored phrase", "Vendored phrase"));
    write("mobile/src/state/shared.ts", `export const s = "Shared mobile phrase";\n`);
    write("mobile/src/state/shared.test.ts", copyTest("shows the shared phrase", "Shared mobile phrase"));
    write("mobile-native/src/Shared.test.ts", copyTest("shows the shared phrase", "Shared mobile phrase"));
    write(
      "appwire-client/typescript/Untracked.test.ts",
      copyTest("shows the untracked phrase", "Shared mobile phrase"),
    );
    const git = (...args: string[]) => execFileSync("git", ["-C", root, ...args], { encoding: "utf8" });
    git("init", "-q");
    git("add", "--", ".");
    git("rm", "-q", "--cached", "--", "appwire-client/typescript/Untracked.test.ts");

    expect(scanRepo(root)).toEqual([
      { file: "cmd/evener-hub/frontend/src/Empty.test.tsx", test: "shows nothing", line: 1 },
      { file: "mobile-native/src/Shared.test.ts", test: "shows the shared phrase", line: 1 },
      { file: "mobile/src/state/shared.test.ts", test: "shows the shared phrase", line: 1 },
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
