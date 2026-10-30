import { useEffect, useRef, useState } from "react";
import { conversationOpenParentRef, workspaceStore } from "../shell/workspace";
import { selectLocation } from "../stores/navigation/selectors";
import { navigationStore, useNavigationStore } from "../stores/navigation/store";

type LocationResource = ReturnType<ReturnType<typeof selectLocation>>;

function workspaceSnapshot() {
  const state = workspaceStore.getState();
  const panes = state.panes.map((pane) => ({
    id: pane.id,
    type: pane.type,
    slot: pane.slot,
    ref: (pane.params as { ref?: string }).ref ?? null,
    parentRef: conversationOpenParentRef(pane) ?? null,
  }));
  return {
    panes,
    focusedPaneId: state.focusedPaneId,
    focusedRef: panes.find((p) => p.id === state.focusedPaneId)?.ref,
  };
}

function visible(element: Element | null) {
  if (!element) return false;
  const rect = element.getBoundingClientRect();
  return element.checkVisibility() && rect.width > 0 && rect.height > 0;
}

function domSnapshot() {
  return [...document.querySelectorAll<HTMLElement>("[data-file-links-source]")].map((wrapper) => ({
    paneId: wrapper.dataset.fileLinksSource,
    visible: visible(wrapper),
    scaffolds: [...wrapper.querySelectorAll<HTMLElement>("[data-pane-scaffold]")].map((el) => el.dataset.paneScaffold),
    composers: [...wrapper.querySelectorAll<HTMLElement>("[data-composer]")].map((composer) => ({
      ref: composer.dataset.composer,
      visible: visible(composer),
      textbox: visible(composer.querySelector('[role="textbox"]')),
    })),
  }));
}

function createProbe(ownerRef: string, childRef: string) {
  const identities = new WeakMap<object, number>();
  let nextIdentity = 0;
  function resourceSnapshot(resource: LocationResource) {
    if (!resource) return null;
    let identity = identities.get(resource);
    if (identity === undefined) {
      identity = ++nextIdentity;
      identities.set(resource, identity);
    }
    return {
      identity,
      key: resource.key,
      loadedRevision: resource.loadedRevision,
      loading: resource.loading,
      stale: resource.stale,
      error: resource.error,
      data: resource.data,
    };
  }
  const trace: {
    seq: number;
    at: number;
    kind: string;
    pathname: string;
    workspace: ReturnType<typeof workspaceSnapshot>;
    detail: unknown;
  }[] = [];
  const api = {
    observing: false,
    resourceSnapshot,
    record(kind: string, detail: unknown = null) {
      const event = {
        seq: trace.length,
        at: performance.now(),
        kind,
        pathname: location.pathname,
        workspace: workspaceSnapshot(),
        detail,
      };
      trace.push(event);
      return event;
    },
    snapshot() {
      return {
        observing: api.observing,
        ownerRef,
        childRef,
        viewport: { width: innerWidth, height: innerHeight },
        pathname: location.pathname,
        workspace: workspaceSnapshot(),
        ownerLocation: resourceSnapshot(selectLocation(ownerRef)(navigationStore.getState())),
        childLocation: resourceSnapshot(selectLocation(childRef)(navigationStore.getState())),
        dom: domSnapshot(),
        trace: [...trace],
      };
    },
  };
  return api;
}

declare global {
  interface Window {
    nestedFocusProbe?: ReturnType<typeof createProbe>;
  }
}

// The fixture renders this sibling AFTER AppShell. Both subscribe to the exact
// selectLocation resource object, so this passive effect follows AppShell's
// route-placement passive effect for the commit that consumes that resource.
// Resource publication and preexisting child DOM alone are not this boundary.
export function NestedFocusProbe({ ownerRef, childRef }: { ownerRef: string; childRef: string }) {
  const [probe] = useState(() => createProbe(ownerRef, childRef));
  const [pathname, setPathname] = useState(() => location.pathname);
  const ownerResource = useNavigationStore(selectLocation(ownerRef));
  const childResource = useNavigationStore(selectLocation(childRef));
  const acknowledged = useRef(new Map<string, LocationResource>());

  useEffect(() => {
    window.nestedFocusProbe = probe;
    probe.observing = true;
    probe.record("observer-start");
    const unsubscribeWorkspace = workspaceStore.subscribe((state, previous) => {
      probe.record(state.focusedPaneId !== previous.focusedPaneId ? "focus" : "workspace");
    });
    let previousOwner = selectLocation(ownerRef)(navigationStore.getState());
    let previousChild = selectLocation(childRef)(navigationStore.getState());
    const unsubscribeNavigation = navigationStore.subscribe((state) => {
      for (const [ref, previous] of [
        [ownerRef, previousOwner],
        [childRef, previousChild],
      ] as const) {
        const resource = selectLocation(ref)(state);
        if (resource !== previous) probe.record("location", { ref, resource: probe.resourceSnapshot(resource) });
      }
      previousOwner = selectLocation(ownerRef)(state);
      previousChild = selectLocation(childRef)(state);
    });
    const onRoute = () => {
      probe.record("route");
      setPathname(location.pathname);
    };
    const onClick = (event: MouseEvent) => {
      if (event.target instanceof Element && event.target.closest("[data-nested-focus-open]")) {
        probe.record("row-open-click", { trusted: event.isTrusted, button: event.button });
      }
    };
    window.addEventListener("popstate", onRoute);
    document.addEventListener("click", onClick, true);
    let lastDOM = "";
    const observeDOM = () => {
      const dom = domSnapshot();
      const key = JSON.stringify(dom);
      if (key !== lastDOM) {
        lastDOM = key;
        probe.record("dom", dom);
      }
    };
    const observer = new MutationObserver(observeDOM);
    observer.observe(document.body, { childList: true, subtree: true, attributes: true });
    observeDOM();
    return () => {
      unsubscribeWorkspace();
      unsubscribeNavigation();
      window.removeEventListener("popstate", onRoute);
      document.removeEventListener("click", onClick, true);
      observer.disconnect();
      probe.observing = false;
      probe.record("observer-stop");
    };
  }, [probe, ownerRef, childRef]);

  useEffect(() => {
    for (const [ref, resource] of [
      [ownerRef, ownerResource],
      [childRef, childResource],
    ] as const) {
      if (
        pathname !== `/s/${encodeURIComponent(ref)}` ||
        pathname !== location.pathname ||
        !resource?.data ||
        resource.loading ||
        resource.stale ||
        resource.error !== null ||
        resource.loadedRevision === null ||
        acknowledged.current.get(ref) === resource
      ) {
        continue;
      }
      const current = selectLocation(ref)(navigationStore.getState());
      if (current !== resource) continue;
      acknowledged.current.set(ref, resource);
      probe.record("post-appshell-effect", {
        ref,
        renderPathname: pathname,
        sameResourceAsRender: current === resource,
        resource: probe.resourceSnapshot(resource),
      });
    }
  }, [probe, ownerRef, childRef, ownerResource, childResource, pathname]);

  return null;
}
