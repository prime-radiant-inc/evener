import { shownReasons } from "../reasonTestUtils";

/** The `kind` of every Chip inside `container`, in document order, so a test
 * can tell which badges a plugin row shows without reading their labels. */
export function chipKinds(container: ParentNode = document): (string | null)[] {
  return [...container.querySelectorAll("[data-kind]")].map((chip) => chip.getAttribute("data-kind"));
}

/** Whether `container` shows an empty-list row. */
export function showsEmptyRow(container: ParentNode = document): boolean {
  return shownReasons(container).includes("empty");
}
