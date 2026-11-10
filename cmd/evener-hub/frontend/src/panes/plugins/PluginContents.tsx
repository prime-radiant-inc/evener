// Loaded inventories retain their healthy components beside load diagnostics.
// Target and request failures belong to the sheet. This block never fetches.

import type { PluginComponents } from "@evener/appwire-client";
import type { ReactElement } from "react";
import { useState } from "react";
import { Badge, Button, Eyebrow, InlineNotice } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./pluginContents.module.css";
import { hookTotal } from "./pluginCounts";

const CLASS = {
  contents: requireClass(styles.contents, "pluginContents.module.css", "contents"),
  list: requireClass(styles.list, "pluginContents.module.css", "list"),
  row: requireClass(styles.row, "pluginContents.module.css", "row"),
  kindLabel: requireClass(styles.kindLabel, "pluginContents.module.css", "kindLabel"),
  names: requireClass(styles.names, "pluginContents.module.css", "names"),
  manifestOnly: requireClass(styles.manifestOnly, "pluginContents.module.css", "manifestOnly"),
};

// Names shown before the expansion affordance; the rest are one click away.
const MAX_NAMES = 8;

const CONTENT_KINDS: readonly { key: string; label: string; names: (components: PluginComponents) => string[] }[] = [
  { key: "skills", label: "Skills", names: (c) => c.skills ?? [] },
  { key: "agents", label: "Agents", names: (c) => c.agents ?? [] },
  { key: "commands", label: "Commands", names: (c) => c.commands ?? [] },
  {
    key: "hooks",
    label: "Hooks",
    names: (c) => (c.hooks ?? []).map(({ event, count }) => `${event} (${count})`),
  },
  { key: "mcpServers", label: "MCP servers", names: (c) => c.mcpServers ?? [] },
];

function ContentsRow({
  kind,
  label,
  names,
  count,
}: {
  kind: string;
  label: string;
  names: string[];
  count: number;
}): ReactElement {
  const [showAll, setShowAll] = useState(false);
  const visible = showAll ? names : names.slice(0, MAX_NAMES);
  const rest = names.length - visible.length;
  return (
    <div className={CLASS.row} data-kind={kind}>
      <span className={CLASS.kindLabel}>{label}</span>
      <Badge count={count} />
      <span className={CLASS.names}>
        {visible.join(", ")}
        {rest > 0 && <Button variant="link" onClick={() => setShowAll(true)}>{` +${rest} more`}</Button>}
      </span>
    </div>
  );
}

export function PluginContents({ components }: { components: PluginComponents }): ReactElement {
  const present = CONTENT_KINDS.filter((kind) => kind.names(components).length > 0);
  return (
    <div className={CLASS.contents} data-kind="plugin-contents">
      <Eyebrow as="h3">Contents</Eyebrow>
      {(components.warnings?.length ?? 0) > 0 && (
        <InlineNotice tone="danger" data-reason="load-warnings">
          {components.warnings?.join(" · ")}
        </InlineNotice>
      )}
      {present.length === 0 ? (
        <p className={CLASS.manifestOnly} data-reason="manifest-only">
          Manifest only - no skills, agents, commands, hooks, or MCP servers.
        </p>
      ) : (
        <div className={CLASS.list}>
          {present.map((kind) => (
            <ContentsRow
              key={kind.key}
              kind={kind.key}
              label={kind.label}
              names={kind.names(components)}
              count={kind.key === "hooks" ? hookTotal(components) : kind.names(components).length}
            />
          ))}
        </div>
      )}
    </div>
  );
}
