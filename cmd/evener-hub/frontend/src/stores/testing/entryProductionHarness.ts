import type { ThreadModel } from "@evener/appwire-client";
import type { SessionEntryCache } from "../sessionEntryCache";
import {
  productionEntryTranscriptForTests,
  setProductionEntryCacheForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "../threads";

// Storage is tab-wide. The ref scopes read-only observations, never routing.
export function observeProductionEntryForTests(cache: SessionEntryCache, ref: string) {
  setProductionEntryCacheForTests(cache);
  return {
    settled: () => settleEntryTranscriptForTests(ref),
    inspect: () => productionEntryTranscriptForTests(ref),
    subscribeRepository: (listener: (model: ThreadModel | undefined) => void) => {
      const repository = productionEntryTranscriptForTests(ref).repository;
      return repository?.subscribe(() => listener(repository.getSnapshot())) ?? (() => {});
    },
    dispose: () => {
      threadsStore.getState().releaseThread(ref);
      const { reader, repository } = productionEntryTranscriptForTests(ref);
      reader?.dispose();
      repository?.dispose();
    },
  };
}
