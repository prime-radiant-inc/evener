// @vitest-environment node

// The ratchet behind docs/developing-evener/testing.md's "Copy Is Not a Test
// Oracle" for TypeScript: a test that exists only to check wording (its name
// says it shows, renders or says something, and its every check compares
// output with production copy) may not land unless allowlist.tsv names it with
// a reason. The allowlist should only shrink: an entry whose test is
// converted, deleted or renamed is stale and must go. Go tests have the same
// audit in copyonly_audit_test.go.
//
// Scanning every tracked TypeScript test file in one test took 10.51s in CI
// (issue #4608), over the per-test ceiling. So the audit is split: one case
// validates the allowlist's shape, one case pins the partition itself, and one
// case owns each slice of the tracked test files, checking both content rules
// for its slice — every copy-only test in it is allowlisted, and every
// allowlist row for its files still names a copy-only test. The slices
// partition the tracked test files exactly once and the shape case owns rows
// outside that partition, so the cases together enforce exactly what the single
// test did.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { openRepo } from "./copyonly";

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = execFileSync("git", ["-C", HERE, "rev-parse", "--show-toplevel"], { encoding: "utf8" }).trim();
const ALLOWLIST = relative(ROOT, join(HERE, "allowlist.tsv"));
const HEADER = "file\ttest\treason";

// One snapshot of the tracked TypeScript test files, sliced here. Each case scans
// about FILES_PER_SLICE files, chosen from the measurement on issue #4608: at
// ~100 files a case, every case scans in about a second against the 3s
// per-test ceiling; the one whole-repository case it replaced took 10.51s in CI
// and ~8s on a developer's machine.
// Slicing by count (not by tree) keeps the cases even, and a new file only
// shifts the case boundaries rather than creating a new case.
const FILES_PER_SLICE = 100;
const REPO = openRepo(ROOT);
const FILES = REPO.testFiles;
const SLICES_OF_FILES: string[][] = [];
for (let i = 0; i < FILES.length; i += FILES_PER_SLICE) {
  SLICES_OF_FILES.push(FILES.slice(i, i + FILES_PER_SLICE));
}
if (SLICES_OF_FILES.length === 0) SLICES_OF_FILES.push([]); // no tracked tests: still audit the allowlist
const SLICES = SLICES_OF_FILES.length;

/** The allowlist's file\ttest keys, in file order; the shape case validates the rows. */
function allowlistKeys(): string[] {
  const [, ...rows] = readFileSync(join(ROOT, ALLOWLIST), "utf8").split("\n");
  return rows
    .filter((row) => row.trim() !== "")
    .map((row) => {
      const [file = "", name = ""] = row.split("\t");
      return `${file}\t${name}`;
    });
}

/** The file a file\ttest key names. */
function keyFile(key: string): string {
  const tab = key.indexOf("\t");
  return tab < 0 ? key : key.slice(0, tab);
}

test("allowlist.tsv is well formed and names tracked TypeScript test files", () => {
  const [header, ...rows] = readFileSync(join(ROOT, ALLOWLIST), "utf8").split("\n");
  expect(header, `${ALLOWLIST} must start with the header`).toBe(HEADER);
  const tracked = new Set(FILES);
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
    // A row whose file is not a tracked TypeScript test file can never match a
    // slice's found set, so this case owns that half of the stale-row check.
    expect(
      tracked.has(cols[0] ?? ""),
      `${ALLOWLIST} line ${i + 2} names ${cols[0]}, which is not a tracked TypeScript test file; remove the stale entry`,
    ).toBe(true);
    keys.push(key);
  });
});

test("the slices cover every tracked TypeScript test file exactly once", () => {
  const seen = new Set<string>();
  for (const files of SLICES_OF_FILES) {
    for (const file of files) {
      expect(seen.has(file), `${file} is audited by more than one slice`).toBe(false);
      seen.add(file);
    }
  }
  // The content cases below are only complete while the slices are a partition
  // of FILES: a slice that skipped a file would narrow the audit silently.
  expect(FILES.length, "no tracked TypeScript test files: the audit would check nothing").toBeGreaterThan(0);
  expect([...seen].sort()).toEqual([...FILES].sort());
});

SLICES_OF_FILES.forEach((files, index) => {
  test(`no new copy-only TypeScript tests: slice ${index + 1}/${SLICES}`, () => {
    const listed = allowlistKeys();
    const inSlice = new Set(files);
    const listedHere = listed.filter((key) => inSlice.has(keyFile(key)));
    const found = REPO.scan(files);
    const foundKeys = new Set(found.map((t) => `${t.file}\t${t.test}`));
    const problems = [
      ...found
        .filter((t) => !listed.includes(`${t.file}\t${t.test}`))
        .map(
          (t) =>
            `${t.file}:${t.line}: test ${JSON.stringify(t.test)} only checks wording. Assert the behavior the copy is for instead ` +
            `(docs/developing-evener/testing.md, "Copy Is Not a Test Oracle"), or add it to ${ALLOWLIST} with the reason it is kept`,
        ),
      ...listedHere
        .filter((key) => !foundKeys.has(key))
        .map(
          (key) =>
            `${ALLOWLIST} lists ${key.replace("\t", " ")}, which is no longer a copy-only test; remove the stale entry`,
        ),
    ];
    expect(problems).toEqual([]);
  }, 120_000);
});
