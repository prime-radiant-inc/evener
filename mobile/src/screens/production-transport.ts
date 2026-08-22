/**
 * Production native transport adapter — maps the typed {@link NativeCommand}
 * union to real Tauri plugin routes.
 *
 * Tauri plugin commands are invoked via namespaced routes:
 * `plugin:evener-native|<snake_case_command>` with a `{payload: ...}` envelope.
 * This module is the only place that knows the route/envelope format; tests
 * inject a fake {@link TauriBridge} to assert exact routes without a Tauri
 * runtime.
 *
 * Lifecycle events use Tauri's `listen` API (from `@tauri-apps/api/event`,
 * imported through the sole seam `services/tauri.ts`). The adapter maps
 * `tauri://suspended`→background and `tauri://resumed`→foreground, returns
 * race-safe/idempotent unsubscribe, and never delivers after unsubscribe.
 */
import type { NativeTransport } from "../native/client";
import type {
  LifecycleState,
  NativeCommand,
  NativeResponse,
} from "../native/contract";
import { decodeNativeResponse } from "../native/contract";
import type { TauriBridge } from "../services/tauri";

const PLUGIN = "plugin:evener-native|";

/**
 * Create the production native transport backed by a real {@link TauriBridge}.
 * Exported so tests can inject a fake bridge and assert exact routes/envelopes.
 */
export function createTauriNativeTransport(
  bridge: TauriBridge,
): NativeTransport {
  return {
    async send(command: NativeCommand): Promise<NativeResponse> {
      const route = `${PLUGIN}${commandToRoute(command)}`;
      const payload = commandToPayload(command);
      const raw = await bridge.invoke<unknown>(route, { payload });
      return decodeNativeResponse(raw);
    },

    subscribe(type, handler) {
      if (type !== "lifecycle.changed") {
        return () => {};
      }
      return subscribeLifecycle(bridge, handler);
    },
  };
}

// ---------------------------------------------------------------------------
// Command → route and payload mapping
// ---------------------------------------------------------------------------

function commandToRoute(command: NativeCommand): string {
  switch (command.type) {
    case "pairing.scanAndPreview":
      return "scan_and_preview_pairing";
    case "haptic.perform":
      return "haptic_perform";
    case "contentSize.get":
      return "content_size_get";
    default:
      // secure.*, speech.*, synthesis.*, permission.* are NOT exposed to JS.
      throw new Error(`unsupported native command: ${command.type}`);
  }
}

function commandToPayload(command: NativeCommand): Record<string, unknown> {
  switch (command.type) {
    case "pairing.scanAndPreview":
      return {};
    case "haptic.perform":
      return { kind: command.kind };
    case "contentSize.get":
      return {};
    default:
      throw new Error(`unsupported native command: ${command.type}`);
  }
}

// ---------------------------------------------------------------------------
// Lifecycle subscription — race-safe, idempotent unsubscribe
// ---------------------------------------------------------------------------

function subscribeLifecycle(
  bridge: TauriBridge,
  handler: (event: { state: LifecycleState }) => void,
): () => void {
  let unsubscribed = false;
  const unlistenFns: Array<() => void> = [];

  const onEvent = (event: { event: string }) => {
    if (unsubscribed) return;
    const state = mapLifecycleEvent(event.event);
    if (state !== null) {
      handler({ state });
    }
  };

  // listen returns a Promise<UnlistenFn>. We store the unlisten functions as
  // they resolve. If unsubscribe is called before the promises resolve,
  // `unsubscribed` prevents further deliveries and the unlisten functions
  // are called when they arrive.
  void bridge
    .listen("tauri://suspended", onEvent)
    .then((fn) => {
      if (unsubscribed) {
        fn();
      } else {
        unlistenFns.push(fn);
      }
    })
    .catch(() => {});

  void bridge
    .listen("tauri://resumed", onEvent)
    .then((fn) => {
      if (unsubscribed) {
        fn();
      } else {
        unlistenFns.push(fn);
      }
    })
    .catch(() => {});

  return () => {
    if (unsubscribed) return;
    unsubscribed = true;
    for (const fn of unlistenFns) {
      fn();
    }
    unlistenFns.length = 0;
  };
}

function mapLifecycleEvent(eventName: string): LifecycleState | null {
  if (eventName === "tauri://suspended") return "background";
  if (eventName === "tauri://resumed") return "foreground";
  return null;
}
