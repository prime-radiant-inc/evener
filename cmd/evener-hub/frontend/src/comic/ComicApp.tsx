import { useEffect, useState } from "react";
import { ConnectionBanner } from "../shell/ConnectionBanner";
import { ToastRegion } from "../shell/chrome/ToastRegion";
import { ClientProvider } from "../shell/clientContext";
import { useBrowserConnection } from "../shell/useBrowserConnection";
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
  const { client, onClientReplaced } = useBrowserConnection({});
  const connectionState = useConnectionStore((state) => state.state);
  const [route, setRoute] = useState(currentRoute);
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
    const onPopState = () => setRoute(currentRoute());
    window.addEventListener("popstate", onPopState);
    onPopState();
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const selectRef = (ref: string) => {
    window.history.pushState(null, "", ref ? comicSessionURL(ref) : "/comic");
    setRoute(currentRoute());
  };
  const replaceRef = (ref: string) => {
    window.history.replaceState(null, "", comicSessionURL(ref));
    setRoute(currentRoute());
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
          <InlineNotice live="status" aria-label="Comic font setup">
            Comic Sans could not load. For a personal build, place Comic.TTF and Comicbd.TTF in
            public/webassets/comic-local/. The conversation remains available in the fallback font.
          </InlineNotice>
        )}
        {route.kind === "invalid" ? (
          <EmptyState title="Invalid conversation link" reason="not-found" />
        ) : route.kind === "picker" ? (
          <ComicPicker onSelect={selectRef} />
        ) : (
          <ComicSession key={route.ref} sessionRef={route.ref} onSelectRef={selectRef} onReplaceRef={replaceRef} />
        )}
      </main>
    </ClientProvider>
  );
}
