/** Every `data-reason` rendered inside `container`, in document order, so a
 * test can tell which notice a section is showing (load failed, refresh
 * failed, host gone, ...) without reading its copy. */
export function shownReasons(container: ParentNode = document): (string | null)[] {
  return [...container.querySelectorAll("[data-reason]")].map((element) => element.getAttribute("data-reason"));
}

/** The `data-reason` of every alert inside `container`, in document order. A
 * section's failure is a danger InlineNotice (role=alert), never an
 * EmptyState, so this is the failure-side twin of emptyStateReasons. */
export function alertReasons(container: ParentNode = document): (string | null)[] {
  return [...container.querySelectorAll('[role="alert"]')].map((element) => element.getAttribute("data-reason"));
}

/** The `data-reason` of every polite status notice inside `container`, in
 * document order. A load that fails as a section opens is reported this way,
 * so a screen reader is not interrupted by an error nobody acted to cause. */
export function statusReasons(container: ParentNode = document): (string | null)[] {
  return [...container.querySelectorAll('[role="status"][data-reason]')].map((element) =>
    element.getAttribute("data-reason"),
  );
}
