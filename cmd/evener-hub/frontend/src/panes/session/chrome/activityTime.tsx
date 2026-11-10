import { absoluteTime, isValidActivityTimestamp, relativeTime, type TaskRow } from "@evener/appwire-client";
import { type ReactNode, useMemo } from "react";
import { requireClass } from "../../../widgets/internal/requireClass";
import { useNowTick } from "../liveness";
import styles from "./activitytime.module.css";

const CLASS = {
  rowTime: requireClass(styles.rowTime, "activitytime.module.css", "rowTime"),
};

export function ActivityBodyClock({ children }: { children: (now: number) => ReactNode }) {
  return children(useNowTick(30_000));
}

export type ActivityTimeMeaning =
  | "activity"
  | "output"
  | "started"
  | "ended"
  | "updated"
  | "completed"
  | "cancelled"
  | "delivered"
  | "armed"
  | "created";

export interface ActivityTimeValue {
  at: string;
  meaning: ActivityTimeMeaning;
}

const MEANING_LABEL: Record<ActivityTimeMeaning, string> = {
  activity: "Latest activity",
  output: "Latest output",
  started: "Started",
  ended: "Ended",
  updated: "Last updated",
  completed: "Completed",
  cancelled: "Cancelled",
  delivered: "Last delivered",
  armed: "Armed",
  created: "Created",
};

export function validActivityTime(at: string | undefined, meaning: ActivityTimeMeaning): ActivityTimeValue | undefined {
  return at !== undefined && isValidActivityTimestamp(at) ? { at, meaning } : undefined;
}

export function ActivityTime({ value, now }: { value: ActivityTimeValue | undefined; now: number }) {
  const at = value?.at;
  const absolute = useMemo(() => (at === undefined ? undefined : absoluteTime(at)), [at]);
  if (!value) return null;
  return (
    <time
      className={CLASS.rowTime}
      data-activity-time
      data-meaning={value.meaning}
      dateTime={value.at}
      title={`${MEANING_LABEL[value.meaning]}: ${absolute}`}
    >
      {relativeTime(value.at, new Date(now))}
    </time>
  );
}

export function taskActivityTime(task: TaskRow): ActivityTimeValue | undefined {
  const settledTime =
    task.status === "done" || task.status === "cancelled"
      ? validActivityTime(task.completedAt, task.status === "done" ? "completed" : "cancelled")
      : undefined;
  return settledTime ?? validActivityTime(task.updatedAt, "updated");
}

export function TaskActivityTime({ task, now }: { task: TaskRow; now: number }) {
  return <ActivityTime value={taskActivityTime(task)} now={now} />;
}
