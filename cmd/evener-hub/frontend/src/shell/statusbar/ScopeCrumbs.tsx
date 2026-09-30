// The scope breadcrumb, shared by the status bar and the activity sidebar:
// the path from the root session to the scope you're reading. Crumbs above
// the leaf open their session; the leaf is current text, never a link to
// itself.

import { selectSessionSummary } from "../../stores/navigation/selectors";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useThreadsStore } from "../../stores/threads";
import { requireClass } from "../../widgets/internal/requireClass";
import { openSessionByRef } from "../sessionPlacement";
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

export function ScopeCrumbs({ path, hierarchy = false }: { path: ScopeCrumb[]; hierarchy?: boolean }) {
  useNavigationStore((state) => state.resources);
  const threads = useThreadsStore((state) => state.threads);
  return (
    <nav className={`${CLASS.crumbs}${hierarchy ? ` ${CLASS.hierarchy}` : ""}`} aria-label="Scope">
      {path.map((crumb, depth) => {
        const row = selectSessionSummary(crumb.ref, navigationStore.getState());
        const title = threads.get(crumb.ref)?.name || (row?.ref === crumb.ref ? row.title : null) || crumb.title;
        const last = depth === path.length - 1;
        return (
          <span
            key={crumb.ref}
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
                onClick={() => openSessionByRef(crumb.ref)}
              >
                {title}
              </button>
            )}
          </span>
        );
      })}
    </nav>
  );
}
