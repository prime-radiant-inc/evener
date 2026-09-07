import { setTimeout } from "node:timers/promises";
import { clientFromEnvironment } from "./connection.mjs";
import { runReadRecipe } from "./streaming-rejoin-logic.mjs";

const ref = process.env.EVENER_THREAD_REF;
if (!ref) throw new Error("Set EVENER_THREAD_REF to an existing owned active session ref.");
const seconds = Number(process.env.EVENER_OBSERVE_SECONDS ?? 5);
if (!Number.isFinite(seconds) || seconds < 0 || seconds > 60)
  throw new Error("EVENER_OBSERVE_SECONDS must be between 0 and 60.");
const { hub } = clientFromEnvironment();
try {
  await hub.connect();
  const result = await runReadRecipe(hub, ref, () => setTimeout(seconds * 1000));
  console.log(JSON.stringify({ ref, first: result.first, paged: result.paged,
    staleCursorRecovered: result.staleCursorRecovered,
    reconciledItemCount: result.reconciledItems.length,
    rejoinedItemCount: result.rejoinedItems.length,
    observedNotificationKinds: result.notifications,
  }, null, 2));
} finally {
  hub.close();
}
