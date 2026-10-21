import { useLayoutEffect, useRef, useState } from "react";

// Only a Send-to-Stop swap needs protection. The menu uses its own existing
// pending gates, and opening a composer on Stop must not delay that action.
export function useInlineStopReady(sessionRef: string, slot: "send" | "stop" | null): boolean {
  const previous = useRef({ sessionRef, slot });
  const [ready, setReady] = useState(true);
  useLayoutEffect(() => {
    const delay = previous.current.sessionRef === sessionRef && previous.current.slot === "send" && slot === "stop";
    previous.current = { sessionRef, slot };
    setReady(!delay);
    if (!delay) return;
    const timer = window.setTimeout(() => setReady(true), 500);
    return () => window.clearTimeout(timer);
  }, [sessionRef, slot]);
  return ready;
}
