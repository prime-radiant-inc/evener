import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  lstat,
  opendir,
  readFile,
  readlink,
  realpath,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const EMPTY_DIGEST = createHash("sha256").digest("hex");
const HEX_DIGEST = /^[0-9a-f]{64}$/;

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

async function digestFile(file) {
  return sha256(await readFile(file));
}

function kindOf(stat) {
  if (stat.isFile()) return "file";
  if (stat.isDirectory()) return "directory";
  if (stat.isSymbolicLink()) return "symlink";
  if (stat.isSocket()) return "socket";
  if (stat.isFIFO()) return "fifo";
  if (stat.isBlockDevice()) return "block-device";
  if (stat.isCharacterDevice()) return "character-device";
  return "other";
}

async function describePath(root, relative) {
  const absolute = path.join(root, ...relative.split("/"));
  let stat;
  try {
    stat = await lstat(absolute);
  } catch (error) {
    if (error.code === "ENOENT") {
      return {
        path: relative,
        kind: "missing",
        mode: "0",
        size: 0,
        content: EMPTY_DIGEST,
      };
    }
    throw error;
  }
  const kind = kindOf(stat);
  let content = EMPTY_DIGEST;
  if (kind === "file") content = await digestFile(absolute);
  if (kind === "symlink") content = sha256(await readlink(absolute));
  return {
    path: relative,
    kind,
    mode: (stat.mode & 0o7777).toString(8),
    size: stat.size,
    content,
  };
}

async function listDirectoryPaths(root, relative = "") {
  const directory = path.join(root, ...relative.split("/").filter(Boolean));
  const entries = [];
  const handle = await opendir(directory);
  for await (const entry of handle) {
    const child = relative ? `${relative}/${entry.name}` : entry.name;
    entries.push(child);
    if (entry.isDirectory()) {
      entries.push(...(await listDirectoryPaths(root, child)));
    }
  }
  return entries;
}

function aggregate(entries, extra = {}) {
  const ordered = [...entries].sort((left, right) =>
    left.path < right.path ? -1 : left.path > right.path ? 1 : 0,
  );
  return {
    digest: sha256(`${JSON.stringify({ entries: ordered, ...extra })}\n`),
    files: ordered.length,
  };
}

/**
 * Fingerprint a resolved application data container without following symlinks.
 * Names and contents are incorporated into the digest but never printed.
 */
export async function fingerprintDirectory(root) {
  const resolved = await realpath(root);
  const relativePaths = await listDirectoryPaths(resolved);
  const entries = await Promise.all(
    relativePaths.map((relative) => describePath(resolved, relative)),
  );
  return aggregate(entries);
}

function runProcess(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => stdout.push(chunk));
    child.stderr.on("data", (chunk) => stderr.push(chunk));
    child.once("error", reject);
    child.once("close", (code, signal) =>
      resolve({
        code: code ?? 1,
        signal,
        stdout: Buffer.concat(stdout).toString("utf8"),
        stderr: Buffer.concat(stderr).toString("utf8"),
      }),
    );
  });
}

async function git(repository, args) {
  const result = await runProcess("git", args, { cwd: repository });
  if (result.code !== 0) {
    throw new Error(
      `git ${args[0]} failed (${result.code}): ${result.stderr.trim()}`,
    );
  }
  return result.stdout;
}

function nulRecords(value) {
  return value.split("\0").filter(Boolean);
}

