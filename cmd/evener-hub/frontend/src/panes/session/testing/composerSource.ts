import { act } from "@testing-library/react";
import { afterEach } from "vitest";
import { type ComposerSourceState, createComposerSourceState } from "../composer/sourceState";

const sources = new Set<ComposerSourceState>();
afterEach(() => {
  act(() => {
    for (const source of sources) source.dispose();
  });
  sources.clear();
});

/** An explicit source lifetime for Composer mounts outside the workspace. */
export function createTestComposerSource(ref: string): ComposerSourceState {
  const source = createComposerSourceState(ref);
  sources.add(source);
  return source;
}
