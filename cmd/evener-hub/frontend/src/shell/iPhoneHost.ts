import { isTextEntryTarget } from "../keybindings/dispatcher";
import { navigate, refParam, urlToPane } from "./routing";
import { openSessionByRef } from "./sessionPlacement";

type HostMessage =
  | { type: "ready" | "hubMenu" }
  | { type: "route"; path: string }
  | { type: "editing"; active: boolean }
  | { type: "shareDocument"; ref: string; path: string }
  | { type: "haptic"; kind: "light" };

interface HostWindow extends Window {
  ReactNativeWebView?: { postMessage(json: string): void };
}

export function iPhoneHost() {
  if (!navigator.userAgent.split(/\s+/).includes("EvenerNative/1")) return undefined;
  return (window as HostWindow).ReactNativeWebView;
}

export function tellIPhoneHost(message: HostMessage): void {
  try {
    iPhoneHost()?.postMessage(JSON.stringify(message));
  } catch {
    // A WebView being dismissed can drop its native channel while the page
    // still settles a submission. Feedback must not change that outcome.
  }
}

function receiveNavigation(event: MessageEvent): void {
  // WebKit dispatches the native command directly on this window. Messages
  // posted by another document carry its origin and window.
  if (event.source !== null || event.origin !== "" || typeof event.data !== "string") return;
  let route: ReturnType<typeof urlToPane>;
  try {
    const message = JSON.parse(event.data);
    if (message?.type !== "navigate" || typeof message.path !== "string") return;
    route = urlToPane(message.path);
  } catch {
    return;
  }
  if (route?.type === "session") {
    const ref = refParam(route.params);
    if (ref) openSessionByRef(ref);
  } else if (route?.type === "spawn") navigate("/new");
  else if (route?.type === "welcome") navigate("/");
}

export function connectIPhoneHost(): (() => void) | undefined {
  if (!iPhoneHost()) return;
  let editing = false;
  const reportEditor = (target: EventTarget | null) => {
    let active = false;
    if (target instanceof HTMLElement && isTextEntryTarget(target)) {
      active =
        target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement
          ? !target.readOnly && target.value.trim() !== ""
          : (target.textContent ?? "").trim() !== "";
    }
    if (editing === active) return;
    editing = active;
    tellIPhoneHost({ type: "editing", active });
  };
  const onFocus = (event: FocusEvent) => reportEditor(event.target);
  const onBlur = (event: FocusEvent) => reportEditor(event.relatedTarget);
  const onInput = () => reportEditor(document.activeElement);
  document.addEventListener("focusin", onFocus);
  document.addEventListener("focusout", onBlur);
  document.addEventListener("input", onInput);
  window.addEventListener("message", receiveNavigation);
  tellIPhoneHost({ type: "ready" });
  onInput();
  return () => {
    document.removeEventListener("focusin", onFocus);
    document.removeEventListener("focusout", onBlur);
    document.removeEventListener("input", onInput);
    window.removeEventListener("message", receiveNavigation);
    reportEditor(null);
  };
}
