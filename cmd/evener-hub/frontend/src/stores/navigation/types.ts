import type { NavigationInvalidationTarget } from "../../protocol/types.gen";

export type ResourceKey =
  | { kind: "manifest" }
  | { kind: "section"; section: "live" | "needs-you"; offset: number; limit: number }
  | { kind: "pin_catalog"; offset: number; limit: number }
  | { kind: "pin_section"; sectionId: string; offset: number; limit: number }
  | { kind: "catalog"; catalog: "projects" | "archived-projects" | "test-runs"; offset: number; limit: number }
  | { kind: "project"; projectKey: string }
  | { kind: "project_page"; projectKey: string; tier: "current" | "recent" | "archived"; offset: number; limit: number }
  | { kind: "location"; ref: string };

export interface ResourceState<T = unknown> {
  readonly key: ResourceKey;
  readonly data: T | null;
  readonly loadedRevision: number | null;
  readonly targetRevision: number | null;
  readonly etag: string | null;
  readonly loading: boolean;
  readonly stale: boolean;
  readonly error: unknown | null;
  readonly generationID: string;
}

export interface NavigationResponse<T = unknown> {
  status: 200 | 304;
  generationID: string;
  revision: number;
  etag: string;
  data?: T;
}

export type NavigationRequest<T = unknown> = (
  signal: AbortSignal,
  etag: string | null,
) => Promise<NavigationResponse<T>>;
export type ResourceListener = (state: ResourceState) => void;

export function resourceKey(target: NavigationInvalidationTarget): ResourceKey | undefined {
  switch (target.kind) {
    case "manifest":
      return { kind: "manifest" };
    case "section":
      return target.section === "live" || target.section === "needs-you"
        ? { kind: "section", section: target.section, offset: 0, limit: 50 }
        : undefined;
    case "pin_catalog":
      return { kind: "pin_catalog", offset: 0, limit: 50 };
    case "pin_section":
      return target.sectionId ? { kind: "pin_section", sectionId: target.sectionId, offset: 0, limit: 50 } : undefined;
    case "catalog":
      return target.catalog === "projects" || target.catalog === "archived-projects" || target.catalog === "test-runs"
        ? { kind: "catalog", catalog: target.catalog, offset: 0, limit: 50 }
        : undefined;
    case "project":
      return target.projectKey ? { kind: "project", projectKey: target.projectKey } : undefined;
    default:
      return undefined;
  }
}

export function keyID(key: ResourceKey): string {
  return JSON.stringify(key);
}
export function isProjectResource(key: ResourceKey): boolean {
  return key.kind === "project" || key.kind === "project_page";
}
