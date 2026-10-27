import { useCallback, useEffect, useRef, useState } from "react";
import { ConnectionBanner } from "../shell/ConnectionBanner";
import { ToastRegion } from "../shell/chrome/ToastRegion";
import { ClientProvider } from "../shell/clientContext";
import { useBrowserConnection } from "../shell/useBrowserConnection";
import { useKeyboardInset } from "../shell/useKeyboardInset";
import { useConnectionStore } from "../stores/connection";
import { EmptyState, InlineNotice } from "../widgets";
import { ComicPicker } from "./ComicPicker";
import { ComicSession } from "./ComicSession";
import styles from "./comic.module.css";
import { loadComicFonts } from "./measure";
import { type ComicRoute, comicSessionURL, parseComicRoute } from "./route";

function currentRoute(): ComicRoute {
  return parseComicRoute(window.location.pathname) ?? { kind: "invalid" };
}

export function ComicApp(): React.ReactElement {
  useKeyboardInset();
  const { client, onClientReplaced } = useBrowserConnection({});
  const connectionState = useConnectionStore((state) => state.state);
  const [selection, setSelection] = useState(() => ({ route: currentRoute(), generation: 0 }));
  const selected = useRef(selection);
  const { route } = selection;
  const readSelection = useCallback(() => {
    const next = { route: currentRoute(), generation: selected.current.generation + 1 };
    selected.current = next;
    setSelection(next);
  }, []);
  const [fontStatus, setFontStatus] = useState<"loading" | "ready" | "unavailable">("loading");
  useEffect(() => {
    let active = true;
    void loadComicFonts().then((result) => {
      if (active) setFontStatus(result);
    });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    const onPopState = readSelection;
    window.addEventListener("popstate", onPopState);
    onPopState();
    return () => window.removeEventListener("popstate", onPopState);
  }, [readSelection]);

  const selectRef = (ref: string) => {
    window.history.pushState(null, "", ref ? comicSessionURL(ref) : "/comic");
    readSelection();
  };
  const replaceRef = (ref: string) => {
    const current = currentRoute();
    if (
      selected.current !== selection ||
      route.kind !== "session" ||
      current.kind !== "session" ||
      current.ref !== route.ref
    )
      return false;
    window.history.replaceState(null, "", comicSessionURL(ref));
    readSelection();
    return true;
  };

  return (
    <ClientProvider client={client}>
      <ConnectionBanner state={connectionState} onClientReplaced={onClientReplaced} />
      <ToastRegion />
      <main
        className={styles.comicRoot}
        data-theme="light"
        data-comic-route={route.kind}
        data-comic-font={fontStatus}
        data-session-ref={route.kind === "session" ? route.ref : undefined}
      >
        {fontStatus === "unavailable" && (
          <InlineNotice live="status" aria-label="Comic font unavailable">
            Comic Sans could not load. The conversation remains available in the fallback font.
          </InlineNotice>
        )}
        {route.kind === "invalid" ? (
          <EmptyState title="Invalid conversation link" reason="not-found" />
        ) : route.kind === "picker" ? (
          <ComicPicker onSelect={selectRef} />
        ) : (
          <ComicSession
            key={`${selection.generation}:${route.ref}`}
            sessionRef={route.ref}
            onSelectRef={selectRef}
            onReplaceRef={replaceRef}
          />
        )}
      </main>
    </ClientProvider>
  );
}
