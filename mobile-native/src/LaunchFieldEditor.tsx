import type {
  LaunchConfigLayer,
  LaunchOption,
  MCPServerSpec,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { LaunchEnvironmentEditor } from "./LaunchEnvironmentEditor";
import { LaunchFallbackEditor } from "./LaunchFallbackEditor";
import { LaunchResourceEditor } from "./LaunchResourceEditor";
import { LaunchScalarEditor } from "./LaunchScalarEditor";

export function LaunchFieldEditor({
  option,
  value,
  effective,
  client,
  close,
  apply,
}: {
  option: LaunchOption;
  value: LaunchConfigLayer;
  effective: LaunchConfigLayer;
  client: ConversationClientLike | null;
  close(): void;
  apply(value: LaunchConfigLayer[keyof LaunchConfigLayer]): void;
}) {
  const field = option.wireField as keyof LaunchConfigLayer;
  const common = { option, client, close, apply };
  if (option.kind === "pathList" || option.kind === "mcpServerList")
    return (
      <LaunchResourceEditor
        {...common}
        value={value[field] as string[] | MCPServerSpec[] | undefined}
        effective={effective[field] as string[] | MCPServerSpec[] | undefined}
      />
    );
  if (option.kind === "modelList")
    return (
      <LaunchFallbackEditor
        {...common}
        value={value[field] as string[] | undefined}
        effective={effective[field] as string[] | undefined}
      />
    );
  if (option.kind === "envMap")
    return (
      <LaunchEnvironmentEditor
        {...common}
        value={value[field] as Record<string, string> | undefined}
        effective={effective[field] as Record<string, string> | undefined}
      />
    );
  return (
    <LaunchScalarEditor
      {...common}
      value={value[field] === undefined ? "" : String(value[field])}
      effective={effective[field] === undefined ? "" : String(effective[field])}
    />
  );
}
