# Connecting a provider

## Start your first session

1. Open **New session**. Choose a working directory and, optionally, write your
   task. These stay in the composer while you connect a provider.
2. Select **Connect provider**, then choose a provider. **Show all providers**
   reveals the complete catalogue and its search; local and custom endpoints have
   their own routes.
3. For a standard API connection, paste the **actual API key**, not an
   environment-variable name. The field is masked. Use **Get an API key** beside
   the form, or these provider pages:
   [Anthropic](https://console.anthropic.com/settings/keys),
   [OpenAI](https://platform.openai.com/api-keys),
   [Gemini](https://aistudio.google.com/apikey),
   [OpenRouter](https://openrouter.ai/settings/keys).
4. Select **Save and check**. Evener saves access on the Evener host, reloads the
   resolved connection, and requests the provider's **model list only**. It does
   not send your prompt or prove generation access, quota, or model capability.
5. After a successful check, select **Continue**. The model chooser opens with
   models returned for that connection's actual instance name. Choose a model;
   **Show all models** returns to the full catalogue. If no models are listed,
   check the provider's model access and endpoint rather than guessing a model ID.
6. Review your working directory, prompt, and model, then press **Start**.
   Connecting and choosing a model do not start a session or change the global
   provider default.

**Subscriptions are not API keys.** Claude, ChatGPT, and Gemini subscriptions
do not automatically include separately billed API access. OpenAI API keys and
**OpenAI Codex** sign-in are separate connections. For the latter, choose its
sign-in route and follow the device or browser/redirect instructions instead of
pasting an API key. Availability depends on your account and provider terms.

## Other access routes

- **Cloud providers:** use **Show all providers**, choose Azure, Vertex, Bedrock, or
  another provider, then **Configure provider** when offered. Supply the required
  project/resource/region or endpoint variables. The full editor retains
  protocol, surface, header, and custom-name controls. Vertex can use credential
  JSON or existing Application Default Credentials; follow the selected
  provider's access instructions rather than substituting an API key.
- **Local/custom endpoints:** start the server on a host reachable from the
  Evener host and use the local/custom route. Configure its actual URL and any
  required credentials. An empty optional key means keep resolved host access,
  not disable authentication. See [Ollama](ollama.md) for local discovery.
- **Already configured on this host:** expand **Already configured access on
  this host?** and choose **Manage existing connections**. The connect dialog
  closes and Settings → Providers & credentials opens; each instance row there
  opens the complete inspector, where you can review the displayed destination
  and credential source and test current access before contacting the provider.
  The guided form also offers **Use existing host access** under **Advanced
  settings**.
- **Advanced management:** Settings → Providers & credentials → **Connect provider** uses the
  same connector. The instance rows are the real editor, with custom instances,
  rename, reset overrides, replace/clear stored credentials, logout, source
  inspection, and explicit default management. In the session model chooser,
  only an explicit model pick requests a session model switch.

## Repair or return

Save failures keep the current credential draft available for retry. Check
failures do not undo a successful save: inspect the inline error, confirm the
destination and active source, repair access or endpoint configuration, and
retry. Changed destinations require review rather than silently reusing inherited
access. If a provider cannot check model-list access, **Continue without
verification** is an explicit unverified route, not a success badge.

**Change provider** returns to discovery; it does not carry your secret to a
different provider. **Cancel** returns to the originating screen without choosing
a model or starting a session. Your composer draft remains, but credentials or
configuration already saved on the host are not rolled back by Cancel.

For storage, environment precedence, and launch internals, see the
[technical provider reference](llm-provider-config-and-launch.md).
