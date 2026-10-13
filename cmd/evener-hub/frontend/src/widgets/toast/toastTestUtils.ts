import { getToasts } from "./store";

/** The kind of every toast raised so far, in order, so a test can tell a
 * success from a warning from a failure without reading the toast's words. */
export function toastKinds(): string[] {
  return getToasts().map((toast) => toast.kind);
}

/** The reason of every toast raised so far, in order (undefined where the
 * caller named none), for telling apart two toasts of the same kind. */
export function toastReasons(): (string | undefined)[] {
  return getToasts().map((toast) => toast.reason);
}

/** The error toasts that carry `fixture` - a value the test fed in, such as a
 * handler's own error message - through to the user. */
export function errorToastsNaming(fixture: string) {
  return getToasts().filter((toast) => toast.kind === "error" && toast.text.includes(fixture));
}
