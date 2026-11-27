import type { SessionURL, ThreadModel } from "@evener/appwire-client";
import { fileURLToPath } from "@evener/appwire-client/docContent";
import {
  type JSX,
  type FocusEvent as ReactFocusEvent,
  type PointerEvent as ReactPointerEvent,
  useMemo,
  useState,
} from "react";
import { EmptyState, Eyebrow, Input, isWebHref, SegmentedControl } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { FileOpenBesideButton } from "../transcript/fileOpenBeside";
import { ActivityTime, type ActivityTimeValue, unixSecondsActivityTime } from "./activityTime";
import styles from "./sessionlinkssection.module.css";

/** A `file://` link can open in-app beside the transcript; every other href is web. */
function isFileHref(href: string): boolean {
  return /^file:\/\//.test(href);
}

type LinksOrderMode = "recent" | "grouped";
type LinkCategory = "Artifacts & Docs" | "Pull Requests" | "Issues" | "Local files" | "Other links";

interface PreparedLink {
  entry: SessionURL;
  category: LinkCategory;
  categoryLower: string;
  labelLower: string;
  urlLower: string;
  addedTime: ActivityTimeValue | undefined;
}

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
    if (/\/(?:pull|pulls|merge_requests)\b/.test(path) || /\/pr\/\d+/.test(path)) {
      return "Pull Requests";
    }
    if (/\/(?:issues|issue)\b/.test(path)) {
      return "Issues";
    }
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
    return "Other links";
  } catch {
    return "Other links";
  }
}

function prepareLinksByRecency(urls: readonly SessionURL[]): PreparedLink[] {
  return urls
    .map((entry, index) => ({ entry, index }))
    .sort((a, b) => {
      const aTime = a.entry.addedAt ?? -1;
      const bTime = b.entry.addedAt ?? -1;
      if (aTime !== bTime) return bTime - aTime;
      return b.index - a.index;
    })
    .map(({ entry }) => {
      const category = linkCategory(entry.url);
      return {
        entry,
        category,
        categoryLower: category.toLowerCase(),
        labelLower: (entry.label ?? "").toLowerCase(),
        urlLower: entry.url.toLowerCase(),
        addedTime: unixSecondsActivityTime(entry.addedAt, "added"),
      };
    });
}

/** Dedicated Overview Links view: switch between recency and grouped
 * categories, filter by label/URL/category, render clean hover-underlined
 * labels without multi-line raw URLs underneath, keep Open beside for local
 * files, show relative timestamps in Recent mode when available, and reveal
 * the exact destination on hover or focus. */
export function SessionLinksSection({
  sessionRef,
  model,
  sourcePaneId,
  now = Date.now(),
}: {
  sessionRef: string;
  model: ThreadModel;
  sourcePaneId?: string;
  now?: number;
}): JSX.Element {
  const [mode, setMode] = useState<LinksOrderMode>("recent");
  const [query, setQuery] = useState("");
  const [inspectedUrl, setInspectedUrl] = useState<string | null>(null);

  const preparedLinks = useMemo(() => prepareLinksByRecency(model.sessionUrls), [model.sessionUrls]);

  const normalizedQuery = query.trim().toLowerCase();
  const filteredLinks = useMemo(() => {
    if (!normalizedQuery) return preparedLinks;
    return preparedLinks.filter(
      (item) =>
        item.labelLower.includes(normalizedQuery) ||
        item.urlLower.includes(normalizedQuery) ||
        item.categoryLower.includes(normalizedQuery),
    );
  }, [preparedLinks, normalizedQuery]);

  const groupedLinks = useMemo(() => {
    if (mode !== "grouped") return [];
    const buckets = new Map<LinkCategory, PreparedLink[]>();
    for (const item of filteredLinks) {
      const list = buckets.get(item.category);
      if (list) list.push(item);
      else buckets.set(item.category, [item]);
    }
    return CATEGORY_ORDER.flatMap((category) => {
      const items = buckets.get(category);
      return items && items.length > 0 ? [{ category, items }] : [];
    });
  }, [filteredLinks, mode]);

  if (model.sessionUrls.length === 0) {
    return (
      <div className={CLASS.root}>
        <EmptyState size="compact" title="No saved links" />
      </div>
    );
  }

  function inspectTargetUrl(target: EventTarget | null) {
    if (!target || typeof target !== "object" || !("closest" in target)) return;
    const item = (target as HTMLElement).closest<HTMLElement>("[data-link-url]");
    setInspectedUrl(item?.dataset.linkUrl ?? null);
  }

  const inspectHandlers = {
    onPointerOver: (event: ReactPointerEvent<HTMLElement>) => inspectTargetUrl(event.target),
    onPointerLeave: () => setInspectedUrl(null),
    onFocusCapture: (event: ReactFocusEvent<HTMLElement>) => inspectTargetUrl(event.target),
    onBlurCapture: (event: ReactFocusEvent<HTMLElement>) => {
      if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setInspectedUrl(null);
    },
  };

  function renderItem({ entry, addedTime }: PreparedLink, showTime: boolean) {
    const title = entry.label || entry.url;
    const timestamp = showTime && addedTime ? <ActivityTime value={addedTime} now={now} /> : null;

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
            {timestamp}
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
          {timestamp}
          {fileAction !== undefined ? <span className={CLASS.action}>{fileAction}</span> : null}
        </div>
      </li>
    );
  }

  let listContent: JSX.Element;
  if (filteredLinks.length === 0) {
    listContent = <EmptyState size="compact" title="No matching links" />;
  } else if (mode === "recent") {
    listContent = (
      <ul className={CLASS.list} aria-label="Session links" {...inspectHandlers}>
        {filteredLinks.map((item) => renderItem(item, true))}
      </ul>
    );
  } else {
    listContent = (
      <section className={CLASS.groups} aria-label="Session links" {...inspectHandlers}>
        {groupedLinks.map(({ category, items }) => (
          <section key={category} className={CLASS.group}>
            <div className={CLASS.groupHeader}>
              <Eyebrow as="h3">{category}</Eyebrow>
              <span className={CLASS.groupCount}>{items.length}</span>
            </div>
            <ul className={CLASS.list}>{items.map((item) => renderItem(item, false))}</ul>
          </section>
        ))}
      </section>
    );
  }

  return (
    <div className={CLASS.root}>
      <div className={CLASS.toolbar}>
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
        <SegmentedControl<LinksOrderMode>
          label="Link order"
          hideLabel
          size="sm"
          value={mode}
          onChange={setMode}
          options={MODE_OPTIONS}
        />
      </div>

      {listContent}

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
