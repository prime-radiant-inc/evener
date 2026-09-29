// The scope breadcrumb, shared by the status bar and the activity sidebar:
// the path from the root session to the scope you're reading. Crumbs above
// the leaf open their session; the leaf is current text, never a link to
// itself.

import { requireClass } from "../../widgets/internal/requireClass";
import { openSessionByRef } from "../sessionPlacement";
import styles from "./scopeCrumbs.module.css";
import type { ScopeCrumb } from "./statusScope";

const CLASS = {
  crumbs: requireClass(styles.crumbs, "scopeCrumbs.module.css", "crumbs"),
  crumbWrap: requireClass(styles.crumbWrap, "scopeCrumbs.module.css", "crumbWrap"),
  crumbSep: requireClass(styles.crumbSep, "scopeCrumbs.module.css", "crumbSep"),
  crumbCurrent: requireClass(styles.crumbCurrent, "scopeCrumbs.module.css", "crumbCurrent"),
  crumbBtn: requireClass(styles.crumbBtn, "scopeCrumbs.module.css", "crumbBtn"),
};

export function ScopeCrumbs({ path }: { path: ScopeCrumb[] }) {
  return (
    <nav className={CLASS.crumbs} aria-label="Scope">
      {path.map((crumb, depth) => {
        const last = depth === path.length - 1;
        return (
          <span key={crumb.ref} className={CLASS.crumbWrap}>
            {depth > 0 ? <span className={CLASS.crumbSep}>›</span> : null}
            {last ? (
              <span className={CLASS.crumbCurrent}>{crumb.title}</span>
            ) : (
              <button type="button" className={CLASS.crumbBtn} onClick={() => openSessionByRef(crumb.ref)}>
                {crumb.title}
              </button>
            )}
          </span>
        );
      })}
    </nav>
  );
}
