import { WireError } from "../../protocol/errors";
import type { NavigationInvalidationTarget } from "../../protocol/types.gen";

const NAVIGATION_UNAVAILABLE_CODE = -32014;

export function isNavigationUnavailable(error: unknown): boolean {
  return (
    error instanceof WireError &&
    error.code === NAVIGATION_UNAVAILABLE_CODE &&
    error.evenerErrorInfo === "actionUnavailable"
  );
}

export type ResourceKey =
  | { kind: "manifest" }
  | { kind: "section"; section: "live" | "needs_you"; offset: number; limit: number }
  | { kind: "pin_catalog"; offset: number; limit: number }
  | { kind: "pin_section"; sectionId: string; offset: number; limit: number }
  | { kind: "catalog"; catalog: "projects" | "archived_projects" | "test_runs"; offset: number; limit: number }
  | { kind: "project"; projectKey: string }
  | { kind: "project_page"; projectKey: string; tier: "current" | "recent" | "archived"; offset: number; limit: number }
  | { kind: "location"; ref: string };

export interface ResourceState<T = unknown> {
  readonly key: ResourceKey;
  readonly data: T | null;
  readonly loadedRevision: number | null;
  readonly targetRevision: number | null;
  readonly forceToken: number;
  readonly etag: string | null;
  readonly loading: boolean;
  readonly stale: boolean;
  readonly error: unknown | null;
  readonly generationID: string;
}

export interface NavigationResponse<T = unknown> {
  status: number;
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
export function keyID(key: ResourceKey): string {
  const fields: Record<string, unknown> = { kind: key.kind };
  for (const name of ["catalog", "limit", "offset", "projectKey", "ref", "section", "sectionId", "tier"]) {
    if (name in key) fields[name] = (key as Record<string, unknown>)[name];
  }
  return JSON.stringify(fields);
}
export function isProjectResource(key: ResourceKey): boolean {
  return key.kind === "project" || key.kind === "project_page";
}
export function targetBase(target: NavigationInvalidationTarget): Partial<ResourceKey> | undefined {
  switch (target.kind) {
    case "manifest":
      return { kind: "manifest" };
    case "section":
      return target.section === "live" || target.section === "needs_you"
        ? { kind: "section", section: target.section }
        : undefined;
    case "pin_catalog":
      return { kind: "pin_catalog" };
    case "pin_section":
      return target.sectionId ? { kind: "pin_section", sectionId: target.sectionId } : undefined;
    case "catalog":
      return target.catalog === "projects" || target.catalog === "archived_projects" || target.catalog === "test_runs"
        ? { kind: "catalog", catalog: target.catalog }
        : undefined;
    case "project":
      return target.projectKey ? { kind: "project", projectKey: target.projectKey } : undefined;
    default:
      return undefined;
  }
}
