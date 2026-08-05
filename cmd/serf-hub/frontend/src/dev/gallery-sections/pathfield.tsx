import { useState } from "react";
import { requireClass } from "../../widgets/internal/requireClass";
import { PathField, type PathFieldKind } from "../../widgets/pathfield";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./pathfield.module.css";

const CLASS = {
  hint: requireClass(styles.hint, "pathfield.module.css", "hint"),
  frame: requireClass(styles.frame, "pathfield.module.css", "frame"),
};

// A tiny in-memory tree standing in for serf/paths/complete, which this widget
// deliberately knows nothing about (see PathField's own doc comment: the caller
// injects `complete`). Keyed by the listing prefix the widget sends - the
// trailing-slash form that means "this directory's children" - and directory
// entries carry a trailing slash, the one bit the wire adds in includeFiles
// mode so the widget can tell a folder from a file.
const TREE: Record<string, string[]> = {
  "/opt/": ["/opt/plugins/", "/opt/skills/", "/opt/serf.toml"],
  "/opt/plugins/": ["/opt/plugins/serf-lint/", "/opt/plugins/serf-format/"],
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
  return ["/opt/plugins/serf-lint", "/opt/skills"];
}

function LivePathField({
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
        Click the field to browse. A directory row descends AND becomes the value, so there is nothing to commit; a file
        row commits and closes. Three kinds: dir (folders only), file, and outputFile (name a file that need not exist
        yet).
      </p>
      <ThemeFlip>
        <LivePathField kind="dir" initial="/opt" withRecents />
        <LivePathField kind="file" initial="/opt/serf.toml" />
        <LivePathField kind="outputFile" initial="/tmp/atif.json" />
        <LivePathField kind="dir" initial="" />
      </ThemeFlip>
    </section>
  );
}
