// Loaded inventories retain their healthy components beside load diagnostics.
// Target and request failures belong to the detail body that hosts this block.
// This block never fetches.

import type { PluginComponents } from "@evener/appwire-client";
import type { PluginComponentsEntry } from "@evener/appwire-client/state/extensions";
import type { ReactElement } from "react";
import { useRef, useState } from "react";
import { Badge, Button, Eyebrow, InlineNotice, useFocusRehome } from "../../widgets";
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
  // Expansion removes its own focused control at this row's commit, with no
  // rerender above it - the row itself is the catch, so the keyboard stays
  // put instead of dropping to <body>.
  const focusRoot = useRef<HTMLDivElement>(null);
  useFocusRehome(focusRoot);
  const visible = showAll ? names : names.slice(0, MAX_NAMES);
  const rest = names.length - visible.length;
  return (
    <div ref={focusRoot} className={CLASS.row} data-kind={kind} tabIndex={-1}>
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
  const present = CONTENT_KINDS.map((kind) => {
    const names = kind.names(components);
    return { kind, names, count: kind.key === "hooks" ? hookTotal(components) : names.length };
  }).filter(({ names }) => names.length > 0);
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
          {present.map(({ kind, names, count }) => (
            <ContentsRow key={kind.key} kind={kind.key} label={kind.label} names={names} count={count} />
          ))}
        </div>
      )}
    </div>
  );
}

interface PluginContentsBlockProps {
  entry: PluginComponentsEntry | undefined;
  onRetry: () => void;
}

export function PluginContentsBlock(props: PluginContentsBlockProps): ReactElement {
  // A retry swaps Retry for loading content at this block's own commit, with
  // no rerender above it - the block wrapper is the catch, so callers need no
  // body-level rerender to keep the keyboard.
  const focusRoot = useRef<HTMLDivElement>(null);
  useFocusRehome(focusRoot);
  return (
    <div ref={focusRoot} tabIndex={-1}>
      <ContentsBlockBody {...props} />
    </div>
  );
}

function ContentsBlockBody({ entry, onRetry }: PluginContentsBlockProps): ReactElement | null {
  if (entry === undefined || entry.status === "loading") return null;
  if (entry.status === "loaded") return <PluginContents components={entry.components} />;
  if (entry.source === "target") {
    return (
      <InlineNotice tone="danger" data-reason="plugin-broken">
        {entry.error}
      </InlineNotice>
    );
  }
  return (
    <InlineNotice
      tone="neutral"
      data-reason="components-degraded"
      action={
        <Button size="sm" type="button" onClick={onRetry}>
          Retry
        </Button>
      }
    >
      {entry.error}
    </InlineNotice>
  );
}
