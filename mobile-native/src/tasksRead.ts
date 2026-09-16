import type { TasksListRead } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

/** The tasks-panel read for a sheet whose client can be replaced under it (a
 * reconnect hands the sheet a new one): every read goes through whichever
 * client `current` returns at that moment, so one store keeps one entry per
 * session and the last loaded list survives a replacement that cannot
 * refresh it. */
export function tasksReadThroughCurrentClient(
  current: () => Pick<ConversationClientLike, "request">,
): TasksListRead {
  return async (ref) =>
    (await current().request("evener/tasks/list", { ref })).data;
}
