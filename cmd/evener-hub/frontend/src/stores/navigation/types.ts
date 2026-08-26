import type {
  NavigationInvalidationTarget,
  NavigationProjectPage,
  NavigationProjectResource,
  NavigationProjectSummary,
} from "../../protocol/types.gen";

export type ResourceKey =
  | "manifest"
  | `section:${string}`
  | `pin_catalog:${string}`
  | `pin_section:${string}`
  | `catalog:${string}`
  | `project:${string}`
  | `project_page:${string}:${string}:${number}`;

export type ResourceValue = NavigationProjectSummary[] | NavigationProjectPage | NavigationProjectResource | unknown;

export interface ResourceState<T = ResourceValue> {
  key: ResourceKey;
  data?: T;
  error?: unknown;
  loadedRevision: number;
  targetRevision: number;
  etag?: string;
  loading: boolean;
  generationID: string;
}

export type NavigationRequest<T = ResourceValue> = (
  signal: AbortSignal,
  etag?: string,
) => Promise<NavigationResponse<T>>;

export interface NavigationResponse<T = ResourceValue> {
  status: number;
  revision?: number;
  generationID?: string;
  generation_id?: string;
  etag?: string;
  data?: T;
  value?: T;
}

export type ResourceListener = (key: ResourceKey, state: ResourceState) => void;

export function resourceKey(target: NavigationInvalidationTarget): ResourceKey | undefined {
  switch (target.kind) {
    case "manifest":
      return "manifest";
    case "section":
      return target.section ? `section:${target.section}` : undefined;
    case "pin_catalog":
      return target.catalog ? `pin_catalog:${target.catalog}` : undefined;
    case "pin_section":
      return target.sectionId ? `pin_section:${target.sectionId}` : undefined;
    case "catalog":
      return target.catalog ? `catalog:${target.catalog}` : undefined;
    case "project":
      return target.projectKey ? `project:${target.projectKey}` : undefined;
    default:
      return undefined;
  }
}

export function projectKeyFromResource(key: ResourceKey): string | undefined {
  if (key.startsWith("project:")) return key.slice("project:".length);
  if (key.startsWith("project_page:")) return key.split(":")[1];
  return undefined;
}

export function isProjectResource(key: ResourceKey): boolean {
  return key.startsWith("project:") || key.startsWith("project_page:");
}
