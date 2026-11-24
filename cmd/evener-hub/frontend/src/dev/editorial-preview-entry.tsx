import { AppShell } from "../shell/AppShell";
import { createEditorialClient } from "./editorial-preview/fixture";
import { mountHarness } from "./mountHarness";

const client = createEditorialClient();
const errors: string[] = [];
window.addEventListener("error", (event) => errors.push(event.error?.stack ?? event.message));
window.addEventListener("unhandledrejection", (event) => errors.push(String(event.reason)));
Object.assign(window, { editorialPreview: { client, errors, fixtureOnly: true } });
if (location.pathname === "/editorial-preview.html") history.replaceState({}, "", "/");
mountHarness("editorial-preview", <AppShell client={client} />);
