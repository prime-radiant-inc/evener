/** The `field` id of every SettingsField row inside `container`, in document
 * order, so a test can tell which rows rendered without reading their labels. */
export function settingsFieldIds(container: ParentNode = document): (string | null)[] {
  return [...container.querySelectorAll("[data-field]")].map((row) => row.getAttribute("data-field"));
}

/** The SettingsField row for `field`; throws when it did not render. */
export function settingsFieldRow(field: string, container: ParentNode = document): HTMLElement {
  const row = container.querySelector<HTMLElement>(`[data-field="${field}"]`);
  if (row === null) throw new Error(`no SettingsField row for "${field}"`);
  return row;
}
