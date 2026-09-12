import { useLayoutEffect, useRef } from "react";

/** Async editor work may finish after dismissal; only a mounted editor owns its feedback. */
export function useEditorLifetime() {
  const active = useRef(true);
  useLayoutEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  return active;
}
