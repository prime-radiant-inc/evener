import type { ThreadModel } from "../../protocol/model";
import { selectSessionSummary } from "../../stores/navigation/selectors";
import { type NavigationStoreState, navigationStore } from "../../stores/navigation/store";

/** Resolve a title without expanding a project. Live thread data always wins. */
export function resolveThreadName(
  threads: Map<string, ThreadModel>,
  navigation: unknown,
  ref: string,
): string | undefined {
  const live = threads.get(ref)?.name;
  if (live !== undefined) return live;
  if (navigation && typeof navigation === "object" && "title" in navigation) {
    const title = (navigation as { title?: unknown }).title;
    if (typeof title === "string") return title;
  }
  return undefined;
}

/** Return a summary only from already-loaded bounded navigation resources. */
export function navigationSummaryFor(ref: string, state: NavigationStoreState = navigationStore.getState()) {
  return selectSessionSummary(ref, state) ?? undefined;
}
