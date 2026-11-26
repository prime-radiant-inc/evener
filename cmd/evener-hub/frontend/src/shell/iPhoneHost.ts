import { isTextEntryTarget } from "../keybindings/dispatcher";
import { connectIPhoneGlass } from "./iPhoneGlass";
import { navigate, refParam, urlToPane } from "./routing";
import { openSessionByRef } from "./sessionPlacement";

export interface IPhoneHeader {
  id: string;
  title: string;
  leading: "back" | "sessions";
  needsYou: number;
  collapsed: boolean;
  height: number;
}

export type IPhoneHeaderAction = IPhoneHeader["leading"] | "find" | "overview";

type HostMessage =
  | { type: "ready" | "hubMenu" }
  | { type: "appearance"; dark: boolean }
  | { type: "sessionHeader"; header: IPhoneHeader | null }
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

export function hasIPhoneSessionHeader(): boolean {
  return !!iPhoneHost() && navigator.userAgent.split(/\s+/).includes("EvenerSessionHeader/1");
}

export function tellIPhoneHost(message: HostMessage): void {
  try {
    iPhoneHost()?.postMessage(JSON.stringify(message));
  } catch {
    // A WebView being dismissed can drop its native channel while the page
    // still settles a submission. Feedback must not change that outcome.
  }
}

export function iPhoneCommand(
  event: MessageEvent,
): { type?: unknown; path?: unknown; id?: unknown; action?: unknown; glass?: unknown } | null {
  // WebKit dispatches the native command directly on this window. Messages
  // posted by another document carry its origin and window.
  if (event.source !== null || event.origin !== "" || typeof event.data !== "string") return null;
  try {
    const command = JSON.parse(event.data);
    return command && typeof command === "object" ? command : null;
  } catch {
    return null;
  }
}

function receiveHostCommand(event: MessageEvent): void {
  const command = iPhoneCommand(event);
  if (command?.type === "material" && typeof command.glass === "boolean") {
    document.body.dataset.iphoneGlass = command.glass ? "regular" : "opaque";
    return;
  }
  if (command?.type !== "navigate" || typeof command.path !== "string") return;
  let route: ReturnType<typeof urlToPane>;
  try {
    route = urlToPane(command.path);
  } catch {
    return; // Malformed percent encoding is not a workspace destination.
  }
  if (route?.type === "session") {
    const ref = refParam(route.params);
    if (ref) openSessionByRef(ref);
  } else if (route?.type === "spawn") navigate("/new");
  else if (route?.type === "welcome") navigate("/");
}

export function connectIPhoneHost(): (() => void) | undefined {
  if (!iPhoneHost()) return;
  // Draw solid controls until the phone has read its accessibility settings.
  document.body.dataset.iphoneGlass = "opaque";
  const stopGlass = connectIPhoneGlass();
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
  window.addEventListener("message", receiveHostCommand);
  tellIPhoneHost({ type: "ready" });
  onInput();
  return () => {
    document.removeEventListener("focusin", onFocus);
    document.removeEventListener("focusout", onBlur);
    document.removeEventListener("input", onInput);
    window.removeEventListener("message", receiveHostCommand);
    stopGlass();
    delete document.body.dataset.iphoneGlass;
    reportEditor(null);
  };
}
