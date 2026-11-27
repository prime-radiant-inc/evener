import { useEffect, useEffectEvent, useSyncExternalStore } from "react";
import { isTextEntryTarget } from "../keybindings/dispatcher";
import { navigate, refParam, urlToPane } from "./routing";
import { openSessionByRef } from "./sessionPlacement";

export const FLOATING_WEB_LAYER = ':is([data-focus-layer], [aria-modal="true"], [role="listbox"])';
const NEEDS_YOU_PATH = "/?attention=needs-you";

export interface IPhoneHeader {
  id: string;
  sessionRef: string;
  title: string;
  leading: "back" | "sessions" | null;
  needsYou: number;
  collapsed: boolean;
  height: number;
  split?: boolean;
}

export type IPhoneHeaderAction = NonNullable<IPhoneHeader["leading"]> | "find" | "overview";

type HostMessage =
  | { type: "ready" | "hubMenu" }
  | { type: "appearance"; dark: boolean }
  | { type: "sessionHeader"; header: IPhoneHeader | null }
  | { type: "sessionsToolbar"; id: string; mode: "list" | "launcher" | null }
  | {
      type: "sessionsToolbar";
      id: string;
      mode: "navigation";
      back: boolean;
      destination: "open" | "board" | null;
      needsYou: number;
    }
  | { type: "createButton"; id: string; frame: { x: number; y: number; width: number; height: number } | null }
  | { type: "route"; path: string }
  | { type: "shareTarget"; path: string }
  | { type: "sharePlaced"; path: string; id: string }
  | { type: "editing" | "readingDocument" | "covered"; active: boolean }
  | { type: "shareDocument"; ref: string; path: string; namePath: string }
  | { type: "haptic"; kind: "light" };

interface HostWindow extends Window {
  ReactNativeWebView?: { postMessage(json: string): void };
  evenerEdgeToolbar?: boolean;
}

export function iPhoneHost() {
  if (!navigator.userAgent.split(/\s+/).includes("EvenerNative/1")) return undefined;
  return (window as HostWindow).ReactNativeWebView;
}

export function hasIPhoneSessionHeader(): boolean {
  return !!iPhoneHost() && navigator.userAgent.split(/\s+/).includes("EvenerSessionHeader/1");
}

export function hasIPhoneWorkspaceControls(): boolean {
  return !!iPhoneHost() && navigator.userAgent.split(/\s+/).includes("EvenerWorkspaceControls/1");
}

export function hasIPhoneSessionsToolbar(): boolean {
  return !!iPhoneHost() && navigator.userAgent.split(/\s+/).includes("EvenerSessionsToolbar/1");
}

export function hasIPhonePaneNavigation(): boolean {
  return !!iPhoneHost() && navigator.userAgent.split(/\s+/).includes("EvenerPaneNavigation/1");
}

function observeToolbar(update: () => void) {
  window.addEventListener("evener:edge-toolbar", update);
  return () => window.removeEventListener("evener:edge-toolbar", update);
}

export function useIPhoneEdgeToolbar() {
  return useSyncExternalStore(observeToolbar, () => (window as HostWindow).evenerEdgeToolbar === true);
}

export function tellIPhoneHost(message: HostMessage): void {
  try {
    iPhoneHost()?.postMessage(JSON.stringify(message));
  } catch {
    // A WebView being dismissed can drop its native channel while the page
    // still settles a submission. Feedback must not change that outcome.
  }
}

export function reportIPhoneRoute(path: string, openNeedsYou: () => void): void {
  if (!iPhoneHost()) return;
  if (path === NEEDS_YOU_PATH) {
    // A cold launch consumed the alert. Back and restore should land on the workspace.
    navigate("/", { replace: true });
    openNeedsYou();
  } else {
    tellIPhoneHost({ type: "route", path });
  }
}

