/** Every `data-reason` rendered inside `container`, in document order, so a
 * test can tell which notice a section is showing (load failed, refresh
 * failed, host gone, ...) without reading its copy. */
export function shownReasons(container: ParentNode = document): (string | null)[] {
  return [...container.querySelectorAll("[data-reason]")].map((element) => element.getAttribute("data-reason"));
}
