import type { InstanceEntry } from "@evener/appwire-client";
import { openInNewTab } from "../../../../shell/openInNewTab";
import { credentialsStore } from "../../../../stores/credentials";

/** CODEX_AUTH_SCHEME is the OpenAI Codex transport auth scheme
 * (llm/registry/types.go's AuthOAuthOpenAICodex), which the hub's own gate
 * compares (cmd/evener-hub/app_auth.go's instanceIsCodex). */
export const CODEX_AUTH_SCHEME = "oauth-openai-codex";

/** COPILOT_AUTH_SCHEME is the GitHub Copilot subscription transport auth
 * scheme (llm/registry/types.go's AuthOAuthGitHubCopilot), which the hub's own
 * gate compares (cmd/evener-hub/app_auth.go's instanceIsCopilot). */
export const COPILOT_AUTH_SCHEME = "oauth-github-copilot";

/** DEVICE_OAUTH_AUTH_SCHEMES is every transport auth scheme whose sign-in is a
 * device-code flow the hub itself can run. It is pinned to the hub's own
 * schemes by cmd/evener-hub/device_oauth_auth_schemes.txt. */
export const DEVICE_OAUTH_AUTH_SCHEMES: readonly string[] = [CODEX_AUTH_SCHEME, COPILOT_AUTH_SCHEME];

/** supportsHostDeviceSignIn reports whether `instance` can begin a device-code
 * sign-in on the hub that owns it ("Sign in on host", component 07d). It is the
 * HOST's own gate, not a client-side guess: the scheme an instance resolves to is
 * what the hub's auth handlers accept (see DEVICE_OAUTH_AUTH_SCHEMES), and every
 * other scheme's sign-in is a browser redirect the remote host has no browser to
 * complete - so the action is offered exactly where the call can land, and never
 * where its only outcome would be the host's refusal.
 *
 * The scheme and NOT the provider id: `auth` is authored independently of `base`
 * (llm/registry/load.go's instanceRecord folds an instance's own fields over the
 * base it inherits, and llm/registry/merge.go's mergeTransport takes the layer's
 * auth), so an instance signed in through Codex OAuth on another base is one the
 * hub accepts, while an openai-codex-based instance overridden to another scheme
 * is one it refuses. */
export function supportsHostDeviceSignIn(instance: InstanceEntry): boolean {
  return DEVICE_OAUTH_AUTH_SCHEMES.includes(instance.auth);
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
