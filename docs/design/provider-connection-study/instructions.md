# Proposed user instructions

**Design copy for the proposed flow. These controls have not shipped.**

## Connect your first model provider

1. In Evener, choose **Connect provider**.
2. Choose your provider, such as **Anthropic**, **OpenAI**, **Google Gemini**, or **OpenRouter**.
3. Follow its access instructions:
   - **API key:** open the linked provider console, create a key, and paste it into **API key**.
   - **ChatGPT:** choose **ChatGPT sign-in**, continue to OpenAI, and approve access. Your plan determines availability and usage limits.
4. Choose **Save & check connection**. Evener saves the access details on the host running Evener and requests the provider’s model list. It does not send a prompt or generate a response.
5. Choose a model and continue where you left off.

For a standard API-key connection, **the key is the only field you need to fill in**. Leave **Advanced settings** closed.

A Claude or Gemini chat subscription is not an API key. The provider console explains API billing and access. OpenAI’s ChatGPT sign-in and OpenAI API-key access are separate connection methods.

## Already have credentials on the Evener host?

Choose **Check existing access**. Evener shows the provider and credential source, with secret values hidden. Check the connection before choosing a model. If you use Evener through a remote host, environment variables and credential files must be on that host.

## Local model server or company gateway?

Choose **Local or company endpoint**.

- **Ollama:** confirm the API URL. `localhost` refers to the machine running Evener. The server must be running and have a model installed.
- **Cloud provider:** enter the displayed project, location, resource, and credential details. These are required when the host has not already supplied them.
- **Custom endpoint:** enter the API URL, select the API format your team provides, and choose the authentication method. Enter a key if the endpoint requires one.

Use **Advanced settings** for another named connection, endpoint overrides, environment-variable references, and custom headers. An API-key environment-variable field takes the *name* of a variable, never the secret itself.

## If the check does not succeed

- **Access rejected:** confirm your key or sign-in can access the provider, and check the provider account’s permissions.
- **Endpoint unavailable:** check the connection from the Evener host. Review the URL if using a proxy or gateway.
- **Check unavailable:** some providers cannot list models. Continue with the connection labeled **unverified**, then try a model in a session.
- **Saved, not checked:** your details are stored, but access has not been verified. Choose **Check connection** when ready.

Being able to list models does not guarantee that every model can generate a response. Evener will show any model-specific error in the session.

## Documentation placement

Proposed title: **Connect a model provider**. Link this how-to from the onboarding panel and the README’s getting-started section. Keep `llm-providers.md` and `llm-provider-config-and-launch.md` as technical references, each starting with a link to the how-to. Put environment variables, TOML precedence, and launch internals after the user’s first successful connection.
