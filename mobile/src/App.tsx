import type { JSX } from "react";

import "./ui/global.css";

export interface AppProps {
  fixture?: boolean;
}

export function App(_props: AppProps): JSX.Element {
  return (
    <main>
      <h1>Evener</h1>
      <nav aria-label="Primary">
        <button type="button">Sessions</button>
        <button type="button">New</button>
        <button type="button">Settings</button>
      </nav>
    </main>
  );
}
