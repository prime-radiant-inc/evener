// LocationCluster: the session's location facts (git branch, canonical project
// path, cwd) in the status row - restoring the legacy input strip's
// status-location cluster (cmd/serf-hub/templates/partials/input_strip.html:
// 6-10) onto the ThreadModel location fields. Each fact is OMITTED when the
// wire did not provide it (honest absence, no placeholder dash), mirroring the
// legacy {{if .Branch}}/{{if .Worktree}}/{{if .WorkingDir}} guards. Values are
// monospaced and ellipsized, with the full text in a title tooltip, since a
// cwd/project path can be long.
//
// Field mapping (all snapshot-only, reducer.ts hydrateThread, no live push):
//   branch  <- model.gitBranch   (Thread.gitInfo.branch)
//   project <- model.projectPath (Thread.ProjectPath, the hub-resolved
//              CANONICAL PROJECT root - appwire/types.go:186-191, deliberately
//              separate from cwd: a linked worktree keeps a different cwd within
//              the same project. Labeled "project", not the legacy "worktree",
//              which the new snapshot does not carry.)
//   cwd     <- model.cwd         (Thread.cwd)
import type { ThreadModel } from "../../../protocol/model";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./locationcluster.module.css";

const CLASS = {
  cluster: requireClass(styles.cluster, "locationcluster.module.css", "cluster"),
  part: requireClass(styles.part, "locationcluster.module.css", "part"),
  key: requireClass(styles.key, "locationcluster.module.css", "key"),
  value: requireClass(styles.value, "locationcluster.module.css", "value"),
};

interface LocationPart {
  key: string;
  value: string;
}

// Order mirrors the legacy status-location cluster: branch, then project (the
// legacy "worktree" slot), then cwd. A fact is included only when its wire
// value is non-empty - an empty cwd (a pathless external thread) is dropped
// too, not shown blank.
function locationParts(model: ThreadModel): LocationPart[] {
  const parts: LocationPart[] = [];
  if (model.gitBranch) parts.push({ key: "branch", value: model.gitBranch });
  if (model.projectPath) parts.push({ key: "project", value: model.projectPath });
  if (model.cwd) parts.push({ key: "cwd", value: model.cwd });
  return parts;
}

export function LocationCluster({ model }: { model: ThreadModel }) {
  const parts = locationParts(model);
  if (parts.length === 0) return null;

  return (
    <span className={CLASS.cluster} data-testid="status-row-location">
      {parts.map((part) => (
        <span key={part.key} className={CLASS.part} title={`${part.key} ${part.value}`}>
          <span className={CLASS.key} data-testid="location-key">
            {part.key}
          </span>
          <span className={CLASS.value}>{part.value}</span>
        </span>
      ))}
    </span>
  );
}
