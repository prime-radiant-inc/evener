import {
  createDocumentReadDemand,
  type DocFileContent,
  DocFileError,
  type DocFileErrorKind,
  type DocPort,
  type DocumentReadDemand,
  docImageReadURL,
  type FileReference,
  isImagePath,
  readDocFile,
} from "@evener/appwire-client/docContent";
import { useLayoutEffect, useRef, useState } from "react";
import { connectionStore, onConnectionNotification, readyConnectionTransition } from "../../stores/connection";
import { browserDocPort } from "./browserDocPort";

export interface DocumentReadState {
  readonly content: DocFileContent | undefined;
  readonly errorKind: DocFileErrorKind | undefined;
  readonly notice: string | undefined;
  readonly reference: FileReference;
  readonly imageGeneration: string | undefined;
  reload(): void;
}

interface Publication {
  identity: string;
  content?: DocFileContent;
  errorKind?: DocFileErrorKind;
  notice?: string;
  imageGeneration?: string;
}

export function useDocumentRead(
  session: string,
  reference: FileReference,
  reopen: number,
  visible: boolean,
  port: DocPort = browserDocPort,
): DocumentReadState {
  const identity = JSON.stringify([session, port.origin, reference]);
  const [publication, setPublication] = useState<Publication>();
  const current = useRef({ session, reference, port, identity, visible });
  const demand = useRef<DocumentReadDemand | undefined>(undefined);
  const abandonImage = useRef<(() => void) | undefined>(undefined);
  const previous = useRef({ identity, reopen });

  // Committed reads snapshot this binding. Rendering below independently
  // suppresses a previous identity, before any layout or passive effect runs.
  useLayoutEffect(() => {
    current.current = { session, reference, port, identity, visible };
  }, [session, reference, port, identity, visible]);

  useLayoutEffect(() => {
    const owner = createDocumentReadDemand(async (attempt) => {
      const captured = current.current;
      const canPublish = () => attempt.isCurrent() && current.current.identity === captured.identity;
      const publish = (next: Omit<Publication, "identity">, preserve = false) => {
        if (!canPublish()) return;
        setPublication((previousPublication) => ({
          ...(preserve && previousPublication?.identity === captured.identity ? previousPublication : {}),
          identity: captured.identity,
          ...next,
        }));
      };
      if (isImagePath(captured.reference.path)) {
        return new Promise((resolve) => {
          const image = new Image();
          let settled = false;
          const finish = (loaded: boolean, abandoned = false) => {
            if (settled) return;
            settled = true;
            image.onload = null;
            image.onerror = null;
            abandonImage.current = undefined;
            if (!abandoned) {
              if (loaded) publish({ imageGeneration: attempt.generation });
              else
                publish(
                  { errorKind: "error", notice: "Image could not be loaded from the session's working directory." },
                  true,
                );
            }
            resolve(loaded ? "success" : "transient");
          };
          abandonImage.current = () => {
            finish(false, true);
            image.removeAttribute("src");
          };
          image.onload = () => finish(true);
          image.onerror = () => finish(false);
          image.src = docImageReadURL(
            captured.port.origin,
            captured.session,
            captured.reference.readTarget,
            attempt.generation,
          );
        });
      }
      try {
        const content = await readDocFile(captured.session, captured.reference.readTarget, captured.port);
        publish({ content });
        return "success";
      } catch (error) {
        const errorKind = error instanceof DocFileError ? error.kind : "error";
        publish({ errorKind, notice: error instanceof Error ? error.message : String(error) }, true);
        return errorKind === "error" ? "transient" : "terminal";
      }
    });
    demand.current = owner;
    const updateActive = () =>
      owner.setActive(
        current.current.visible &&
          document.visibilityState !== "hidden" &&
          connectionStore.getState().state === "ready",
      );
    const unsubscribeConnection = connectionStore.subscribe((state, prior) => {
      updateActive();
      if (state.client !== prior.client && readyConnectionTransition(state, prior) && prior.state === "ready")
        owner.refresh();
    });
    // A resync names an exact session. There is no remote-attachment-ready
    // notification, so transient attachment failures also retain paced demand.
    const unsubscribeNotifications = onConnectionNotification((notification) => {
      if (notification.method === "evener/thread/resync" && notification.params.ref === current.current.session)
        owner.refresh();
    });
    document.addEventListener("visibilitychange", updateActive);
    updateActive();
    return () => {
      owner.dispose();
      demand.current = undefined;
      unsubscribeConnection();
      unsubscribeNotifications();
      document.removeEventListener("visibilitychange", updateActive);
      abandonImage.current?.();
    };
  }, []);

  useLayoutEffect(() => {
    const owner = demand.current;
    owner?.setActive(visible && document.visibilityState !== "hidden" && connectionStore.getState().state === "ready");
    if (previous.current.identity !== identity) {
      setPublication(undefined);
      owner?.replace();
    } else if (previous.current.reopen !== reopen) owner?.refresh();
    previous.current = { identity, reopen };
  }, [identity, reopen, visible]);

  const displayed = publication?.identity === identity ? publication : undefined;
  return {
    content: displayed?.content,
    errorKind: displayed?.errorKind,
    notice: displayed?.notice,
    reference,
    imageGeneration: displayed?.imageGeneration,
    reload: () => demand.current?.refresh(),
  };
}
