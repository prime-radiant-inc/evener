/** The `field` id of every settings PropertyList row inside `container` (the
 * rowProps each row's wrapper carries), in document order, so a test can tell
 * which rows rendered without reading their labels. */
export function settingsFieldIds(container: ParentNode = document): (string | null)[] {
  return [...container.querySelectorAll("[data-field]")].map((value) => value.getAttribute("data-field"));
}

/** The value definition (<dd>) of the row for `field`; throws when it did not render. */
export function settingsFieldValue(field: string, container: ParentNode = document): HTMLElement {
  const row = container.querySelector<HTMLElement>(`[data-field="${field}"]`);
  const value = row?.closest<HTMLElement>("dd") ?? row?.querySelector<HTMLElement>("dd");
  if (!value) throw new Error(`no settings row for "${field}"`);
  return value;
}

/** The help definition under the row for `field`'s value, or null when the
 * row has none: a stacked PropertyList row's help is a second definition of
 * the same term, right after its value. */
export function settingsFieldHelp(field: string, container: ParentNode = document): Element | null {
  const help = settingsFieldValue(field, container).nextElementSibling;
  return help?.tagName === "DD" ? help : null;
}
