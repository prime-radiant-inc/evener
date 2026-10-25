import styles from "./comic.module.css";
import type { ComicComposition } from "./layout";
import type { ComicEntry } from "./projection";

const FONT = '"Comic Sans Comic"';

/** A missing local webfont is a usable state, not a stalled reader. */
export async function loadComicFonts(): Promise<"ready" | "unavailable"> {
  if (!document.fonts) return "unavailable";
  const faces = await Promise.allSettled([
    document.fonts.load(`400 18px ${FONT}`),
    document.fonts.load(`700 30px ${FONT}`),
  ]);
  return faces.every((face) => face.status === "fulfilled" && face.value.length > 0) ? "ready" : "unavailable";
}

/** Probe shares the rendered panel/balloon/text CSS, including thought padding and mobile breakpoints. */
export function createComicMeasurer(container: HTMLElement): {
  fits(text: string, composition: ComicComposition, kind: ComicEntry["kind"]): boolean;
  dispose(): void;
} {
  const panel = document.createElement("article");
  panel.className = styles.panel as string;
  panel.setAttribute("data-comic-probe", "");
  panel.setAttribute("aria-hidden", "true");
  panel.style.position = "absolute";
  panel.style.visibility = "hidden";
  panel.style.pointerEvents = "none";
  panel.style.zIndex = "-1";
  const balloon = document.createElement("div");
  balloon.className = styles.balloon as string;
  const copy = document.createElement("p");
  copy.className = styles.balloonText as string;
  balloon.append(copy);
  panel.append(balloon);
  container.append(panel);

  return {
    fits(text, composition, kind) {
      const width = container.getBoundingClientRect().width || container.clientWidth || window.innerWidth;
      panel.style.width = `${composition === "paired" && width > 760 ? (width - 15) / 2 : width}px`;
      panel.classList.toggle(styles.spotlight as string, composition === "spotlight");
      balloon.classList.toggle(styles.thought as string, kind === "thought");
      panel.setAttribute("data-comic-kind", kind);
      copy.textContent = text;
      const computed = getComputedStyle(copy);
      const lineHeight =
        Number.parseFloat(computed.lineHeight) ||
        (composition === "spotlight" ? (width <= 760 ? 24 : 30) * 1.3 : (width <= 760 ? 17 : 18) * 1.45);
      return (
        Math.max(copy.getBoundingClientRect().height, copy.scrollHeight) <=
        lineHeight * (composition === "spotlight" ? 3 : 6) + 1
      );
    },
    dispose() {
      panel.remove();
    },
  };
}
