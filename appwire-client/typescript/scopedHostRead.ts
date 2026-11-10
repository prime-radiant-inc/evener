// A debounced read about one host and project, held as a framework-free store.
// The read is keyed by client, host, cwd and the caller's inputKey. The store
// keeps the previous response while a same-client, same-host, same-cwd refresh
// is in flight, keeps it beside an error for the same inputs, and never
// reports another request's state for inputs it has not yet started. A view
// binding calls start() when its inputs change (an effect, in React), reads
// view() with the current inputs on each render, and subscribes to the store
// to render again when an answer arrives. Pure logic - no DOM, no React.

import type { AppwireClientLike } from "./clientLike";
import { errorText } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { hostRequest } from "./hostRouting";
import type { LaunchConfigLayer, PluginComponentsResult, PluginPreviewResponse } from "./types.gen";

export const SCOPED_HOST_READ_DEBOUNCE_MS = 250;

export type ScopedReadState<T> =
  | { status: "loading"; response?: T }
  | { status: "ready"; response: T }
  | { status: "error"; message: string; response?: T };

export interface ScopedHostReadInputs<T> {
  /** Compared by identity: a new client drops the previous client's answers. */
  client: object;
  host: string;
  cwd: string;
  /** Every other input the read depends on, serialized by the caller. A change
   * restarts the debounce and drops any in-flight answer. */
  inputKey: string;
  enabled: boolean;
  /** What a disabled read reports; defaults to a loading state with no response. */
  disabledState?: ScopedReadState<T>;
  /** Issues the request; the debounce timer calls it once the inputs settle. */
  read: () => Promise<T>;
}

export interface ScopedHostReadSnapshot<T> {
  /** Counts retry() calls; part of the request's identity, so a retry restarts the read. */
  revision: number;
  result: { client: object; requestKey: string; state: ScopedReadState<T> } | null;
}

export interface ScopedHostReadStore<T> extends FrameworkFreeStore<ScopedHostReadSnapshot<T>> {
  /** Starts the read for these inputs and returns the function that cancels it.
   * Call it again (after cancelling) whenever an input or the revision changes. */
  start(inputs: ScopedHostReadInputs<T>): () => void;
  /** The state to show for these inputs now. */
  view(inputs: ScopedHostReadInputs<T>): ScopedReadState<T>;
  /** Asks again for the inputs last started. */
  retry(): void;
}

export function createScopedHostReadStore<T>(): ScopedHostReadStore<T> {
  const store = createFrameworkFreeStore<ScopedHostReadSnapshot<T>>(() => ({ revision: 0, result: null }));
  let lastResponse: { client: object; cwd: string; host: string; logicalKey: string; response: T } | null = null;

  const logicalKeyOf = (inputs: ScopedHostReadInputs<T>) => `${inputs.host}\u0000${inputs.cwd}\u0000${inputs.inputKey}`;
  const requestKeyOf = (inputs: ScopedHostReadInputs<T>) => `${logicalKeyOf(inputs)}\u0000${store.getState().revision}`;
  const sameScope = (inputs: ScopedHostReadInputs<T>) =>
    lastResponse?.client === inputs.client && lastResponse.cwd === inputs.cwd && lastResponse.host === inputs.host;
  const loadingState = (inputs: ScopedHostReadInputs<T>): ScopedReadState<T> =>
    lastResponse && sameScope(inputs) ? { status: "loading", response: lastResponse.response } : { status: "loading" };

  return {
    ...store,
    start(inputs) {
      if (!inputs.enabled) {
        lastResponse = null;
        if (store.getState().result) store.setState({ result: null });
        return () => {};
      }

      let active = true;
      const logicalKey = logicalKeyOf(inputs);
      const requestKey = requestKeyOf(inputs);
      const report = (state: ScopedReadState<T>) =>
        store.setState({ result: { client: inputs.client, requestKey, state } });
      report(loadingState(inputs));

      const timer = setTimeout(() => {
        void inputs.read().then(
          (response) => {
            if (!active) return;
            lastResponse = { client: inputs.client, cwd: inputs.cwd, host: inputs.host, logicalKey, response };
            report({ status: "ready", response });
          },
          (error) => {
            if (!active) return;
            const kept =
              lastResponse?.client === inputs.client && lastResponse.logicalKey === logicalKey
                ? lastResponse.response
                : undefined;
            report(
              kept
                ? { status: "error", message: errorText(error), response: kept }
                : { status: "error", message: errorText(error) },
            );
          },
        );
      }, SCOPED_HOST_READ_DEBOUNCE_MS);

      return () => {
        active = false;
        clearTimeout(timer);
      };
    },
    view(inputs) {
      if (!inputs.enabled) return inputs.disabledState ?? { status: "loading" };
      const { result } = store.getState();
      return result?.client === inputs.client && result.requestKey === requestKeyOf(inputs)
        ? result.state
        : loadingState(inputs);
    },
    retry() {
      store.setState((snapshot) => ({ revision: snapshot.revision + 1 }));
    },
  };
}

