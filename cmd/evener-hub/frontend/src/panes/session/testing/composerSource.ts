import { act } from "@testing-library/react";
import { afterEach } from "vitest";
import { type ComposerSourceState, createComposerSourceState } from "../composer/sourceState";

const sources = new Set<ComposerSourceState>();
// Disposal enqueues one final durable write on the source's recovery-write
// chain (close flushes, never drops). Drain it before this test ends: the
// durable store the web layer writes through is swapped between tests, so an
// undrained flush would land in the NEXT test's storage instead of this one.
afterEach(async () => {
  act(() => {
    for (const source of sources) source.dispose();
  });
  await Promise.all([...sources].map((source) => source.flushRecoveryWrites()));
  sources.clear();
});

/** An explicit source lifetime for Composer mounts outside the workspace. */
export function createTestComposerSource(ref: string, ownerId?: string): ComposerSourceState {
  const source = createComposerSourceState(ref, ownerId);
  sources.add(source);
  return source;
}
