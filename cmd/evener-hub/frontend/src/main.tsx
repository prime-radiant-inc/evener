import "./styles/tokens.css";
import "./styles/global.css";
import { createRoot } from "react-dom/client";
import { App } from "./App";

const root = document.getElementById("root");
if (!root) throw new Error("index.html is missing the #root element");
createRoot(root).render(<App />);
