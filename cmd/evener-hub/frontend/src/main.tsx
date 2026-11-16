import "./styles/tokens.css";
import "./styles/global.css";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { installTouchPinchVeto } from "./shell/pinchVeto";

// Before first paint: the app shell loads lazily behind a blank Suspense
// fallback, and a pinch in that window would zoom a page this veto could no
// longer undo. The veto lives for the page's lifetime, so its remover is
// discarded.
installTouchPinchVeto(window);

const root = document.getElementById("root");
if (!root) throw new Error("index.html is missing the #root element");
createRoot(root).render(<App />);
