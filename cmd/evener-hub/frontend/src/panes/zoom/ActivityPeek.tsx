import { type ReactElement, useState } from "react";
import { ACTIVITY_TABS, activityTabSpec } from "../../shell/activitybar/activityTabs";
import { cadenceStateFor, SessionStatusIndicator } from "../../shell/SessionStatusIndicator";
import type { ActivityScope, ActivityTab } from "../../shell/statusbar/statusScope";
import { IconButton, Popover } from "../../widgets";
import { DisclosurePersistenceContext } from "../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../widgets/internal/requireClass";
import { enterAgentCascade } from "./actions";
import styles from "./zoom.module.css";

const CLASS = {
  peek: requireClass(styles.peek, "zoom.module.css", "peek"),
  peekHead: requireClass(styles.peekHead, "zoom.module.css", "peekHead"),
  peekBody: requireClass(styles.peekBody, "zoom.module.css", "peekBody"),
  activityControls: requireClass(styles.activityControls, "zoom.module.css", "activityControls"),
  activityChip: requireClass(styles.activityChip, "zoom.module.css", "activityChip"),
  activityLabel: requireClass(styles.activityLabel, "zoom.module.css", "activityLabel"),
  state: requireClass(styles.state, "zoom.module.css", "state"),
  stateIndicator: requireClass(styles.stateIndicator, "zoom.module.css", "stateIndicator"),
  stateLabel: requireClass(styles.stateLabel, "zoom.module.css", "stateLabel"),
};

export function ActivityPeek({
  paneId,
  scope,
  tab,
  open,
  onClose,
  trigger,
}: {
  paneId: string;
  scope: ActivityScope;
  tab: ActivityTab;
  open: boolean;
  onClose: () => void;
  trigger: ReactElement;
}) {
  const spec = activityTabSpec(tab);
  const Body = spec.Body;
  return (
    <Popover open={open} onClose={onClose} trigger={trigger} closeOnScroll={false} data-testid="activity-peek">
      <div className={CLASS.peek} role="dialog" aria-label={`${spec.label} for ${scope.leaf.title}`}>
        <header className={CLASS.peekHead}>
          <strong>
            {spec.label} · {scope.leaf.title}
          </strong>
          <IconButton label="Close activity peek" icon="×" variant="quiet" size="sm" onClick={onClose} />
        </header>
        <div className={CLASS.peekBody}>
          <DisclosurePersistenceContext.Provider value={JSON.stringify(["cascade-peek", paneId, scope.leaf.ref, tab])}>
            <Body
              scope={scope}
              onDrill={(sub) => {
                enterAgentCascade(sub, paneId);
                onClose();
              }}
            />
          </DisclosurePersistenceContext.Provider>
        </div>
      </div>
    </Popover>
  );
}

export function ScopeActivityControls({ paneId, scope }: { paneId: string; scope: ActivityScope }) {
  const [tab, setTab] = useState<ActivityTab | null>(null);
  return (
    <div className={CLASS.activityControls}>
      {ACTIVITY_TABS.filter((spec) => spec.id !== "about").map((spec) => (
        <ActivityPeek
          key={spec.id}
          paneId={paneId}
          scope={scope}
          tab={spec.id}
          open={tab === spec.id}
          onClose={() => setTab(null)}
          trigger={
            <button
              type="button"
              className={CLASS.activityChip}
              aria-label={`${spec.chipLabel(scope.counts)} - peek at ${scope.leaf.ref}`}
              aria-expanded={tab === spec.id}
              aria-haspopup="dialog"
              onClick={() => setTab(tab === spec.id ? null : spec.id)}
            >
              <span aria-hidden="true">{spec.glyph}</span>
              <span className={CLASS.activityLabel}>{spec.label}</span>
              <span>{spec.chipCount(scope.counts)}</span>
            </button>
          }
        />
      ))}
    </div>
  );
}

export function ScopeRuntimeState({ scope, compact = false }: { scope: ActivityScope; compact?: boolean }) {
  const state = scope.activity?.runtime?.status.type;
  const label = state ? state.charAt(0).toUpperCase() + state.slice(1) : "State unknown";
  if (compact) {
    return (
      <span className={CLASS.stateIndicator} data-runtime-state={state ?? "unknown"} title={label}>
        <SessionStatusIndicator state={cadenceStateFor(state ?? "")} />
        <span className={CLASS.stateLabel} data-runtime-label>
          {label}
        </span>
      </span>
    );
  }
  return (
    <span className={CLASS.state} data-runtime-state={state ?? "unknown"}>
      {label}
    </span>
  );
}
