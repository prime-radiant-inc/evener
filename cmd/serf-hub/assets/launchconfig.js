// launchconfig.js — thin wrappers around serf/launch/* and serf/auth/*
// RPCs. Re-uses SerfAppwire.request from appwire.js.
(function (global) {
  function request(method, params) {
    return global.SerfAppwire.request(method, params);
  }

  global.launchconfig = {
    resolve: (cwd, overrides) =>
      request("serf/launch/resolve", { cwd, launchOverrides: overrides || undefined }),
    getLayer: (cwd, layer) => request("serf/launch/getLayer", { cwd, layer }),
    setLayer: (cwd, layer, config) => request("serf/launch/setLayer", { cwd, layer, config }),
    trustRepo: (cwd, hash) => request("serf/launch/trustRepo", { cwd, hash }),

    authList: () => request("serf/auth/list", {}),
    authStatus: (provider) => request("serf/auth/status", { provider }),
    authApiKeySet: (provider, value) => request("serf/auth/apiKey/set", { provider, value }),
    authLoginStart: (provider) => request("serf/auth/login/start", { provider }),
    authLoginComplete: (provider, flowId, redirectUrl) =>
      request("serf/auth/login/complete", { provider, flowId, redirectUrl }),
    authLogout: (provider) => request("serf/auth/logout", { provider }),
  };
})(window);
