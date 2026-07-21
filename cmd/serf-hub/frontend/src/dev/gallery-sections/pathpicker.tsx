import { useState } from "react";
import { PathPicker } from "../../widgets/pathpicker";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./pathpicker.module.css";

// A tiny in-memory tree, standing in for the caller-supplied RPC lister
// this widget deliberately has no knowledge of (see PathPicker's own doc
// comment: "this widget stays wire-free").
const TREE: Record<string, string[]> = {
  "/opt": ["/opt/plugins", "/opt/skills"],
  "/opt/plugins": ["/opt/plugins/serf-lint", "/opt/plugins/serf-format"],
  "/opt/skills": [],
};

async function listChildren(path: string): Promise<string[]> {
  await new Promise((resolve) => setTimeout(resolve, 150));
  return TREE[path] ?? [];
}

function LivePathPicker() {
  const [value, setValue] = useState("/opt");
  return (
    <div className={styles.frame}>
      <PathPicker
        id="gallery-pathpicker"
        value={value}
        onChange={setValue}
        listChildren={listChildren}
        placeholder="/opt/plugins"
      />
    </div>
  );
}

export default function PathPickerGallerySection() {
  return (
    <section>
      <h2>PathPicker</h2>
      <p className={styles.hint}>
        Type a path, or click the folder button to browse. Browsing never commits until "Use this folder".
      </p>
      <ThemeFlip>
        <LivePathPicker />
      </ThemeFlip>
    </section>
  );
}
