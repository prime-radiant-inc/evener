import { computeAccessibleName } from "dom-accessibility-api";

/** The accessible name assistive tech hears for `element`, under the full
 * accname rules (dom-accessibility-api, the computation Testing Library's own
 * role queries use): aria-labelledby and aria-label override the content, and
 * content that is hidden, display: none or aria-hidden drops out. Read this
 * instead of textContent whenever a test claims something is announced. */
export function accessibleName(element: Element): string {
  return computeAccessibleName(element);
}
