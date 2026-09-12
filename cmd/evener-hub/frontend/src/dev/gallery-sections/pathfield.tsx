import { useState } from "react";
import { requireClass } from "../../widgets/internal/requireClass";
import { PathField, type PathFieldKind } from "../../widgets/pathfield";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./pathfield.module.css";

const CLASS = {
  hint: requireClass(styles.hint, "pathfield.module.css", "hint"),
  frame: requireClass(styles.frame, "pathfield.module.css", "frame"),
};

// A tiny in-memory tree standing in for evener/paths/complete, which this widget
// deliberately knows nothing about (see PathField's own doc comment: the caller
// injects `complete`). Keyed by the listing prefix the widget sends - the
// trailing-slash form that means "this directory's children" - and directory
// entries carry a trailing slash, the one bit the wire adds in includeFiles
// mode so the widget can tell a folder from a file.
const TREE: Record<string, string[]> = {
  "/opt/": ["/opt/plugins/", "/opt/skills/", "/opt/evener.toml"],
  "/opt/plugins/": ["/opt/plugins/evener-lint/", "/opt/plugins/evener-format/"],
  "/opt/skills/": [],
  "/tmp/": ["/tmp/traces/", "/tmp/atif.json"],
  "/tmp/traces/": ["/tmp/traces/session.jsonl"],
};

/** Mirrors the hub's two modes: a dirs-only response is unsuffixed, and a
 * files-included one keeps the directory slashes. */
async function complete(prefix: string, includeFiles: boolean): Promise<string[]> {
  await new Promise((resolve) => setTimeout(resolve, 150));
  const entries = TREE[prefix] ?? [];
  if (!includeFiles) return entries.filter((entry) => entry.endsWith("/")).map((entry) => entry.replace(/\/+$/, ""));
  return entries;
}

async function listRecents(): Promise<string[]> {
  return ["/opt/plugins/evener-lint", "/opt/skills"];
}

export function LivePathField({
  kind,
  initial,
  withRecents = false,
}: {
  kind: PathFieldKind;
  initial: string;
  withRecents?: boolean;
}) {
  const [value, setValue] = useState(initial);
  return (
    <div className={CLASS.frame}>
      <PathField
        id={`gallery-pathfield-${kind}${withRecents ? "-recents" : ""}`}
        value={value}
        onChange={setValue}
        kind={kind}
        directory={{
          validatePath: async (path) => {
            const canonical = path === "~" ? "/opt" : path;
            return { valid: `${canonical}/` in TREE, path: canonical, error: "Directory does not exist" };
          },
          createDirectory: async (path) => {
            const parent = path.slice(0, path.lastIndexOf("/") + 1);
            if (`${path}/` in TREE) throw new Error("Directory already exists");
            TREE[`${path}/`] = [];
            TREE[parent]?.push(`${path}/`);
          },
        }}
        complete={complete}
        listRecents={withRecents ? listRecents : undefined}
      />
    </div>
  );
}

export default function PathFieldGallerySection() {
  return (
    <section>
      <h2>PathField</h2>
      <p className={CLASS.hint}>
        Directory fields share the responsive DirectoryPicker: browse, create a folder, then confirm with Use this
        folder. Cancel preserves the selected directory. File and output-file fields use file completion and accept
        literal paths.
      </p>
      <ThemeFlip>
        <LivePathField kind="file" initial="/opt/evener.toml" />
        <LivePathField kind="outputFile" initial="/tmp/atif.json" />
      </ThemeFlip>
    </section>
  );
}
