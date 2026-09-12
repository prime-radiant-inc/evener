import { validatePathListAdd } from "../../cmd/evener-hub/frontend/src/panes/settings/sections/launchShared/pathListAdd";
import type { LaunchOption } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

export async function addLaunchPath(
  client: ConversationClientLike,
  option: LaunchOption,
  items: string[],
  raw: string,
) {
  const path = raw.trim();
  if (!path) throw Error("Enter a path.");
  const result = await validatePathListAdd(option, items, path, (path, kind) =>
    client.request("evener/path/validate", { path, kind }),
  );
  if (!result.ok) throw Error(result.error);
  return [...items, result.value];
}