export function iPhoneCommand(event: MessageEvent): {
  type?: unknown;
  path?: unknown;
  id?: unknown;
  text?: unknown;
  action?: unknown;
  glass?: unknown;
  edgeToolbar?: unknown;
} | null {
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

/** Only the focused composer's document may accept a share addressed by the picker. */
export function useIPhoneShare(path: string | null, place: (id: string, text: string) => void): void {
  const onShare = useEffectEvent(place);
  useEffect(() => {
    if (!path || !iPhoneHost()) return;
    const receive = (event: MessageEvent) => {
      const command = iPhoneCommand(event);
      if (
        command?.type !== "shareText" ||
        command.path !== path ||
        window.location.pathname !== path ||
        typeof command.id !== "string" ||
        !command.id ||
        typeof command.text !== "string" ||
        !command.text.trim()
      )
        return;
      onShare(command.id, command.text);
    };
    window.addEventListener("message", receive);
    tellIPhoneHost({ type: "shareTarget", path });
    return () => window.removeEventListener("message", receive);
  }, [path]);
}

function receiveHostCommand(event: MessageEvent, openNeedsYou: () => void): void {
  const command = iPhoneCommand(event);
  if (command?.type === "material" && typeof command.glass === "boolean") {
    document.body.dataset.iphoneGlass = command.glass ? "regular" : "opaque";
    const host = window as HostWindow;
    if (host.evenerEdgeToolbar !== (command.edgeToolbar === true)) {
      host.evenerEdgeToolbar = command.edgeToolbar === true;
      window.dispatchEvent(new Event("evener:edge-toolbar"));
    }
    return;
  }
  if (command?.type !== "navigate" || typeof command.path !== "string") return;
  if (command.path === NEEDS_YOU_PATH) {
    // The empty palette lists Needs you first, over the user's current pane.
    openNeedsYou();
    const { pathname, search, hash } = window.location;
    tellIPhoneHost({ type: "route", path: pathname + search + hash });
    return;
  }
  let route: ReturnType<typeof urlToPane>;
  try {
    if (!command.path.startsWith("/")) return;
    const url = new URL(command.path, window.location.origin);
    if (url.origin !== window.location.origin || url.searchParams.has("token")) return;
    route = urlToPane(url.pathname);
  } catch {
    return; // Malformed percent encoding is not a workspace destination.
  }
  if (route?.type === "session") {
    const ref = refParam(route.params);
    if (ref) openSessionByRef(ref);
  } else if (route?.type === "spawn" || route?.type === "settings" || route?.type === "welcome") {
    navigate(command.path);
  }
}

export function connectIPhoneHost(openNeedsYou: () => void): (() => void) | undefined {
  if (!iPhoneHost()) return;
  // Draw solid controls until the phone has read its accessibility settings.
  document.body.dataset.iphoneGlass = "opaque";
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
  const onInput = () => reportEditor(document.activeElement);
  // A submitted ProseMirror draft clears in place without a DOM input event.
  const editorChanges = new MutationObserver(onInput);
  const trackEditor = (target: EventTarget | null) => {
    editorChanges.disconnect();
    if (
      target instanceof HTMLElement &&
      (target.isContentEditable || target.getAttribute("contenteditable") === "true")
    )
      editorChanges.observe(target, { childList: true, characterData: true, subtree: true });
    reportEditor(target);
  };
  const onFocus = (event: FocusEvent) => trackEditor(event.target);
  const onBlur = (event: FocusEvent) => trackEditor(event.relatedTarget);
  const modalSelector = '[aria-modal="true"]';
  let modal: Element | null = null;
  const reportModal = () => {
    const next = document.querySelector(`${modalSelector}:not([inert] *)`);
    if (Boolean(next) !== Boolean(modal)) tellIPhoneHost({ type: "covered", active: !!next });
    modal = next;
  };
  const modalChanges = new MutationObserver((records) => {
    if (
      (modal && !modal.isConnected) ||
      records.some(
        (record) =>
          record.type === "attributes" ||
          [...record.addedNodes].some(
            (node) => node instanceof Element && (node.matches(modalSelector) || node.querySelector(modalSelector)),
          ),
      )
    )
      reportModal();
  });
  modalChanges.observe(document.body, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["aria-modal", "inert"],
  });
  document.addEventListener("focusin", onFocus);
  document.addEventListener("focusout", onBlur);
  document.addEventListener("input", onInput);
  const onMessage = (event: MessageEvent) => receiveHostCommand(event, openNeedsYou);
  window.addEventListener("message", onMessage);
  tellIPhoneHost({ type: "ready" });
  trackEditor(document.activeElement);
  reportModal();
  return () => {
    document.removeEventListener("focusin", onFocus);
    document.removeEventListener("focusout", onBlur);
    document.removeEventListener("input", onInput);
    window.removeEventListener("message", onMessage);
    editorChanges.disconnect();
    modalChanges.disconnect();
    if (modal) tellIPhoneHost({ type: "covered", active: false });
    delete document.body.dataset.iphoneGlass;
    delete (window as HostWindow).evenerEdgeToolbar;
    reportEditor(null);
  };
}
