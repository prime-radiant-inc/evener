import type { JobActivityJob, SessionDelegate, SessionWatch, TaskRow } from "@evener/appwire-client";
import { absoluteTime, relativeTime } from "@evener/appwire-client";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./activitybar.module.css";

const CLASS = {
  rowTime: requireClass(styles.rowTime, "activitybar.module.css", "rowTime"),
};

export type ActivityTimeMeaning =
  | "activity"
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
  started: "Started",
  ended: "Ended",
  updated: "Last updated",
  completed: "Completed",
  cancelled: "Cancelled",
  delivered: "Last delivered",
  armed: "Armed",
  created: "Created",
};

function validTime(at: string | undefined, meaning: ActivityTimeMeaning): ActivityTimeValue | undefined {
  return at && Number.isFinite(Date.parse(at)) ? { at, meaning } : undefined;
}

export function ActivityTime({ value, now }: { value: ActivityTimeValue | undefined; now: number }) {
  if (!value) return null;
  return (
    <time
      className={CLASS.rowTime}
      data-activity-time
      data-meaning={value.meaning}
      dateTime={value.at}
      title={`${MEANING_LABEL[value.meaning]}: ${absoluteTime(value.at)}`}
    >
      {relativeTime(value.at, new Date(now))}
    </time>
  );
}

export function agentActivityTime(sub: SessionDelegate): ActivityTimeValue | undefined {
  if (sub.terminal) return validTime(sub.runEndedAt, "ended");
  const started = validTime(sub.runStartedAt, "started");
  const activity = validTime(sub.latestActivityAt, "activity");
  if (started && activity) return Date.parse(activity.at) >= Date.parse(started.at) ? activity : started;
  return activity ?? started;
}

export function jobActivityTime(job: JobActivityJob): ActivityTimeValue | undefined {
  if (job.terminal) return validTime(job.endedAt, "ended");
  return validTime(job.lastOutputAt, "activity") ?? validTime(job.startedAt, "started");
}

export function watchActivityTime(watch: SessionWatch): ActivityTimeValue | undefined {
  const times = watch.watch.deliveryTimes;
  const last = times && times.length > 0 ? times[times.length - 1] : undefined;
  const delivery = validTime(last, "delivered");
  if (delivery) return delivery;
  return validTime(watch.watch.createdAt, watch.state === "armed" ? "armed" : "created");
}

export function taskActivityTime(task: TaskRow): ActivityTimeValue | undefined {
  if (task.status === "done" || task.status === "cancelled") {
    return (
      validTime(task.completedAt, task.status === "done" ? "completed" : "cancelled") ??
      validTime(task.updatedAt, "updated")
    );
  }
  return validTime(task.updatedAt, "updated");
}
