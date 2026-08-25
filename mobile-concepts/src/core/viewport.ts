export function installViewportMetrics(
  target: Window,
  root: HTMLElement,
): () => void {
  const visualViewport = target.visualViewport;

  if (!visualViewport) {
    root.style.setProperty(
      "--visual-viewport-height",
      `${target.innerHeight}px`,
    );
    root.style.setProperty("--keyboard-inset", "0px");
    return () => {};
  }

  const update = () => {
    const keyboardInset = Math.max(
      0,
      target.innerHeight - (visualViewport.height + visualViewport.offsetTop),
    );
    root.style.setProperty(
      "--visual-viewport-height",
      `${visualViewport.height}px`,
    );
    root.style.setProperty("--keyboard-inset", `${keyboardInset}px`);
  };

  update();
  visualViewport.addEventListener("resize", update);
  visualViewport.addEventListener("scroll", update);

  return () => {
    visualViewport.removeEventListener("resize", update);
    visualViewport.removeEventListener("scroll", update);
  };
}
