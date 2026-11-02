import type { JobActivityJob, SessionDelegate, SessionWatch } from "@evener/appwire-client";
import { type ActivityTimeValue, validActivityTime } from "../../panes/session/chrome/activityTime";

export function agentActivityTime(sub: SessionDelegate): ActivityTimeValue | undefined {
  if (sub.terminal) return validActivityTime(sub.runEndedAt, "ended");
  const started = validActivityTime(sub.runStartedAt, "started");
  const activity = validActivityTime(sub.latestActivityAt, "activity");
  if (started && activity) return Date.parse(activity.at) >= Date.parse(started.at) ? activity : started;
  return activity ?? started;
}

export function jobActivityTime(job: JobActivityJob): ActivityTimeValue | undefined {
  if (job.terminal) return validActivityTime(job.endedAt, "ended");
  return validActivityTime(job.lastOutputAt, "output") ?? validActivityTime(job.startedAt, "started");
}

export function watchActivityTime(watch: SessionWatch): ActivityTimeValue | undefined {
  const times = watch.watch.deliveryTimes;
  const last = times && times.length > 0 ? times[times.length - 1] : undefined;
  const delivery = validActivityTime(last, "delivered");
  if (delivery) return delivery;
  return validActivityTime(watch.watch.createdAt, watch.state === "armed" ? "armed" : "created");
}
