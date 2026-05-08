(function () {
  window.serfHub = window.serfHub || {};
  window.serfHub.setTheme = function (theme) {
    if (theme === "light" || theme === "dark") {
      document.documentElement.setAttribute("data-theme", theme);
      localStorage.setItem("serf-hub.theme", theme);
    } else {
      document.documentElement.removeAttribute("data-theme");
      localStorage.removeItem("serf-hub.theme");
    }
  };
})();
