import { useEffect } from "react";
import { friendlyErrorMessage } from "../../../protocol/errors";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Disclosure } from "../../../widgets/disclosure";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./about.module.css";
import { FieldDim } from "./settingsField";

const CLASS = {
  root: requireClass(styles.root, "about.module.css", "root"),
  identity: requireClass(styles.identity, "about.module.css", "identity"),
  help: requireClass(styles.help, "about.module.css", "help"),
  license: requireClass(styles.license, "about.module.css", "license"),
};

// The MIT license text Beautiful UI's own terms require reproducing
// verbatim (LICENSES/beautiful-ui.txt) - hardcoded here rather than a
// build-time `?raw` import, because vitest's default `test.css: false`
// breaks `?raw` for stylesheets (token-contract.test.ts's own comment)
// and this needed to work under the exact same test config without
// touching vite.config.ts. about.test.tsx asserts this string against the
// file read straight off disk, so the two can't drift silently.
const MIT_LICENSE_TEXT = `MIT License

Copyright (c) 2026 Shane Levine

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`;

export { MIT_LICENSE_TEXT };

/**
 * Settings -> About: app identity plus the credits the design language's
 * borrowed terms require. Version/commit ride the same serf/settings/
 * overview fetch General already uses (settingsOverviewStore) rather than a
 * second request - hub.version/hub.commit are the same fields General's own
 * "Hub version" row shows. Unlike General, a failed or not-yet-connected
 * fetch isn't fatal here: the credits below don't depend on the hub at all,
 * so only the identity line degrades (to a bare "serf hub", no raw error
 * text - friendlyErrorMessage only feeds the dim caption, never the store's
 * raw message).
 */
export function AboutSection() {
  const data = useSettingsOverviewStore((s) => s.data);
  const error = useSettingsOverviewStore((s) => s.error);

  useEffect(() => {
    void settingsOverviewStore.getState().fetch();
  }, []);

  const hub = data?.hub;

  return (
    <div className={CLASS.root}>
      <p className={CLASS.identity}>
        serf hub
        {hub?.version !== undefined && <> {hub.version}</>}
        {hub?.commit !== undefined && <FieldDim> ({hub.commit})</FieldDim>}
      </p>
      {hub?.version === undefined && data === null && error !== null && (
        <p className={CLASS.help}>Version unavailable: {friendlyErrorMessage(error)}</p>
      )}

      <p className={CLASS.help}>
        The visual design language is adapted from Beautiful UI (https://www.beautifului.dev) by Shane Levine, used
        under the MIT License.
      </p>
      <Disclosure id="about-mit-license" summary="MIT License">
        <pre className={CLASS.license}>{MIT_LICENSE_TEXT}</pre>
      </Disclosure>

      <p className={CLASS.help}>Inter and JetBrains Mono, used under the SIL Open Font License.</p>

      <p className={CLASS.help}>Full third-party notices live in the repository.</p>
    </div>
  );
}
