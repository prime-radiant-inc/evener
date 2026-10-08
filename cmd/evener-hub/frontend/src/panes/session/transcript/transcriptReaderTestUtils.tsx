import { makeTranscriptDisplayConfig, type ThreadReadResponse } from "@evener/appwire-client";
import { act, fireEvent, render } from "@testing-library/react";
import { createRef } from "react";
import { useStore } from "zustand";
import { installKeybindings } from "../../../shell/installKeybindings";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { type OpenPaneRecord, workspaceStore } from "../../../shell/workspace";
import { transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import { type CommittedVirtualListLayout, VirtualList, type VirtualListHandle } from "../../../widgets";
import { type CapturedTranscriptView, captureTranscriptView } from "./flow/transcriptViewRegistry";
import { useTranscriptScroll, useTranscriptViewRegistration } from "./flow/useTranscriptScroll";
import { useTranscriptScrollKeys } from "./flow/useTranscriptScrollKeys";
import { TranscriptBody } from "./TranscriptBody";
import { holdReaderFrames, installTranscriptGeometry } from "./transcriptReadingGeometryTestUtils";
import { retainedTranscriptReadView } from "./transcriptReadView";

/** A real retained reader, shared gesture hook and focused key dispatcher. */
export function mountReaderScene(
  id: string,
  rowHeights = [1600, 1000],
  widget?: { estimate: number; viewportHeight: number },
) {
  const pane: OpenPaneRecord = { id, type: "transcript", params: { ref: id }, slot: "main" };
  workspaceStore.setState((state) => ({ panes: [...state.panes, pane], focusedPaneId: id }));
  const lifetime = conversationPaneLifetime(pane);
  const view = retainedTranscriptReadView(lifetime, id, "transcript");
  const geometry = { width: 152, viewportHeight: widget?.viewportHeight ?? 400, rowHeights };
  const external = installTranscriptGeometry(() => geometry);
  const frames = holdReaderFrames();
  const listRef = createRef<VirtualListHandle>();
  const model = {
    ...makeTranscriptPreviewModel(),
    ref: id,
    turns: rowHeights.map((_, index) => {
      const name = index === 0 ? "current" : `tail-${index}`;
      return {
        id: name,
        status: "completed",
        items: [{ id: `${name}-entry`, turnId: name, type: "userMessage", text: name, status: "completed" }],
      };
    }),
  };
  const anchorEntries = model.turns.map((turn, index) => ({
    id: turn.items[0]?.id ?? "",
    index,
    sourceIndex: index,
    isMessage: true,
  }));
  let layout: CommittedVirtualListLayout | undefined;
  let scrollFlow: ReturnType<typeof useTranscriptScroll> | undefined;
  const tools = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  function Reader({ initial }: { initial?: CapturedTranscriptView }) {
    const config = useStore(transcriptDisplayStore, (state) => state.local.desktop ?? tools);
    const flow = useTranscriptScroll({
      ref: id,
      model,
      listRef,
      initialViewCapture: initial,
      loadOlder: async () => {},
      renderedRowCount: model.turns.length,
      anchorEntries,
      onReaderIntent: view.supersedePositioning,
      onReaderMovement: view.syncPositioningMovement,
    });
    scrollFlow = flow;
    useTranscriptScrollKeys({
      paneId: id,
      listRef,
      jumpToBottom: flow.jumpToBottom,
      markGesture: flow.markGesture,
      onPositioningCommand: view.supersedePositioning,
    });
    const registration = useTranscriptViewRegistration({
      enabled: widget !== undefined,
      id: view.id,
      readView: view,
      listRef,
      renderedRowCount: model.turns.length,
      anchorEntries,
      initialViewCapture: initial,
    });
    return (
      <>
        <textarea aria-label="Neighbor editor" defaultValue="keep this selection" />
        <button type="button">Inspector focus</button>
        <button type="button" onClick={flow.jumpToBottom}>
          Jump to live
        </button>
        {widget ? (
          <section data-testid="transcript-virtual-list">
            <VirtualList
              ref={listRef}
              dynamic
              anchorToEnd={!registration.hasRetainedPlacement()}
              count={rowHeights.length}
              estimateSize={() => widget.estimate}
              getItemKey={(index) => model.turns[index]?.id ?? index}
              renderRow={(index) => (
                <div
                  data-view-anchor-id={anchorEntries[index]?.id}
                  data-view-anchor-index={index}
                  data-view-anchor-source-index={index}
                  data-view-anchor-message="true"
                >
                  {model.turns[index]?.items[0]?.text}
                </div>
              )}
              onChange={flow.restoreViewAnchorAfterMeasurement}
              onLayout={(value) => {
                layout = value;
                registration.restoreAfterLayout(value);
              }}
            />
          </section>
        ) : (
          <TranscriptBody
            model={model}
            config={config}
            surface="readOnly"
            disclosureScope={view.id}
            viewId={view.id}
            readView={view}
            initialViewCapture={initial}
            listRef={listRef}
            onMeasurementsChange={flow.restoreViewAnchorAfterMeasurement}
          />
        )}
      </>
    );
  }
  installKeybindings();
  view.setReadable(true);
  let mounted = render(<Reader />);
  const port = () => {
    const element = listRef.current?.getScrollElement();
    if (!element) throw new Error("Actual retained reader has no scroll port");
    return element;
  };
  return {
    view,
    lifetime,
    pane,
    geometry,
    external,
    frames,
    listRef,
    port,
    flow() {
      if (!scrollFlow) throw new Error("Actual shared scroll hook has not rendered");
      return scrollFlow;
    },
    layout() {
      if (!layout) throw new Error("Actual committed widget payload has not published");
      return layout;
    },
    async start(offset = 900) {
      await act(async () => external.notify());
      await act(async () => frames.release());
      await act(async () => frames.release());
      await act(async () => {
        fireEvent.wheel(port(), { deltaY: -100 });
        port().scrollTop = offset + 100;
        fireEvent.scroll(port());
      });
      await act(async () => {
        port().scrollTop = offset;
        fireEvent.scroll(port());
      });
    },
    async holdReflow(nextHeight = 700) {
      geometry.width = 352;
      geometry.rowHeights[0] = nextHeight;
      await act(async () => external.notify((target) => target === port()));
    },
    capture() {
      return captureTranscriptView(view.id);
    },
    appendRow(height: number) {
      const index = rowHeights.length;
      const name = `tail-${index}`;
      rowHeights.push(height);
      model.turns.push({
        id: name,
        status: "completed",
        items: [{ id: `${name}-entry`, turnId: name, type: "userMessage", text: name, status: "completed" }],
      });
      anchorEntries.push({ id: `${name}-entry`, index, sourceIndex: index, isMessage: true });
      mounted.rerender(<Reader />);
    },
    remount(initial = view.getCapture()) {
      view.setReadable(false);
      mounted.unmount();
      view.setReadable(true);
      mounted = render(<Reader initial={initial ?? view.getCapture()} />);
    },
    dispose() {
      mounted.unmount();
      lifetime.dispose();
      external.restore();
      frames.restore();
      if (workspaceStore.getState().panes.includes(pane)) workspaceStore.getState().closePane(id);
    },
  };
}

export function readerTouch(target: HTMLElement, type: string, clientY: number) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "touches", { value: [{ clientY }] });
  target.dispatchEvent(event);
}

/** Wire rows supplied at the scripted transport boundary, not a mocked reader. */
export function readerWireTurns(ref: string): ThreadReadResponse["thread"]["turns"] {
  return ["current", "tail"].map((name) => ({
    id: `${ref}-${name}`,
    status: "completed",
    itemsView: "full",
    items: [
      {
        id: `${ref}-${name}-entry`,
        turnId: `${ref}-${name}`,
        type: "userMessage",
        text: `${ref} ${name} reading content`,
        status: "completed",
      },
    ],
  }));
}
