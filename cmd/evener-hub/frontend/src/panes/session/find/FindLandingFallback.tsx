// Every tool renders its own body, and many show less than Find searches: a
// write shows its confirmation, a fetch a short preview. So that a match Find
// steps to is always one the reader can see, the tool row asks here: when
// this call is Find's landing and the body it rendered does not hold the
// words, the stretch of the call's text that does is shown beneath it.
import { type ItemModel, itemFindTexts, textMatchesFind } from "@evener/appwire-client";
import { useLayoutEffect, useRef, useState } from "react";
import { Excerpt } from "../transcript/tools/bodies";
import { excerptAround } from "./findExcerpt";
import { useFindReveal } from "./findReveal";

// A reading-sized stretch: enough context to place the words, not the file.
const FALLBACK_CHARS = 1200;

export function FindLandingFallback({ item }: { item: ItemModel }) {
  const needle = useFindReveal(item.id);
  const own = useRef<HTMLDivElement>(null);
  const [hidden, setHidden] = useState(false);
  // Read what the body actually rendered, after it rendered: no renderer has
  // to declare what it shows. This element is always the body's last child,
  // so its siblings are the body.
  useLayoutEffect(() => {
    const body = own.current?.parentElement;
    if (needle === "" || !body) {
      setHidden(false);
      return;
    }
    let shown = "";
    for (const child of body.childNodes) {
      if (child !== own.current) shown += ` ${child.textContent ?? ""}`;
    }
    setHidden(!textMatchesFind(shown, needle));
  }, [needle]);
  const source =
    hidden && needle !== "" ? itemFindTexts(item).find((text) => textMatchesFind(text, needle)) : undefined;
  const excerpt = source === undefined ? null : excerptAround(source, needle, FALLBACK_CHARS);
  // Present but not rendered while there is nothing to show, so it takes no
  // room in the body's layout.
  return (
    <div ref={own} hidden={excerpt === null}>
      {excerpt !== null && <Excerpt text={excerpt} />}
    </div>
  );
}
