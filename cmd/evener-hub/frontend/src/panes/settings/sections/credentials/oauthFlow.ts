import type { InstanceEntry } from "@evener/appwire-client";
import { openInNewTab } from "../../../../shell/openInNewTab";
import { credentialsStore } from "../../../../stores/credentials";

/** CODEX_PROVIDER_ID is the provider whose sign-in is the OpenAI device-code
 * flow. It is the one the hub's auth/device handlers accept
 * (cmd/evener-hub/app_auth.go's requiresCodex), because the flow is OpenAI
 * device authorization, not a generic OAuth path. */
export const CODEX_PROVIDER_ID = "openai-codex";

/** supportsHostDeviceSignIn reports whether `instance` can begin a device-code
 * sign-in on the hub that owns it ("Sign in on host", component 07d). Only the
 * Codex provider uses it: its access is a ChatGPT/Codex subscription signed in
 * by code (the hub has no key for it), and every other provider's sign-in is a
 * browser redirect the remote host has no browser to complete. */
export function supportsHostDeviceSignIn(instance: InstanceEntry): boolean {
  return instance.providerId === CODEX_PROVIDER_ID;
}

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
  openInNewTab(login.url);
  return { kind: "oauth-redirect", name, flowId: login.flowId, authUrl: login.url };
}
