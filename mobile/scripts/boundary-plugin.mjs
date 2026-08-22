import path from "node:path";

function sourcePath(id) {
  return path.resolve(id.split("?", 1)[0]);
}

function isWithin(candidate, parent) {
  const relative = path.relative(parent, candidate);
  return relative === "" || (!relative.startsWith(`..${path.sep}`) && relative !== ".." && !path.isAbsolute(relative));
}

function repositoryRelative(root, file) {
  return path.relative(root, file).split(path.sep).join("/");
}

export function assertAllowedModule(id, root, allowlist) {
  if (id.startsWith("\0")) {
    return;
  }

  const resolved = sourcePath(id);
  const frontendSourceRoot = path.join(root, "cmd/evener-hub/frontend/src");
  if (!isWithin(resolved, frontendSourceRoot)) {
    return;
  }

  if (!allowlist.has(repositoryRelative(root, resolved))) {
    throw new Error(`Mobile renderer may not import Hub frontend module: ${resolved}`);
  }
}

export function mobileRendererBoundary({ root, allowlist, rendererSourceRoot }) {
  function assertRendererEntries(context) {
    for (const id of context.getModuleIds()) {
      const moduleInfo = context.getModuleInfo(id);
      const resolved = sourcePath(id);
      if (
        moduleInfo?.isEntry &&
        path.extname(resolved) !== ".html" &&
        !id.startsWith("\0") &&
        !isWithin(resolved, rendererSourceRoot)
      ) {
        throw new Error(`Mobile renderer entry must resolve below mobile/src: ${resolved}`);
      }
    }
  }

  return {
    name: "mobile-renderer-boundary",
    load(id) {
      assertAllowedModule(id, root, allowlist);
      return null;
    },
    buildEnd() {
      for (const id of this.getModuleIds()) {
        assertAllowedModule(id, root, allowlist);
      }
      assertRendererEntries(this);
    },
  };
}
