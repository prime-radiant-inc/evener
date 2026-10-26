import type { AppwireClientLike } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { useCallback, useEffect, useRef, useState } from "react";

export const SCOPED_HOST_READ_DEBOUNCE_MS = 250;

export type ScopedReadState<T> =
  | { status: "loading"; response?: T }
  | { status: "ready"; response: T }
  | { status: "error"; message: string; response?: T };

export interface UseScopedHostReadArgs<T> {
  client: AppwireClientLike;
  host: string;
  cwd: string;
  // Every other input the read depends on, serialized by the caller. A change
  // restarts the debounce and drops any in-flight answer.
  inputKey: string;
  enabled: boolean;
  // What a disabled read reports; defaults to a loading state with no response.
  disabledState?: ScopedReadState<T>;
  // Issues the request. The latest closure is the one the debounce timer calls.
  read: () => Promise<T>;
}

/**
 * useScopedHostRead is a debounced host-scoped read keyed by client, host, cwd
 * and the caller's inputKey. It keeps the previous response mounted while a
 * same-client, same-host, same-cwd refresh is in flight, keeps it beside an
 * error for the same inputs, and never exposes another request's state on the
 * render before its effect resets.
 */
export function useScopedHostRead<T>(args: UseScopedHostReadArgs<T>): { state: ScopedReadState<T>; retry(): void } {
  const { client, host, cwd, inputKey, enabled, disabledState, read } = args;
  const [retryRevision, setRetryRevision] = useState(0);
  const [result, setResult] = useState<{
    client: AppwireClientLike;
    requestKey: string;
    state: ScopedReadState<T>;
  } | null>(null);
  const lastResponse = useRef<{
    client: AppwireClientLike;
    cwd: string;
    host: string;
    logicalKey: string;
    response: T;
  } | null>(null);
  // Latest-value sync for the debounced callback below: assigned in an effect,
  // not the render body, so a concurrent render never tears the value. The
  // effect runs before the settle timer it feeds can fire.
  const readRef = useRef(read);
  useEffect(() => {
    readRef.current = read;
  }, [read]);
  const logicalKey = `${host}\u0000${cwd}\u0000${inputKey}`;
  const requestKey = `${logicalKey}\u0000${retryRevision}`;

  const retry = useCallback(() => setRetryRevision((revision) => revision + 1), []);

  useEffect(() => {
    if (!enabled) {
      lastResponse.current = null;
      setResult(null);
      return undefined;
    }

    let active = true;
    const setState = (state: ScopedReadState<T>) => setResult({ client, requestKey, state });
    const cached = lastResponse.current;
    setState(
      cached?.client === client && cached.cwd === cwd && cached.host === host
        ? { status: "loading", response: cached.response }
        : { status: "loading" },
    );

    const timer = setTimeout(() => {
      void readRef.current().then(
        (response) => {
          if (!active) return;
          lastResponse.current = { client, cwd, host, logicalKey, response };
          setState({ status: "ready", response });
        },
        (error) => {
          if (!active) return;
          const kept = lastResponse.current;
          const response = kept?.client === client && kept.logicalKey === logicalKey ? kept.response : undefined;
          setState(
            response
              ? { status: "error", message: errorText(error), response }
              : { status: "error", message: errorText(error) },
          );
        },
      );
    }, SCOPED_HOST_READ_DEBOUNCE_MS);

    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [client, cwd, host, enabled, logicalKey, requestKey]);

  const cached = lastResponse.current;
  const state: ScopedReadState<T> = !enabled
    ? (disabledState ?? { status: "loading" })
    : result?.client === client && result.requestKey === requestKey
      ? result.state
      : cached?.client === client && cached.cwd === cwd && cached.host === host
        ? { status: "loading", response: cached.response }
        : { status: "loading" };
  return { state, retry };
}
