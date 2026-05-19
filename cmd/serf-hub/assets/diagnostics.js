(function () {
  "use strict";

  const SOURCE_LABELS = {
    provider: "Provider",
    serf: "Serf",
    hub: "Hub",
    ui: "UI",
  };

  function cleanMessage(message) {
    let text = String(message || "").trim();
    text = text.replace(/^\[(error|warning|note)\]\s*/i, "").trim();
    if (!text) return "";
    return text.charAt(0).toUpperCase() + text.slice(1);
  }

  function normalizeSeverity(value) {
    const severity = String(value || "error").toLowerCase();
    if (severity === "warning" || severity === "note") return severity;
    return "error";
  }

  function normalizeSource(value) {
    const source = String(value || "").toLowerCase();
    if (SOURCE_LABELS[source]) return source;
    return "";
  }

  function classify(input) {
    const raw = typeof input === "string" ? { message: input } : (input || {});
    const severity = normalizeSeverity(raw.severity || raw.kind);
    const message = cleanMessage(firstNonEmpty(raw.message, raw.error, raw.warning));
    const storedSource = normalizeSource(raw.source);
    let source = storedSource;
    let title = String(raw.title || "").trim();
    let hint = String(raw.hint || "").trim();
    const lower = message.toLowerCase();
    const typedCauseKind = raw.cause && typeof raw.cause === "object" ? String(raw.cause.kind || "").toLowerCase() : "";

    if (!source) {
      if (isSerfConfiguration(lower)) {
        source = "serf";
      } else if (isHubFailure(lower)) {
        source = "hub";
      } else if (isProviderFailure(lower)) {
        source = "provider";
      } else if (isUIFailure(lower)) {
        source = "ui";
      } else {
        source = "serf";
      }
    }

    // Override stored source=serf when the message clearly indicates a
    // provider or hub failure. Legacy transcripts emitted before the
    // classifier patterns were broadened (commit 05203e0) keep their
    // stored source forever; this lets the e465 Reconnect/Retry button
    // surface for them. Only fires for the literal "serf" source; other
    // sources (provider, hub, ui) are honored unchanged.
    if (source === "serf" && !isSerfConfiguration(lower)) {
      if (isProviderFailure(lower)) source = "provider";
      else if (isHubFailure(lower)) source = "hub";
    }

    // Typed-cause override (kata 9476): appwire NotifyWarning carries
    // cause.{kind,provider,model,status} when the underlying error was a
    // typed llm.Error (kata cmfz). When cause.kind === "provider" trust it
    // unconditionally — typed wins over both the stored source and the
    // substring-match heuristics, which remain as the safety net for
    // envelopes that pre-date this field (legacy transcripts, non-LLM
    // warnings, etc).
    if (typedCauseKind === "provider") {
      source = "provider";
    }

    if (storedSource === "serf" && source !== "serf") {
      if (isDefaultSerfTitle(title, severity, lower)) title = "";
      if (isDefaultSerfHint(hint, lower)) hint = "";
    }

    if (!title) title = defaultTitle(source, severity, lower);
    if (!hint) hint = defaultHint(source, lower);

    return { severity, source, title, message, hint };
  }

  function firstNonEmpty() {
    for (let i = 0; i < arguments.length; i++) {
      const value = arguments[i];
      if (value == null) continue;
      if (typeof value === "object" && typeof value.message === "string") return value.message;
      const text = String(value).trim();
      if (text) return text;
    }
    return "";
  }

  function isSerfConfiguration(message) {
    return message.includes("unknown provider") ||
      message.includes("configuration error") ||
      message.includes("must use provider/model") ||
      message.includes("no model:");
  }

  function isProviderFailure(message) {
    const providers = ["openai", "anthropic", "google", "gemini", "openrouter", "ollama", "kimi", "glm", "minimax"];
    for (const provider of providers) {
      if (message.includes(provider + " error")) return true;
    }
    return message.includes("provider unavailable") ||
      message.includes("api key") ||
      message.includes("rate limit") ||
      message.includes("quota") ||
      message.includes("unauthorized") ||
      message.includes("invalid_grant") ||
      message.includes("token endpoint") ||
      // Stream-truncation: provider closed the stream early. The daemon
      // is fine; the upstream API is what failed.
      message.includes("stream ended without") ||
      message.includes("stream error") ||
      message.includes("missing response in finish event");
  }

  function isHubFailure(message) {
    return message.includes("rendezvous") ||
      message.includes("daemon spawn") ||
      message.includes("process exited before rendezvous") ||
      message.includes("resume timed out") ||
      message.includes("appwire") ||
      message.includes("websocket") ||
      message.includes("stream failed") ||
      message.includes("send failed") ||
      message.includes("steer failed") ||
      message.includes("fork failed") ||
      message.includes("source not found");
  }

  function isUIFailure(message) {
    return message.includes("browser") ||
      message.includes("clipboard") ||
      message.includes("render") ||
      message.includes("javascript") ||
      message.includes("read failed:");
  }

  function defaultTitle(source, severity, message) {
    if (source === "serf" && isSerfConfiguration(message)) return "Serf configuration error";
    if (source === "provider") return "Provider error";
    if (source === "hub") return "Hub error";
    if (source === "ui") return "UI error";
    return severity === "warning" ? "Serf warning" : "Serf error";
  }

  function defaultHint(source, message) {
    if (source === "serf" && isSerfConfiguration(message)) {
      return "Hub launched Serf with provider configuration this Serf runtime does not recognize. Check the model/provider passed by Hub and the Serf binary Hub is using.";
    }
    if (source === "provider") {
      return "The model provider failed to complete the response. Check the selected model, credentials, account access, and rate limits. The daemon is fine — retrying the turn or switching models may help.";
    }
    if (source === "hub") {
      return "Check the hub process, AppWire connection, spawn arguments, and rendezvous state.";
    }
    if (source === "ui") {
      return "Check the browser console and refresh if the local UI state is stale.";
    }
    return "Check the Serf session log and daemon state.";
  }

  function isDefaultSerfTitle(title, severity, message) {
    if (!title) return false;
    const text = String(title || "").trim();
    return text === defaultTitle("serf", severity, message) || text === "Serf error" || text === "Serf warning" || text === "Session warning";
  }

  function isDefaultSerfHint(hint, message) {
    if (!hint) return false;
    const text = String(hint || "").trim();
    return text === defaultHint("serf", message) ||
      text === "Check the Serf session log and daemon state." ||
      text.indexOf("Hub launched Serf with provider configuration") === 0;
  }

  // render builds a diagnostic card element.
  //
  // The optional `actions` array (on the input object or overridden by the
  // caller) may contain objects of the form { label: string, onclick: fn }.
  // Each action is rendered as a button inside the diagnostic footer.
  // This is used by the renderer to attach a "Retry turn" button when the
  // provider fails and a retry is possible.
  function render(input, actions) {
    const diagnostic = classify(input);
    const el = document.createElement("div");
    el.className = "diagnostic diagnostic-" + diagnostic.severity + " diagnostic-source-" + diagnostic.source;
    el.setAttribute("role", diagnostic.severity === "error" ? "alert" : "status");

    const header = document.createElement("div");
    header.className = "diagnostic-header";

    const badge = document.createElement("span");
    badge.className = "diagnostic-badge";
    badge.textContent = (SOURCE_LABELS[diagnostic.source] || "Serf") + " " + diagnostic.severity;
    header.appendChild(badge);

    const title = document.createElement("span");
    title.className = "diagnostic-title";
    title.textContent = diagnostic.title;
    header.appendChild(title);
    el.appendChild(header);

    if (diagnostic.message) {
      const body = document.createElement("div");
      body.className = "diagnostic-message";
      body.textContent = diagnostic.message;
      el.appendChild(body);
    }

    if (diagnostic.hint) {
      const hint = document.createElement("div");
      hint.className = "diagnostic-hint";
      hint.textContent = diagnostic.hint;
      el.appendChild(hint);
    }

    // Render action buttons (e.g. "Retry turn") when the caller supplies them.
    const resolvedActions = actions || (input && Array.isArray(input.actions) ? input.actions : null);
    if (resolvedActions && resolvedActions.length > 0) {
      const footer = document.createElement("div");
      footer.className = "diagnostic-actions";
      for (const action of resolvedActions) {
        const btn = document.createElement("button");
        btn.className = "diagnostic-action-btn";
        btn.textContent = String(action.label || "");
        if (typeof action.onclick === "function") {
          btn.addEventListener("click", action.onclick);
        }
        footer.appendChild(btn);
      }
      el.appendChild(footer);
    }

    return el;
  }

  window.SerfDiagnostics = {
    classify,
    cleanMessage,
    render,
  };
})();
