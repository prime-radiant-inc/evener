// A first page is bounded independently of the summary total; terminal history
// may be folded, so painting every historical row would be misleading and tall.
export const ACTIVITY_PLACEHOLDER_PAGE = 20;

export function activityPlaceholderLines(total: number | null | undefined): number | undefined {
  if (total === null || total === undefined || total === 0) return undefined;
  return Math.min(total, ACTIVITY_PLACEHOLDER_PAGE);
}
