import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
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

test("rejects a source line past the end of its file", async (t) => {
  const root = await fixture(t, {
    "docs/page.md": "# Page\n\n[src](../src/a.go#L9)\n",
    "src/a.go": "package a\n",
  });
  await assert.rejects(checkDocLinks({ root }), /line 9 beyond src\/a\.go/);
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
