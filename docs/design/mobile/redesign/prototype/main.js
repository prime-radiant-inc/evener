// Boot: build state, mount the app, start the simulation, and expose the
// moderator API (window.__proto.reset / trigger / screen) used by the
// usability harness.
(function () {
  const EV = window.EV;
  const root = document.getElementById("app");
  const hash = location.hash;
  EV.framed = hash.includes("device") || matchMedia("(min-width: 560px) and (min-height: 700px)").matches;
  root.classList.toggle("device", hash.includes("device"));

  let epoch = 0;
  EV.mount = function () {
    epoch++;
    EV.render(EV.h(EV.App, { key: epoch }), root);
  };

  function boot(preset) {
    EV.S = EV.freshState();
    if (hash.includes("dark")) { EV.S.prefs.theme = "Dark"; EV.S.prefs.themeTouched = true; }
    if (preset) EV.sim.preset(preset, EV.S);
    EV.applyTheme();
    EV.mount();
  }

  const proto = window.__proto;
  proto.reset = function (preset) {
    proto.log = [];
    boot(preset || "default");
    EV.log("reset", { preset: preset || "default" });
  };
  proto.trigger = (name) => EV.sim.trigger(name);
  proto.screen = function () {
    const t = EV.top();
    return { top: t && t.name, id: t && (t.id || t.path || t.subId || null), sheets: EV.S.sheets.map((s) => s.kind), menu: !!EV.S.menu, banner: EV.S.banner ? EV.S.banner.kind : null };
  };

  boot(null);
  EV.sim.start();
  EV.log("load", { framed: EV.framed });
})();
