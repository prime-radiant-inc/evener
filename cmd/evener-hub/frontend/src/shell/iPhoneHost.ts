import { useSyncExternalStore } from "react";
import { isTextEntryTarget } from "../keybindings/dispatcher";
import { navigate, refParam, urlToPane } from "./routing";
import { openSessionByRef } from "./sessionPlacement";

export interface IPhoneHeader {
  id: string;
  title: string;
  leading: "back" | "sessions" | null;
  needsYou: number;
  collapsed: boolean;
  height: number;
}

export type IPhoneHeaderAction = NonNullable<IPhoneHeader["leading"]> | "find" | "overview";

type HostMessage =
  | { type: "ready" | "hubMenu" }
  | { type: "appearance"; dark: boolean }
  | { type: "sessionHeader"; header: IPhoneHeader | null }
  | { type: "route"; path: string }
  | { type: "editing" | "readingDocument" | "covered"; active: boolean }
  | { type: "shareDocument"; ref: string; path: string; namePath: string }
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

function observeToolbar(update: () => void) {
  const observer = new MutationObserver(update);
  observer.observe(document.body, { attributes: true, attributeFilter: ["data-iphone-edge-toolbar"] });
  return () => observer.disconnect();
}

export function useIPhoneEdgeToolbar() {
  return useSyncExternalStore(observeToolbar, () => document.body.hasAttribute("data-iphone-edge-toolbar"));
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
): { type?: unknown; path?: unknown; id?: unknown; action?: unknown; glass?: unknown; edgeToolbar?: unknown } | null {
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
    document.body.toggleAttribute("data-iphone-edge-toolbar", command.edgeToolbar === true);
    return;
  }
  if (command?.type !== "navigate" || typeof command.path !== "string") return;
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

export function connectIPhoneHost(): (() => void) | undefined {
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
  window.addEventListener("message", receiveHostCommand);
  tellIPhoneHost({ type: "ready" });
  trackEditor(document.activeElement);
  reportModal();
  return () => {
    document.removeEventListener("focusin", onFocus);
    document.removeEventListener("focusout", onBlur);
    document.removeEventListener("input", onInput);
    window.removeEventListener("message", receiveHostCommand);
    editorChanges.disconnect();
    modalChanges.disconnect();
    if (modal) tellIPhoneHost({ type: "covered", active: false });
    delete document.body.dataset.iphoneGlass;
    document.body.removeAttribute("data-iphone-edge-toolbar");
    reportEditor(null);
  };
}
