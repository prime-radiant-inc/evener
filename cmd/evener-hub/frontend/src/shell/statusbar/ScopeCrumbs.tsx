// The activity sidebar's scope breadcrumb: the path from the root session to
// the scope you're reading. Crumbs above the leaf open their session; the leaf
// is current text, never a link to itself.

import { selectSessionSummary } from "../../stores/navigation/selectors";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useThreadsStore } from "../../stores/threads";
import { requireClass } from "../../widgets/internal/requireClass";
import { refParam } from "../routing";
import { openSessionByRef } from "../sessionPlacement";
import { workspaceStore } from "../workspace";
import styles from "./scopeCrumbs.module.css";
import type { ScopeCrumb } from "./statusScope";

const CLASS = {
  hierarchy: requireClass(styles.hierarchy, "scopeCrumbs.module.css", "hierarchy"),
  crumbs: requireClass(styles.crumbs, "scopeCrumbs.module.css", "crumbs"),
  crumbWrap: requireClass(styles.crumbWrap, "scopeCrumbs.module.css", "crumbWrap"),
  crumbSep: requireClass(styles.crumbSep, "scopeCrumbs.module.css", "crumbSep"),
  crumbCurrent: requireClass(styles.crumbCurrent, "scopeCrumbs.module.css", "crumbCurrent"),
  crumbBtn: requireClass(styles.crumbBtn, "scopeCrumbs.module.css", "crumbBtn"),
};

/** A scope's display title: the loaded thread name, then the navigation row's
 * title for the same ref, then the caller's fallback. The activity context's
 * ancestor titles are session ids, not names, so every surface that shows a
 * scope must resolve through here rather than render its fallback raw. */
export function useScopeTitle(ref: string, fallback: string): string {
  const name = useThreadsStore((state) => state.threads.get(ref)?.name);
  useNavigationStore((state) => state.resources);
  const row = selectSessionSummary(ref, navigationStore.getState());
  return name || (row?.ref === ref ? row.title : null) || fallback;
}

function ScopeCrumbLabel({
  crumb,
  depth,
  last,
  hierarchy,
  onNavigate,
}: {
  crumb: ScopeCrumb;
  depth: number;
  last: boolean;
  hierarchy: boolean;
  onNavigate?: (ref: string) => void;
}) {
  const title = useScopeTitle(crumb.ref, crumb.title);
  return (
    <span
      className={CLASS.crumbWrap}
      style={hierarchy ? { paddingInlineStart: `calc(var(--space-2) * ${Math.min(depth, 3)})` } : undefined}
    >
      {depth > 0 && !hierarchy ? <span className={CLASS.crumbSep}>›</span> : null}
      {last ? (
        <span className={CLASS.crumbCurrent} aria-current="page" title={title}>
          {title}
        </span>
      ) : (
        <button
          type="button"
          className={CLASS.crumbBtn}
          title={title}
          data-session-navigation-ref={crumb.ref}
          onClick={() => {
            if (onNavigate) {
              onNavigate(crumb.ref);
              return;
            }
            const workspace = workspaceStore.getState();
            const transcript = workspace.panes.find(
              (pane) => pane.type === "transcript" && refParam(pane.params) === crumb.ref,
            );
            // Ancestor navigation preserves the selected read-only context;
            // the rail and other session links still request live session chrome.
            if (transcript) workspace.focusPane(transcript.id);
            else openSessionByRef(crumb.ref);
          }}
        >
          {title}
        </button>
      )}
    </span>
  );
}

export function ScopeCrumbs({
  path,
  hierarchy = false,
  onNavigate,
}: {
  path: ScopeCrumb[];
  hierarchy?: boolean;
  onNavigate?: (ref: string) => void;
}) {
  useNavigationStore((state) => state.resources);
  return (
    <nav className={`${CLASS.crumbs}${hierarchy ? ` ${CLASS.hierarchy}` : ""}`} aria-label="Scope">
      {path.map((crumb, depth) => (
        <ScopeCrumbLabel
          key={crumb.ref}
          crumb={crumb}
          depth={depth}
          last={depth === path.length - 1}
          hierarchy={hierarchy}
          onNavigate={onNavigate}
        />
      ))}
    </nav>
  );
}
