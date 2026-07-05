// plugins.js — wrappers around serf/marketplace/* and serf/plugin/* RPCs.
// Mirrors the request-wrapper shape of launchconfig.js.
(function (global) {
  function request(method, params) {
    return global.SerfAppwire.request(method, params);
  }

  global.pluginsAdmin = {
    marketplaceList: () => request("serf/marketplace/list", {}),
    marketplaceAdd: (params) => request("serf/marketplace/add", params),
    marketplaceRemove: (name) => request("serf/marketplace/remove", { name }),
    marketplaceRefresh: (name) => request("serf/marketplace/refresh", { name }),
    marketplaceBrowse: (name) => request("serf/marketplace/browse", { name }),

    pluginList: () => request("serf/plugin/list", {}),
    pluginInstall: (plugin, marketplace) => request("serf/plugin/install", { plugin, marketplace }),
    pluginUpgrade: (plugin, marketplace) => request("serf/plugin/upgrade", { plugin, marketplace }),
    pluginRemove: (plugin, marketplace) => request("serf/plugin/remove", { plugin, marketplace }),
    pluginEnable: (plugin, marketplace) => request("serf/plugin/enable", { plugin, marketplace }),
    pluginDisable: (plugin, marketplace) => request("serf/plugin/disable", { plugin, marketplace }),
    pluginSetAutoUpgrade: (plugin, marketplace, autoUpgrade) =>
      request("serf/plugin/setAutoUpgrade", { plugin, marketplace, autoUpgrade }),
  };
})(window);
