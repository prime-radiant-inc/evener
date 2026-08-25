import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Bootstrap } from "./app/Bootstrap";
import "./styles/base.css";

const root = document.getElementById("root");
if (!root) throw new Error("missing #root mount point");
createRoot(root).render(
  <StrictMode>
    <Bootstrap />
  </StrictMode>,
);
