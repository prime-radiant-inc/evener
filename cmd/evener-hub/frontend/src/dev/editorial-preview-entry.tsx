import { createRoot } from "react-dom/client";
import { AppShell } from "../shell/AppShell";
import { createEditorialClient } from "./editorial-preview/fixture";
import "../styles/tokens.css";
import "../styles/global.css";

const client = createEditorialClient();
const errors: string[] = [];
window.addEventListener("error", (event) => errors.push(event.error?.stack ?? event.message));
window.addEventListener("unhandledrejection", (event) => errors.push(String(event.reason)));
Object.assign(window, { editorialPreview: { client, errors, fixtureOnly: true } });
if (location.pathname === "/editorial-preview.html") history.replaceState({}, "", "/");
const root = document.getElementById("root");
if (!root) throw new Error("Editorial preview requires #root");
createRoot(root).render(<AppShell client={client} />);