export type PluginPreviewLoadState = ScopedReadState<PluginPreviewResponse>;

export interface PluginPreviewReadArgs {
  client: Pick<AppwireClientLike, "request">;
  cwd: string;
  /** The selected host: the preview is resolved by that host's own hub through
   * evener/host/request; "local" keeps the plain call. */
  host?: string;
  launchOverrides: LaunchConfigLayer;
  pluginRevision: number;
  enabled?: boolean;
}

/** The scoped read of what plugins a session would start with, for a host and project. */
export function pluginPreviewReadInputs(args: PluginPreviewReadArgs): ScopedHostReadInputs<PluginPreviewResponse> {
  const { client, cwd, host = "local", launchOverrides, pluginRevision, enabled = true } = args;
  return {
    client,
    host,
    cwd,
    inputKey: `${JSON.stringify(launchOverrides)}\u0000${pluginRevision}`,
    enabled,
    read: () =>
      hostRequest(
        client,
        host,
        "evener/plugin/preview",
        Object.keys(launchOverrides).length > 0 ? { cwd, launchOverrides } : { cwd },
      ),
  };
}

/** The one result row a requested target's components answer is read through:
 * the hub's own row for the target, or - when the answer carries none - a
 * row that reports the absence as an error. A well-formed request always gets
 * exactly one row per target (the batch echoes its targets in order), so an
 * absent row is an off-spec answer, and reporting it as an error row is what
 * keeps it from reading as a loadable plugin with no components: the hub
 * spells that case "neither components nor error". Shared with the settings
 * store's per-key landing, which owes the same tolerance. */
export function pluginComponentsResultOrMissing(result: PluginComponentsResult | undefined): PluginComponentsResult {
  return result ?? { error: "the hub returned no result for this target" };
}

export interface PluginComponentsReadArgs {
  client: Pick<AppwireClientLike, "request">;
  /** The selected host: the components are read by that host's own hub
   * through evener/host/request; "local" keeps the plain call. */
  host?: string;
  /** Exactly one of the two forms - an installed plugin/marketplace pair, or
   * a directory path. The union cannot express the wire's malformed targets
   * (both forms set, or neither), so this read never sends one. */
  target: { plugin: string; marketplace: string } | { path: string };
}

/** The scoped read of one plugin's component inventory, for a host. The
 * launcher reads one target; the method is batch-shaped (the installed list
 * reads all its rows in one call), so this read sends a one-target batch and
 * takes that target's row back. */
export function pluginComponentsReadInputs(
  args: PluginComponentsReadArgs,
): ScopedHostReadInputs<PluginComponentsResult> {
  const { client, host = "local", target } = args;
  return {
    client,
    host,
    // The target names the plugin directory outright - an installed pair, or
    // a path the caller resolved - so the read has no project to be scoped to.
    cwd: "",
    inputKey: JSON.stringify(target),
    enabled: true,
    read: async () => {
      const response = await hostRequest(client, host, "evener/plugin/components", { targets: [target] });
      return pluginComponentsResultOrMissing(response.results[0]);
    },
  };
}
