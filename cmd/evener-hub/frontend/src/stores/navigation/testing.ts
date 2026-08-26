import type { NavigationCapability, NavigationManifest } from "../../protocol/types.gen";
import type { NavigationResponse, ResourceKey } from "./types";
export const capability = (generationId = "generation_test", version = 1): NavigationCapability => ({
  version,
  generationId,
  sequence: 0,
});
export const response = <T>(
  data: T,
  generationID = "generation_test",
  revision = 1,
  etag = '"test"',
): NavigationResponse<T> => ({ status: 200, generationID, revision, etag, data });
export const manifest = (overrides: Partial<NavigationManifest> = {}): NavigationManifest => ({
  generation_id: "generation_test",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  ...overrides,
});
export const key = (_kind: ResourceKey["kind"]): ResourceKey => ({ kind: "manifest" }) as ResourceKey;
