import type { SessionURL, ThreadModel } from "@evener/appwire-client";
import { fileURLToPath } from "@evener/appwire-client/docContent";
import { type JSX, useMemo, useState } from "react";
import { EmptyState, Eyebrow, Input, isWebHref, SegmentedControl } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { FileOpenBesideButton } from "../transcript/fileOpenBeside";
import { isFileHref } from "./NotesPanel";
import styles from "./sessionlinkssection.module.css";

type LinksOrderMode = "recent" | "grouped";
type LinkCategory = "Artifacts & Docs" | "Pull Requests" | "Issues" | "Local files" | "Other links";

const CATEGORY_ORDER: readonly LinkCategory[] = [
  "Artifacts & Docs",
  "Pull Requests",
  "Issues",
  "Local files",
  "Other links",
];

const MODE_OPTIONS: readonly { value: LinksOrderMode; label: string }[] = [
  { value: "recent", label: "Recent" },
  { value: "grouped", label: "Grouped" },
];

const CLASS = {
  root: requireClass(styles.root, "sessionlinkssection.module.css", "root"),
  toolbar: requireClass(styles.toolbar, "sessionlinkssection.module.css", "toolbar"),
  filterWrap: requireClass(styles.filterWrap, "sessionlinkssection.module.css", "filterWrap"),
  groups: requireClass(styles.groups, "sessionlinkssection.module.css", "groups"),
  group: requireClass(styles.group, "sessionlinkssection.module.css", "group"),
  groupHeader: requireClass(styles.groupHeader, "sessionlinkssection.module.css", "groupHeader"),
  groupCount: requireClass(styles.groupCount, "sessionlinkssection.module.css", "groupCount"),
  list: requireClass(styles.list, "sessionlinkssection.module.css", "list"),
  webLink: requireClass(styles.webLink, "sessionlinkssection.module.css", "webLink"),
  row: requireClass(styles.row, "sessionlinkssection.module.css", "row"),
  label: requireClass(styles.label, "sessionlinkssection.module.css", "label"),
  hostHint: requireClass(styles.hostHint, "sessionlinkssection.module.css", "hostHint"),
  action: requireClass(styles.action, "sessionlinkssection.module.css", "action"),
  destinationBar: requireClass(styles.destinationBar, "sessionlinkssection.module.css", "destinationBar"),
  destinationText: requireClass(styles.destinationText, "sessionlinkssection.module.css", "destinationText"),
};

function linkCategory(rawUrl: string): LinkCategory {
  if (isFileHref(rawUrl)) return "Local files";
  try {
    const parsed = new URL(rawUrl);
    const host = parsed.hostname.toLowerCase();
    const path = parsed.pathname.toLowerCase();
    if (/\/(?:pull|pulls|merge_requests)\b/.test(path) || /\/pr\/\d+/.test(path)) return "Pull Requests";
    if (/\/(?:issues|issue)\b/.test(path)) return "Issues";
    if (
      host.startsWith("artifacts.") ||
      host.startsWith("docs.") ||
      host.includes("notion.") ||
      host.includes("wiki.") ||
      path.startsWith("/p/") ||
      path.startsWith("/docs/") ||
      path.startsWith("/doc/") ||
      path.endsWith(".md")
    ) {
      return "Artifacts & Docs";
    }
  } catch {
    // Non-URL custom scheme falls through to Other links.
  }
  return "Other links";
}

function linkHostHint(rawUrl: string): string {
  if (!isWebHref(rawUrl)) return "";
  try {
    return new URL(rawUrl).hostname.replace(/^www\./i, "");
  } catch {
    return "";
  }
}

function orderByRecency(urls: readonly SessionURL[]): SessionURL[] {
  return urls
    .map((entry, index) => ({ entry, index }))
    .sort((a, b) => {
      const aTime = a.entry.addedAt ?? -1;
      const bTime = b.entry.addedAt ?? -1;
      if (aTime !== bTime) return bTime - aTime;
      return b.index - a.index;
    })
    .map((item) => item.entry);
}

/** Dedicated Overview Links view: switch between recency and grouped
 * categories, filter by label/URL/category, render clean hover-underlined
 * labels without multi-line raw URLs underneath, keep Open beside for local
 * files, and reveal the exact destination on hover or focus. */
