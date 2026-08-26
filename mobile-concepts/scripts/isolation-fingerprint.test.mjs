import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  renameSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  fingerprintAndroid,
  fingerprintDirectory,
  fingerprintIosSimulator,
  fingerprintWorkspace,
} from "./isolation-fingerprint.mjs";

function temporaryDirectory(prefix) {
  return mkdtempSync(path.join(os.tmpdir(), prefix));
}

function createEquivalentTree(root, reverse = false) {
  mkdirSync(path.join(root, "nested"), { recursive: true });
  const files = [
    ["alpha.txt", "alpha"],
    [path.join("nested", "beta.txt"), "beta"],
  ];
  if (reverse) files.reverse();
  for (const [relative, content] of files) {
    writeFileSync(path.join(root, relative), content);
  }
}

test("directory fingerprint is stable across filesystem enumeration order", async () => {
  const first = temporaryDirectory("fingerprint-order-a-");
  const second = temporaryDirectory("fingerprint-order-b-");
  try {
    createEquivalentTree(first, false);
    createEquivalentTree(second, true);
    assert.deepEqual(
      await fingerprintDirectory(first),
      await fingerprintDirectory(second),
    );
  } finally {
    rmSync(first, { recursive: true, force: true });
    rmSync(second, { recursive: true, force: true });
  }
});

test("directory fingerprint changes on path, mode, size, or content change", async () => {
  const root = temporaryDirectory("fingerprint-mutations-");
  try {
    writeFileSync(path.join(root, "entry.txt"), "same");
    const baseline = await fingerprintDirectory(root);

    renameSync(path.join(root, "entry.txt"), path.join(root, "renamed.txt"));
    const pathChanged = await fingerprintDirectory(root);
    assert.notEqual(pathChanged.digest, baseline.digest, "path change");

    chmodSync(path.join(root, "renamed.txt"), 0o600);
    const modeChanged = await fingerprintDirectory(root);
    assert.notEqual(modeChanged.digest, pathChanged.digest, "mode change");

    writeFileSync(path.join(root, "renamed.txt"), "different-size");
    const sizeChanged = await fingerprintDirectory(root);
    assert.notEqual(sizeChanged.digest, modeChanged.digest, "size change");

    writeFileSync(path.join(root, "renamed.txt"), "DIFFERENT-SIZE");
    const contentChanged = await fingerprintDirectory(root);
    assert.equal(
      "DIFFERENT-SIZE".length,
      "different-size".length,
      "content mutation must preserve size",
    );
    assert.notEqual(
      contentChanged.digest,
      sizeChanged.digest,
      "content change",
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("directory fingerprint records symlinks without following them", async () => {
  const root = temporaryDirectory("fingerprint-symlink-root-");
  const outside = temporaryDirectory("fingerprint-symlink-outside-");
  try {
    const target = path.join(outside, "target.txt");
    writeFileSync(target, "first");
    symlinkSync(target, path.join(root, "link"));
    const before = await fingerprintDirectory(root);
    writeFileSync(target, "second and different");
    assert.deepEqual(await fingerprintDirectory(root), before);
  } finally {
    rmSync(root, { recursive: true, force: true });
    rmSync(outside, { recursive: true, force: true });
  }
});

test("workspace fingerprint includes tracked, staged, and untracked state but excludes ignored outputs", async () => {
  const repository = temporaryDirectory("fingerprint-git-");
  try {
    execFileSync("git", ["init", "-q"], { cwd: repository });
    execFileSync("git", ["config", "user.email", "contract@example.invalid"], {
      cwd: repository,
    });
    execFileSync("git", ["config", "user.name", "Contract Test"], {
      cwd: repository,
    });
    mkdirSync(path.join(repository, "mobile"));
    writeFileSync(path.join(repository, ".gitignore"), "mobile/ignored/\n");
    writeFileSync(path.join(repository, "mobile", "tracked.txt"), "baseline");
    execFileSync("git", ["add", ".gitignore", "mobile/tracked.txt"], {
      cwd: repository,
    });
    execFileSync("git", ["commit", "-qm", "fixture"], { cwd: repository });

    const baseline = await fingerprintWorkspace(repository, "mobile");
    writeFileSync(path.join(repository, "mobile", "tracked.txt"), "modified");
    const modified = await fingerprintWorkspace(repository, "mobile");
    assert.notEqual(modified.digest, baseline.digest, "tracked modification");

    execFileSync("git", ["add", "mobile/tracked.txt"], { cwd: repository });
    const staged = await fingerprintWorkspace(repository, "mobile");
    assert.notEqual(staged.digest, modified.digest, "staged state");

    writeFileSync(
      path.join(repository, "mobile", "untracked.txt"),
      "untracked",
    );
    const untracked = await fingerprintWorkspace(repository, "mobile");
    assert.notEqual(untracked.digest, staged.digest, "untracked file");

    mkdirSync(path.join(repository, "mobile", "ignored"));
    writeFileSync(
      path.join(repository, "mobile", "ignored", "output.bin"),
      "ignored",
    );
    assert.deepEqual(
      await fingerprintWorkspace(repository, "mobile"),
      untracked,
      "ignored output",
    );
  } finally {
    rmSync(repository, { recursive: true, force: true });
  }
});

test("missing iOS simulator app returns structured unavailable state", async () => {
  const result = await fingerprintIosSimulator(
    "SIMULATOR-UDID",
    "com.primeradiant.evener",
    {
      run: async () => ({ code: 2, stdout: "", stderr: "not installed" }),
    },
  );
  assert.deepEqual(result, {
    status: "unavailable",
    platform: "ios",
    identifier: "com.primeradiant.evener",
    reason: "production app container is absent or inaccessible",
  });
});

test("Android fingerprint uses only its serial-bound client and returns aggregate data", async () => {
  const calls = [];
  const adbClient = {
    run: async (args) => {
      calls.push(args);
      return args.at(-1).includes("sha256sum")
        ? { code: 0, stdout: `${"a".repeat(64)}  -\n`, stderr: "" }
        : { code: 0, stdout: "3\n", stderr: "" };
    },
  };
  assert.deepEqual(
    await fingerprintAndroid(adbClient, "com.primeradiant.evener"),
    {
      status: "available",
      platform: "android",
      identifier: "com.primeradiant.evener",
      digest: "a".repeat(64),
      files: 3,
    },
  );
  assert.equal(calls.length, 2);
  for (const args of calls) {
    assert.deepEqual(args.slice(0, 3), [
      "shell",
      "run-as",
      "com.primeradiant.evener",
    ]);
  }
});