async function buildWorkspaceManifest(repository, workspace) {
  const root = await realpath(repository);
  const normalized = workspace.replaceAll("\\", "/").replace(/^\.\//, "");
  if (
    !normalized ||
    path.posix.isAbsolute(normalized) ||
    normalized === ".." ||
    normalized.startsWith("../")
  ) {
    throw new Error("workspace must be a repository-relative path");
  }
  const listed = nulRecords(
    await git(root, [
      "ls-files",
      "-co",
      "--exclude-standard",
      "-z",
      "--",
      normalized,
    ]),
  );
  const uniquePaths = [...new Set(listed)].sort();
  const entries = await Promise.all(
    uniquePaths.map((relative) => describePath(root, relative)),
  );
  const porcelain = nulRecords(
    await git(root, [
      "status",
      "--porcelain=v2",
      "--untracked-files=all",
      "-z",
      "--",
      normalized,
    ]),
  );
  const cachedIndex = nulRecords(
    await git(root, ["diff", "--cached", "--raw", "-z", "--", normalized]),
  );
  const summary = aggregate(entries, { porcelain, cachedIndex });
  return {
    version: 1,
    workspace: normalized,
    entries: [...entries].sort((left, right) =>
      left.path < right.path ? -1 : left.path > right.path ? 1 : 0,
    ),
    porcelain,
    cachedIndex,
    ...summary,
  };
}

/** Fingerprint tracked/nonignored source plus path-scoped worktree and index state. */
export async function fingerprintWorkspace(repository, workspace) {
  const manifest = await buildWorkspaceManifest(repository, workspace);
  return { digest: manifest.digest, files: manifest.files };
}

/** Resolve and fingerprint an installed iOS simulator data container. */
export async function fingerprintIosSimulator(udid, bundleId, tools = {}) {
  const run = tools.run ?? runProcess;
  const result = await run("xcrun", [
    "simctl",
    "get_app_container",
    udid,
    bundleId,
    "data",
  ]);
  if (result.code !== 0 || !result.stdout.trim()) {
    return {
      status: "unavailable",
      platform: "ios",
      identifier: bundleId,
      reason: "production app container is absent or inaccessible",
    };
  }
  try {
    const fingerprint = await fingerprintDirectory(result.stdout.trim());
    return {
      status: "available",
      platform: "ios",
      identifier: bundleId,
      ...fingerprint,
    };
  } catch {
    return {
      status: "unavailable",
      platform: "ios",
      identifier: bundleId,
      reason: "production app container is absent or inaccessible",
    };
  }
}

const ANDROID_HASH_SCRIPT = String.raw`set -eu
find . -xdev -mindepth 1 -exec sh -c '
  for p do
    if [ -L "$p" ]; then kind=symlink; content=$(readlink "$p" | sha256sum | cut -d" " -f1)
    elif [ -f "$p" ]; then kind=file; content=$(sha256sum "$p" | cut -d" " -f1)
    elif [ -d "$p" ]; then kind=directory; content=${EMPTY_DIGEST}
    else kind=other; content=${EMPTY_DIGEST}
    fi
    mode=$(stat -c %a "$p")
    size=$(stat -c %s "$p")
    printf "%s\\0%s\\0%s\\0%s\\0%s\\0" "$p" "$kind" "$mode" "$size" "$content"
  done
' sh {} + | sort -z | sha256sum`;
const ANDROID_COUNT_SCRIPT =
  "set -eu; find . -xdev -mindepth 1 -print0 | tr -cd '\\000' | wc -c";

function validPackageName(packageName) {
  return /^(?:[A-Za-z][A-Za-z0-9_]*\.)+[A-Za-z][A-Za-z0-9_]*$/.test(
    packageName,
  );
}

/**
 * Fingerprint Android app-private data with the supplied validated serial-bound
 * adb client. The device streams only aggregate digest/count output.
 */
export async function fingerprintAndroid(adbClient, packageName) {
  if (!adbClient || typeof adbClient.run !== "function") {
    throw new TypeError("adbClient.run is required");
  }
  if (!validPackageName(packageName))
    throw new Error("invalid Android package name");
  const invoke = (script) =>
    adbClient.run(["shell", "run-as", packageName, "sh", "-c", script]);
  const hashed = await invoke(ANDROID_HASH_SCRIPT);
  if (hashed.code !== 0) {
    return {
      status: "unavailable",
      platform: "android",
      identifier: packageName,
      reason: "production app container is absent or inaccessible",
    };
  }
  const digest = hashed.stdout.trim().split(/\s+/, 1)[0]?.toLowerCase();
  if (!HEX_DIGEST.test(digest)) {
    throw new Error("Android fingerprint returned an invalid digest");
  }
  const counted = await invoke(ANDROID_COUNT_SCRIPT);
  const files = Number.parseInt(counted.stdout.trim(), 10);
  if (counted.code !== 0 || !Number.isSafeInteger(files) || files < 0) {
    return {
      status: "unavailable",
      platform: "android",
      identifier: packageName,
      reason: "production app container is absent or inaccessible",
    };
  }
  return {
    status: "available",
    platform: "android",
    identifier: packageName,
    digest,
    files,
  };
}

function parseArguments(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--workspace") options.workspace = argv[++index];
    else if (argument === "--output") options.output = argv[++index];
    else if (argument === "--ios-simulator")
      options.iosSimulator = argv[++index];
    else if (argument === "--bundle-id") options.bundleId = argv[++index];
    else throw new Error(`unknown argument: ${argument}`);
  }
  return options;
}

async function runCli(argv) {
  const options = parseArguments(argv);
  let result;
  if (options.workspace) {
    const manifest = await buildWorkspaceManifest(
      process.cwd(),
      options.workspace,
    );
    const output = options.output
      ? path.resolve(options.output)
      : path.join(
          process.env.EVENER_SCRATCH_DIR ?? os.tmpdir(),
          `isolation-workspace-${process.pid}.json`,
        );
    await writeFile(output, `${JSON.stringify(manifest, null, 2)}\n`, {
      mode: 0o600,
    });
    result = {
      status: "available",
      platform: "workspace",
      digest: manifest.digest,
      files: manifest.files,
      output,
    };
  } else if (options.iosSimulator && options.bundleId) {
    result = await fingerprintIosSimulator(
      options.iosSimulator,
      options.bundleId,
    );
    if (options.output) {
      const output = path.resolve(options.output);
      await writeFile(output, `${JSON.stringify(result, null, 2)}\n`, {
        mode: 0o600,
      });
      result = { ...result, output };
    }
  } else {
    throw new Error(
      "usage: isolation-fingerprint.mjs --workspace PATH [--output FILE] | --ios-simulator UDID --bundle-id ID [--output FILE]",
    );
  }
  console.log(JSON.stringify(result));
  if (result.status === "unavailable") process.exitCode = 2;
}

const isMain =
  process.argv[1] &&
  path.resolve(process.argv[1]) ===
    path.resolve(fileURLToPath(import.meta.url));
if (isMain) {
  runCli(process.argv.slice(2)).catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
