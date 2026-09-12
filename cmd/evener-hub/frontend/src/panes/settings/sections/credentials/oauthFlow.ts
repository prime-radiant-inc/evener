import { credentialsStore } from "../../../../stores/credentials";

export type OAuthEditor =
  | { kind: "oauth-redirect"; name: string; flowId: string; authUrl: string }
  | {
      kind: "device";
      name: string;
      flowId: string;
      userCode: string;
      verificationUrl: string;
      intervalSeconds: number;
    };

/** Starts the best OAuth flow supported by the hub for one provider instance. */
export async function startOAuthFlow(name: string, isCurrent: () => boolean = () => true): Promise<OAuthEditor | null> {
  const response = await credentialsStore.getState().deviceStart(name);
  if (!isCurrent()) return null;
  if (!response.fallback) {
    return {
      kind: "device",
      name,
      flowId: response.flowId,
      userCode: response.userCode,
      verificationUrl: response.verificationUrl,
      intervalSeconds: response.intervalSeconds,
    };
  }

  const login = await credentialsStore.getState().loginStart(name);
  if (!isCurrent()) return null;
  window.open(login.url, "_blank", "noopener");
  return { kind: "oauth-redirect", name, flowId: login.flowId, authUrl: login.url };
}
