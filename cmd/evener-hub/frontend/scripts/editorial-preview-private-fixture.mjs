import { constants } from "node:fs";
import {
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  readlink,
  realpath,
  rm,
  symlink,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const omittedFrontendEntries = new Set([".vite-cache", "coverage", "dist", "node_modules"]);
const omittedPackageEntries = new Set(["dist", "node_modules"]);

function isInside(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative === "" || (!relative.startsWith(`..${path.sep}`) && relative !== ".." && !path.isAbsolute(relative));
}

async function copyTree(source, destination, { omittedRootEntries = new Set(), dependencyRoot } = {}) {
  async function copyDirectory(sourceDirectory, destinationDirectory, relativeDirectory) {
    await mkdir(destinationDirectory, { recursive: true });
    const entries = await readdir(sourceDirectory, { withFileTypes: true });
    for (const entry of entries) {
      if (relativeDirectory === "" && omittedRootEntries.has(entry.name)) continue;
      const relative = path.join(relativeDirectory, entry.name);
      const sourcePath = path.join(sourceDirectory, entry.name);
      const destinationPath = path.join(destinationDirectory, entry.name);
      if (entry.isDirectory()) {
        await copyDirectory(sourcePath, destinationPath, relative);
        continue;
      }
      if (entry.isSymbolicLink()) {
        if (!dependencyRoot || !relative.split(path.sep).includes(".bin")) {
          throw new Error(`private editorial fixture refuses non-.bin symlink: ${relative}`);
        }
        const target = await readlink(sourcePath);
        const resolvedTarget = path.resolve(path.dirname(sourcePath), target);
        const realTarget = await realpath(sourcePath);
        if (path.isAbsolute(target) || !isInside(dependencyRoot, resolvedTarget) || !isInside(dependencyRoot, realTarget)) {
          throw new Error(`private editorial fixture refuses escaping dependency symlink: ${relative} -> ${target}`);
        }
        await symlink(target, destinationPath);
        continue;
      }
      if (!entry.isFile()) throw new Error(`private editorial fixture refuses special file: ${relative}`);
      // COPYFILE_FICLONE requests an independent copy-on-write file where the
      // filesystem supports it and falls back to an ordinary copy elsewhere.
      await copyFile(sourcePath, destinationPath, constants.COPYFILE_FICLONE);
    }
  }

  await copyDirectory(source, destination, "");
}

export async function createPrivateEditorialPreviewFixture(sourceFrontend) {
  const sourceAppwire = path.resolve(sourceFrontend, "../../../appwire-client/typescript");
  const sourceNodeModules = await realpath(path.join(sourceFrontend, "node_modules"));
  const [frontendLock, installLock] = await Promise.all([
    readFile(path.join(sourceFrontend, "package-lock.json")),
    readFile(path.join(path.dirname(sourceNodeModules), "package-lock.json")),
  ]);
  if (!frontendLock.equals(installLock)) {
    throw new Error("private editorial fixture requires node_modules built from this frontend package-lock.json");
  }

  const root = await realpath(await mkdtemp(path.join(os.tmpdir(), "editorial-private-fixture-")));
  const privateRepo = path.join(root, "private-repo");
  const frontend = path.join(privateRepo, "cmd/evener-hub/frontend");
  const appwirePackage = path.join(privateRepo, "appwire-client/typescript");
  const sharedRepo = path.join(root, "shared-refusal-repo");
  const sharedFrontend = path.join(sharedRepo, "cmd/evener-hub/frontend");
  const sharedAppwire = path.join(sharedRepo, "appwire-client/typescript");
  try {
    await copyTree(sourceFrontend, frontend, { omittedRootEntries: omittedFrontendEntries });
    await copyTree(sourceAppwire, appwirePackage, { omittedRootEntries: omittedPackageEntries });
    await copyTree(sourceFrontend, sharedFrontend, { omittedRootEntries: omittedFrontendEntries });
    await copyTree(sourceAppwire, sharedAppwire, { omittedRootEntries: omittedPackageEntries });
    await copyTree(sourceNodeModules, path.join(frontend, "node_modules"), { dependencyRoot: sourceNodeModules });
    await symlink(path.join(frontend, "node_modules"), path.join(sharedFrontend, "node_modules"));
  } catch (error) {
    await rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
    throw error;
  }

  return {
    root,
    frontend,
    appwirePackage,
    configFile: path.join(frontend, "scripts/editorial-preview.vite.config.mjs"),
    sharedFrontend,
    sharedConfigFile: path.join(sharedFrontend, "scripts/editorial-preview.vite.config.mjs"),
    sourceNodeModules,
    cleanup: () => rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }),
  };
}
