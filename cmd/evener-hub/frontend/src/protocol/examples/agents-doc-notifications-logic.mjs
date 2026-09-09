import { decodeAgentsDoc } from "./agents-doc-logic.mjs";
import { observeNotifications } from "./bounded-notifications.mjs";

const method = "evener/settings/agentsDoc/changed";
export function runAgentsDocNotifications(hub, { observe = null, observeDurationMs = 1000, maxEvents = 100 } = {}) {
  return observeNotifications(hub, {
    methods: [method],
    decodeNotification: (event) => ({ method, ...decodeAgentsDoc(event.params) }),
    readSnapshot: async () => decodeAgentsDoc(await hub.request("evener/settings/agentsDoc/get", {})),
    observe,
    observeDurationMs,
    maxEvents,
  });
}
