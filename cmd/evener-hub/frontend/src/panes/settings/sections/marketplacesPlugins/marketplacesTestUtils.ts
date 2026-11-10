import chipStyles from "../../../../widgets/chip/chip.module.css";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { shownReasons } from "../reasonTestUtils";

const CHIP_CLASS = requireClass(chipStyles.chip, "chip.module.css", "chip");

/** The `kind` of every Chip inside `container`, in document order, so a test
 * can tell which badges a plugin row shows without reading their labels. */
export function chipKinds(container: ParentNode = document): (string | null)[] {
  return [...container.querySelectorAll(`.${CHIP_CLASS}[data-kind]`)].map((chip) => chip.getAttribute("data-kind"));
}

/** Whether `container` shows an empty-list row. */
export function showsEmptyRow(container: ParentNode = document): boolean {
  return shownReasons(container).includes("empty");
}
