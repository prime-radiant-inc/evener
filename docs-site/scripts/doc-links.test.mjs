import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm, symlink } from "node:fs/promises";
import { dirname, join } from "node:path";
import os from "node:os";
import { checkDocLinks } from "./check.mjs";

async function fixture(t, files) {
  const root = await mkdtemp(join(process.env.EVENER_SCRATCH_DIR || os.tmpdir(), "doc-links-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const [file, text] of Object.entries(files)) {
    await mkdir(dirname(join(root, file)), { recursive: true });
    await writeFile(join(root, file), text);
  }
  return root;
}

test("accepts a resolving page link, fragment, anchor, image, and source line", async (t) => {
  const root = await fixture(t, {
    "README.md": "# Readme\n\n[guide](docs/page.md#install)\n",
    "docs/page.md": "# Page\n\n## Install\n\n<a id=\"legacy\"></a>\n\n[back](../README.md)\n[src](../src/a.go#L2)\n![shot](assets/shot.png)\n",
    "src/a.go": "package a\nfunc A() {}\n",
    "docs/assets/shot.png": "not a real png",
  });
  await checkDocLinks({ root });
});

test("resolves a percent-encoded filename", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": "# Page\n\n[two](two%20words.md)\n",
    "docs/two words.md": "# Two\n",
  });
  await checkDocLinks({ root });
});

test("rejects a same-page fragment with no matching heading", async (t) => {
  const root = await fixture(t, { "docs/page.md": "# Page\n\n[bad](#missing)\n" });
  await assert.rejects(checkDocLinks({ root }), /missing fragment #missing/);
});

test("rejects a reference-style link to a missing file", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": "# Page\n\n[bad][ref]\n\n[ref]: missing.md\n",
  });
  await assert.rejects(checkDocLinks({ root }), /missing target missing\.md/);
});

test("rejects a link whose title is present", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": '# Page\n\n[bad](missing.md "title")\n',
  });
  await assert.rejects(checkDocLinks({ root }), /missing target missing\.md/);
});

test("rejects a missing file target", async (t) => {
  const root = await fixture(t, { "docs/page.md": "# Page\n\n[gone](missing.md)\n" });
  await assert.rejects(checkDocLinks({ root }), /missing target missing\.md/);
});

test("rejects a missing fragment", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": "# Page\n\n[gone](other.md#absent)\n",
    "docs/other.md": "# Other\n",
  });
  await assert.rejects(checkDocLinks({ root }), /missing fragment other\.md#absent/);
});

test("rejects a reversed source range and a line past the end", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": "# Page\n\n[rev](../src/a.go#L9-L1)\n[far](../src/a.go#L9)\n",
    "src/a.go": "package a\n",
  });
  await assert.rejects(checkDocLinks({ root }), /line range L9-L1 outside src\/a\.go/);
});

test("rejects a path that climbs out of the repository after normalizing", async (t) => {
  const outside = await mkdtemp(join(os.tmpdir(), "outside-"));
  t.after(() => rm(outside, { recursive: true, force: true }));
  await writeFile(join(outside, "outside.md"), "# Outside\n");
  const root = await fixture(t, {
    "docs/page.md": `# Page\n\n[bad](../docs/../../${outside.split("/").pop()}/outside.md)\n`,
  });
  await assert.rejects(checkDocLinks({ root }), /target outside repository/);
});

test("rejects a link whose target is a symlink out of the repository", async (t) => {
  const outside = await mkdtemp(join(os.tmpdir(), "outside-"));
  t.after(() => rm(outside, { recursive: true, force: true }));
  await writeFile(join(outside, "secret.md"), "# Secret\n");
  const root = await fixture(t, { "docs/page.md": "# Page\n\n[bad](escape.md)\n" });
  await symlink(join(outside, "secret.md"), join(root, "docs/escape.md"));
  await assert.rejects(checkDocLinks({ root }), /target outside repository/);
});

test("ignores links inside code fences and inline code", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": "# Page\n\n```md\n[not a link](missing.md)\n```\n\n`[also not](missing.md)`\n",
  });
  await checkDocLinks({ root });
});

test("does not treat a dated design record as a current reference", async (t) => {
  const root = await fixture(t, {
    "docs/design/2026-01-01-plan.md": "# Plan\n\n[old](missing.md)\n",
    "docs/superpowers/plans/2026-01-01-plan.md": "# Plan\n\n[old](missing.md)\n",
  });
  await checkDocLinks({ root });
});

test("checks a maintained README outside docs and outside the archive", async (t) => {
  const root = await fixture(t, {
    "cmd/example/README.md": "# Example\n\n[gone](../missing.md)\n",
  });
  await assert.rejects(checkDocLinks({ root }), /cmd\/example\/README\.md: missing target/);
});

test("checks a living document inside an archive directory", async (t) => {
  const root = await fixture(t, {
    "docs/design/mobile/ios-build-distribution.md": "# Build\n\n[gone](missing.md)\n",
  });
  await assert.rejects(checkDocLinks({ root }), /ios-build-distribution\.md: missing target/);
});

test("checks an archive directory's own README index", async (t) => {
  const root = await fixture(t, {
    "docs/design/README.md": "# Design records\n\n[gone](missing.md)\n",
  });
  await assert.rejects(checkDocLinks({ root }), /docs\/design\/README\.md: missing target/);
});

test("refuses to read a source file that resolves outside the repository", async (t) => {
  const outside = await mkdtemp(join(os.tmpdir(), "outside-"));
  t.after(() => rm(outside, { recursive: true, force: true }));
  await writeFile(join(outside, "external.md"), "# Outside\n\n[gone](missing.md)\n");
  const root = await fixture(t, { "docs/page.md": "# Page\n" });
  await symlink(join(outside, "external.md"), join(root, "README.md"));
  // The external file's broken link must never be reported: the file itself is
  // outside the repository and is skipped rather than parsed.
  await checkDocLinks({ root });
});

test("checks a guide listed as an explicit file, not only its directory", async (t) => {
  const root = await fixture(t, {
    "tools/prompt-eval/README.md": "# Prompt eval\n\n[gone](missing.md)\n",
  });
  await assert.rejects(checkDocLinks({ root }), /tools\/prompt-eval\/README\.md: missing target/);
});

test("ignores a link inside an HTML comment", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": '# Page\n\n<!-- <a href="missing.md">missing</a> -->\n',
  });
  await checkDocLinks({ root });
});

test("decodes an HTML entity in an attribute", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": '# Page\n\n<a href="existing&#46;md">ok</a>\n',
    "docs/existing.md": "# Existing\n",
  });
  await checkDocLinks({ root });
});

test("rejects a single-quoted HTML anchor", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": "# Page\n\n<a href='missing.md'>x</a>\n",
  });
  await assert.rejects(checkDocLinks({ root }), /missing target missing\.md/);
});

test("rejects an unquoted HTML image source", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": "# Page\n\n<img src=missing.png>\n",
  });
  await assert.rejects(checkDocLinks({ root }), /missing target missing\.png/);
});
