import type { ActivityScope } from "../../shell/statusbar/statusScope";
import { requireClass } from "../../widgets/internal/requireClass";
import { ScopeActivityControls, ScopeRuntimeState } from "./ActivityPeek";
import { popAgentCascade } from "./actions";
import styles from "./zoom.module.css";

const CLASS = {
  spinePop: requireClass(styles.spinePop, "zoom.module.css", "spinePop"),
  spineName: requireClass(styles.spineName, "zoom.module.css", "spineName"),
};

export function CascadeSpine({ paneId, scope }: { paneId: string; scope: ActivityScope }) {
  return (
    <>
      <button
        type="button"
        className={CLASS.spinePop}
        aria-label={`Show ${scope.leaf.title}`}
        onClick={() => popAgentCascade(paneId, scope.leaf.ref)}
      >
        <span className={CLASS.spineName}>{scope.leaf.title}</span>
      </button>
      <ScopeRuntimeState scope={scope} compact />
      <ScopeActivityControls paneId={paneId} scope={scope} />
    </>
  );
}
