// @vitest-environment node

// The ratchet behind docs/developing-evener/testing.md's "Copy Is Not a Test
// Oracle" for TypeScript: a test that exists only to check wording (its name
// says it shows, renders or says something, and its every check compares
// output with production copy) may not land unless allowlist.tsv names it with
// a reason. The allowlist should only shrink: an entry whose test is
// converted, deleted or renamed is stale and must go. Go tests have the same
// audit in copyonly_audit_test.go.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { scanRepo } from "./copyonly";

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = execFileSync("git", ["-C", HERE, "rev-parse", "--show-toplevel"], { encoding: "utf8" }).trim();
const ALLOWLIST = relative(ROOT, join(HERE, "allowlist.tsv"));
const HEADER = "file\ttest\treason";

/** The allowlist's file\ttest keys; every row needs a reason, and a test may be listed once. */
function allowlistKeys(): string[] {
  const [header, ...rows] = readFileSync(join(ROOT, ALLOWLIST), "utf8").split("\n");
  expect(header, `${ALLOWLIST} must start with the header`).toBe(HEADER);
  const keys: string[] = [];
  rows.forEach((row, i) => {
    if (row.trim() === "") return;
    const cols = row.split("\t");
    expect(
      cols.length === 3 && cols[2]?.trim() !== "",
      `${ALLOWLIST} line ${i + 2}: want file, test and a reason`,
    ).toBe(true);
    const key = `${cols[0]}\t${cols[1]}`;
    expect(keys.includes(key), `${ALLOWLIST} line ${i + 2} lists a test twice`).toBe(false);
    keys.push(key);
  });
  return keys;
}

test("no new copy-only TypeScript tests", () => {
  const listed = allowlistKeys();
  const targets = scanRepo(ROOT);
  const found = new Set(targets.map((t) => `${t.file}\t${t.test}`));
  const problems = [
    ...targets
      .filter((t) => !listed.includes(`${t.file}\t${t.test}`))
      .map(
        (t) =>
          `${t.file}:${t.line}: test ${JSON.stringify(t.test)} only checks wording. Assert the behavior the copy is for instead ` +
          `(docs/developing-evener/testing.md, "Copy Is Not a Test Oracle"), or add it to ${ALLOWLIST} with the reason it is kept`,
      ),
    ...listed
      .filter((key) => !found.has(key))
      .map(
        (key) =>
          `${ALLOWLIST} lists ${key.replace("\t", " ")}, which is no longer a copy-only test; remove the stale entry`,
      ),
  ];
  expect(problems).toEqual([]);
}, 120_000);
