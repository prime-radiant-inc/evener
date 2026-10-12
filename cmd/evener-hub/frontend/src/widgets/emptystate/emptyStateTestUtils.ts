/** The `reason` of every EmptyState rendered inside `container`, in document
 * order, so a test can tell loading from deleted from unavailable without
 * reading the copy. */
export function emptyStateReasons(container: HTMLElement): (string | null)[] {
  return [...container.querySelectorAll('[data-testid="empty-state"]')].map((empty) =>
    empty.getAttribute("data-reason"),
  );
}
