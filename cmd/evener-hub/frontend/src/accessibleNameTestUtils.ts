import { type ByRoleMatcher, within } from "@testing-library/react";

/** The accessible name assistive tech hears for `element` in `role`: computed
 * by Testing Library's role query (an aria-label overrides the content, and
 * aria-hidden content drops out), not read off textContent. Throws when the
 * element is not exposed in that role at all. */
export function accessibleName(element: HTMLElement, role: ByRoleMatcher): string {
  let name: string | undefined;
  within(element.parentElement ?? document.body).getAllByRole(role, {
    name: (computed, candidate) => {
      if (candidate !== element) return false;
      name = computed;
      return true;
    },
  });
  if (name === undefined) throw new Error("element is not exposed in that role");
  return name;
}

/** What assistive tech reads for an element announced by its content - a
 * disclosure's <summary> (Testing Library maps no role to it, so the role
 * query above cannot reach it) or a card's text label: the content minus
 * anything aria-hidden. A label override would replace that content, so one
 * fails here rather than being silently ignored. */
export function contentName(element: HTMLElement): string {
  if (element.hasAttribute("aria-label") || element.hasAttribute("aria-labelledby")) {
    throw new Error("element carries a label override; read its name through a role query instead");
  }
  if (element.closest('[aria-hidden="true"]')) return "";
  const visible = (node: Node): string => {
    if (node instanceof Element && node.getAttribute("aria-hidden") === "true") return "";
    if (node.nodeType === Node.TEXT_NODE) return node.textContent ?? "";
    return [...node.childNodes].map(visible).join("");
  };
  return visible(element);
}