export function SessionLinksSection({
  sessionRef,
  model,
  sourcePaneId,
}: {
  sessionRef: string;
  model: ThreadModel;
  sourcePaneId?: string;
}): JSX.Element {
  const [mode, setMode] = useState<LinksOrderMode>("recent");
  const [query, setQuery] = useState("");
  const [inspectedUrl, setInspectedUrl] = useState<string | null>(null);

  const normalizedQuery = query.trim().toLowerCase();
  const filteredUrls = useMemo(() => {
    const ordered = orderByRecency(model.sessionUrls);
    if (!normalizedQuery) return ordered;
    return ordered.filter((entry) => {
      const label = (entry.label ?? "").toLowerCase();
      const url = entry.url.toLowerCase();
      const category = linkCategory(entry.url).toLowerCase();
      return label.includes(normalizedQuery) || url.includes(normalizedQuery) || category.includes(normalizedQuery);
    });
  }, [model.sessionUrls, normalizedQuery]);

  const groupedUrls = useMemo(() => {
    if (mode !== "grouped") return [];
    const buckets = new Map<LinkCategory, SessionURL[]>();
    for (const entry of filteredUrls) {
      const category = linkCategory(entry.url);
      const list = buckets.get(category);
      if (list) list.push(entry);
      else buckets.set(category, [entry]);
    }
    return CATEGORY_ORDER.flatMap((category) => {
      const items = buckets.get(category);
      return items && items.length > 0 ? [{ category, items }] : [];
    });
  }, [filteredUrls, mode]);

  if (model.sessionUrls.length === 0) {
    return (
      <div className={CLASS.root}>
        <EmptyState size="compact" title="No saved links" />
      </div>
    );
  }

  function renderItem(entry: SessionURL) {
    const title = entry.label || entry.url;
    const hostHint = linkHostHint(entry.url);

    if (isWebHref(entry.url)) {
      return (
        <li key={entry.id} data-link-url={entry.url}>
          <a
            className={CLASS.webLink}
            data-testid={`about-link-${entry.id}`}
            href={entry.url}
            title={entry.url}
            target="_blank"
            rel="noopener noreferrer"
          >
            <span className={CLASS.label}>{title}</span>
            {hostHint ? <span className={CLASS.hostHint}>{hostHint}</span> : null}
          </a>
        </li>
      );
    }

    const fileAction =
      sourcePaneId !== undefined && isFileHref(entry.url) ? (
        <FileOpenBesideButton
          absPath={fileURLToPath(entry.url)}
          sessionRef={sessionRef}
          cwd={model.cwd}
          sourcePaneId={sourcePaneId}
        />
      ) : undefined;

    return (
      <li key={entry.id} data-link-url={entry.url}>
        <div className={CLASS.row} data-testid={`about-link-${entry.id}`} title={entry.url}>
          <span className={CLASS.label}>{title}</span>
          {fileAction !== undefined ? <span className={CLASS.action}>{fileAction}</span> : null}
        </div>
      </li>
    );
  }

  function inspectTargetUrl(target: EventTarget | null) {
    if (!target || typeof target !== "object" || !("closest" in target)) return;
    const item = (target as HTMLElement).closest<HTMLElement>("[data-link-url]");
    setInspectedUrl(item?.dataset.linkUrl ?? null);
  }

  return (
    <div className={CLASS.root}>
      <div className={CLASS.toolbar}>
        <SegmentedControl<LinksOrderMode>
          label="Link order"
          hideLabel
          size="sm"
          value={mode}
          onChange={setMode}
          options={MODE_OPTIONS}
        />
        <div className={CLASS.filterWrap}>
          <Input
            type="search"
            fill
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Filter links…"
            aria-label="Filter links"
          />
        </div>
      </div>

      {filteredUrls.length === 0 ? (
        <EmptyState size="compact" title="No matching links" />
      ) : mode === "recent" ? (
        <ul
          className={CLASS.list}
          aria-label="Session links"
          onPointerOver={(event) => inspectTargetUrl(event.target)}
          onPointerLeave={() => setInspectedUrl(null)}
          onFocusCapture={(event) => inspectTargetUrl(event.target)}
          onBlurCapture={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setInspectedUrl(null);
          }}
        >
          {filteredUrls.map(renderItem)}
        </ul>
      ) : (
        <section
          className={CLASS.groups}
          aria-label="Session links"
          onPointerOver={(event) => inspectTargetUrl(event.target)}
          onPointerLeave={() => setInspectedUrl(null)}
          onFocusCapture={(event) => inspectTargetUrl(event.target)}
          onBlurCapture={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setInspectedUrl(null);
          }}
        >
          {groupedUrls.map(({ category, items }) => (
            <section key={category} className={CLASS.group}>
              <div className={CLASS.groupHeader}>
                <Eyebrow as="h3">{category}</Eyebrow>
                <span className={CLASS.groupCount}>{items.length}</span>
              </div>
              <ul className={CLASS.list}>{items.map(renderItem)}</ul>
            </section>
          ))}
        </section>
      )}

      {inspectedUrl !== null ? (
        <div className={CLASS.destinationBar} data-testid="session-links-destination" aria-live="polite">
          <span className={CLASS.destinationText} title={inspectedUrl}>
            {inspectedUrl}
          </span>
        </div>
      ) : null}
    </div>
  );
}
