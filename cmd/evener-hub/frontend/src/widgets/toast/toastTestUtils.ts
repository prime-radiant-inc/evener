import { getToasts, type ToastKind } from "./store";

/** The kind of every queued toast, in order, so a test can check that a
 * refusal raised an error toast without reading its copy. */
export function toastKinds(): ToastKind[] {
  return getToasts().map((toast) => toast.kind);
}

/** The error toasts that carry `fixture` - a value the test fed in, such as a
 * handler's own error message - through to the user. */
export function errorToastsNaming(fixture: string) {
  return getToasts().filter((toast) => toast.kind === "error" && toast.text.includes(fixture));
}
