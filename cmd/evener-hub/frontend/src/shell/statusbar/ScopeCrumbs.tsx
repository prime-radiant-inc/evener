// The activity sidebar's scope breadcrumb: the path from the root session to
// the scope you're reading. Crumbs above the leaf open their session; the leaf
// is current text, never a link to itself.

import { navigationSummaryFor } from "../../panes/session/threadTitle";
import { useNavigationStore } from "../../stores/navigation/store";
import { useThreadsStore } from "../../stores/threads";
import { Button } from "../../widgets";
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
};

/** A scope's display title: the loaded thread name, then the navigation row's
 * title for the same ref, then the caller's fallback. The activity context's
 * ancestor titles are session ids, not names, so a surface that shows a scope
 * should resolve its title here rather than render a fallback ref raw. */
export function useScopeTitle(ref: string, fallback: string): string {
  const name = useThreadsStore((state) => state.threads.get(ref)?.name);
  useNavigationStore((state) => state.resources);
  return name || navigationSummaryFor(ref)?.title || fallback;
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
  const navigate = () => {
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
  };
  return (
    <span
      className={CLASS.crumbWrap}
      style={hierarchy ? { marginInlineStart: `calc(var(--space-2) * ${Math.min(depth, 3)})` } : undefined}
    >
      {depth > 0 && !hierarchy ? <span className={CLASS.crumbSep}>›</span> : null}
      {last ? (
        <span className={CLASS.crumbCurrent} aria-current="page" title={title}>
          {title}
        </span>
      ) : (
        <Button variant="link" title={title} data-session-navigation-ref={crumb.ref} onClick={navigate}>
          {title}
        </Button>
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
