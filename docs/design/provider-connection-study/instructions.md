# Proposed user instructions

**Design copy for the proposed flow. These controls have not shipped.**

## Connect your first model provider

Connect **one provider** to get started. Add others later.

For a standard API-key connection, **the key is the only field you need to fill in**. Leave **Advanced settings** closed. An API key is a secret code that lets Evener use your provider account.

1. On Evener’s welcome screen, choose your first provider. No providers are configured yet; the displayed providers are available choices.
2. Choose your provider, such as **Anthropic** for Claude, **OpenAI**, **Google Gemini**, or **OpenRouter**.
3. Follow the appropriate access route:
   - **API key:** open the linked provider console, create a key, and paste it into **API key**. Choose **Save & check connection**.
   - **ChatGPT:** choose **ChatGPT sign-in**, then **Continue with ChatGPT**. Approve access in OpenAI and return to Evener. Check the linked current requirements for availability and usage limits.
4. Evener saves the access details on the host running Evener and checks the provider’s model list. It does not send a prompt or generate a response.
5. After a successful check, choose a model and continue to your first session. You can now enter what you want to work on. Your global default does not change.

A Claude chat subscription does not include Claude API access. Create a Claude Console API key; API usage is billed separately. A Gemini chat subscription is not an API key either. OpenAI API-key access and ChatGPT sign-in are separate Evener connections: `openai` and `openai-codex`.

## Already have credentials on the Evener host?

Open **Already configured access on this host?**, then choose **Check existing access**. In the key step, **Use existing host access instead** is inside **Advanced settings**. These are secondary routes; first-run setup assumes you have not configured Evener yet. Review the provider, destination, and active credential source, with secret values hidden. Check the connection before choosing a model.

Environment or header credentials can take precedence over a saved key. The live flow must show the resolved source, rather than assume the key just pasted is active. If you use a remote Evener host, its environment variables and credential files matter, not this browser’s machine.

## Local model server or company gateway?

Choose **Local or company endpoint**.

- **Ollama:** confirm the API URL. `localhost` refers to the machine running Evener. The server must be running and have a model installed. A default local server needs no key; a protected remote server can use the optional key in Advanced settings.
- **Azure:** enter the resource name and its API key.
- **Google Vertex AI:** enter the project ID and location. Use a supported ADC file on the Evener host or paste supported credential JSON. The accepted JSON types are `service_account` and `authorized_user`; metadata-server identity alone is not supported. JSON stays concealed until you choose **Show**.
- **Custom endpoint:** enter the API URL, select the API format supplied by your team, and provide a key if needed. **No new credential** means you are not supplying a key; existing host credentials may still apply.

For a custom destination, endpoint override, or host credential reference, review the destination and resolved credential source before continuing. Do not reuse access details at a different endpoint without confirming where they will go.

Use **Advanced settings** for another named connection, endpoint overrides, environment-variable references, and custom headers. An API-key environment-variable field takes the *name* of a variable, never the secret itself. **Open full connection editor** preserves the existing surface, template-variable, override-reset, rename, and credential-management controls.

## If setup does not succeed

- **Access rejected:** check the key or sign-in and your account’s API permissions.
- **Credentials missing:** add access details or review the active source on the Evener host.
- **Endpoint unavailable:** check connectivity from the Evener host. Review the URL if using a proxy or gateway.
- **Configuration failure:** review the API format, endpoint, and required cloud values.
- **Save failure:** the changes were not saved. Review the error and retry with the retained draft.
- **Partial save:** some changes may have reached the host. Re-read the saved connection and credential status before retrying; do not create a duplicate.
- **Check unavailable:** some providers cannot list models. Continue with the connection **unverified**, then try a model in a session.
- **Saved, not checked:** access details are stored, but access has not been verified. Choose **Check connection** when ready.
- **Sign-in code expired:** request a new code or use the browser-redirect route.

**Change provider** returns to provider choices. **Cancel** exits setup and discards unsaved access details. After saving, **Done** exits without undoing the saved changes. Back and error recovery retain the current draft only for this setup.

Listing models does not guarantee generation or access to every model. The check result belongs to this setup; persistent verification badges would require separate design and implementation work.

## Documentation placement

Proposed title: **Connect a model provider**. Link this how-to from provider setup and the README’s getting-started section. Keep `llm-providers.md` and `llm-provider-config-and-launch.md` as technical references, each starting with a link to the how-to. Put environment variables, TOML precedence, and launch internals after the first successful connection.
