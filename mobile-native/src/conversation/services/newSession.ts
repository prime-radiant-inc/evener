// NewSessionService wraps an AppwireClient's launch methods (thread/start,
// recent projects, models, plugin preview, path checks, git HEAD and launch
// resolution) in a typed API for the New session form.
//
// The service does NOT create or own the socket — it wraps an existing
// ConversationClientLike (structurally compatible with AppwireClient). Reads
// that describe a machine take the host they are about: the hub's own machine
// is asked directly, and any other host through evener/host/request
// (hostRouting.ts), so a session for another host is set up from that host's
// projects, models and plugins.

import type {
	InputItem,
	LaunchConfigLayer,
	LaunchConfigResolved,
	MethodTypes,
	ModelListParams,
	ModelListResponse,
	PluginPreviewParams,
	PluginPreviewResponse,
	Thread,
	ThreadStartResponse,
	Turn,
} from "@evener/appwire-client";
import { hostRequest, isLocalHost } from "@evener/appwire-client";
import type { ConversationClientLike } from "./conversation";

export interface NewSessionParams {
	/** The host to start on; absent or "local" is the hub's own machine. */
	source?: string;
	cwd: string;
	input?: InputItem[];
	modelProvider?: string;
	model?: string;
	reasoningEffort?: string;
	launchOverrides?: LaunchConfigLayer;
}

export interface NewSessionService {
	start(params: NewSessionParams): Promise<{ thread: Thread; turn: Turn }>;
	recentProjects(host?: string): Promise<string[]>;
	models(params?: ModelListParams, host?: string): Promise<ModelListResponse>;
	previewPlugins(params: PluginPreviewParams, host?: string): Promise<PluginPreviewResponse>;
	directoryExists(host: string, path: string): Promise<boolean>;
	createDirectory(host: string, path: string): Promise<string>;
	/** The project's current branch, or null outside a repository. */
	branch(host: string, cwd: string): Promise<string | null>;
	resolveLaunch(host: string, cwd: string, launchOverrides: LaunchConfigLayer): Promise<LaunchConfigResolved>;
}

export function createNewSessionService(client: ConversationClientLike): NewSessionService {
	return {
		async start(params) {
			const wireParams: MethodTypes["thread/start"]["params"] = {
				cwd: params.cwd,
			};
			// The hub's own machine takes no source (ruling 2): only another host
			// is named on the wire.
			if (params.source !== undefined && !isLocalHost(params.source)) {
				wireParams.source = params.source;
			}
			if (params.input !== undefined) {
				wireParams.input = params.input;
			}
			if (params.modelProvider !== undefined) {
				wireParams.modelProvider = params.modelProvider;
			}
			if (params.model !== undefined) {
				wireParams.model = params.model;
			}
			if (params.reasoningEffort !== undefined) {
				wireParams.reasoningEffort = params.reasoningEffort;
			}
			if (params.launchOverrides !== undefined) {
				wireParams.launchOverrides = params.launchOverrides;
			}
			const response: ThreadStartResponse = await client.request("thread/start", wireParams);
			return { thread: response.thread, turn: response.turn };
		},

		async recentProjects(host) {
			const response = await hostRequest(client, host, "evener/projects/recent", {});
			return response.data ?? [];
		},

		async models(params = {}, host) {
			return hostRequest(client, host, "model/list", params);
		},
		async previewPlugins(params, host) {
			return hostRequest(client, host, "evener/plugin/preview", params);
		},
		async directoryExists(host, path) {
			const response = await hostRequest(client, host, "evener/path/validate", { path, kind: "dir" });
			return response.valid;
		},
		async createDirectory(host, path) {
			const response = await hostRequest(client, host, "evener/dirs/create", { path });
			return response.path;
		},
		async branch(host, cwd) {
			const response = await hostRequest(client, host, "evener/git/head", { cwd });
			return response.head || null;
		},
		async resolveLaunch(host, cwd, launchOverrides) {
			return hostRequest(client, host, "evener/launch/resolve", {
				cwd,
				...(Object.keys(launchOverrides).length > 0 ? { launchOverrides } : {}),
			});
		},
	};
}

export type { ConversationClientLike };
