// chip-overflow.js — caps visible .chip children of any
// [data-chip-overflow-host] container at 4 (sorted by data-chip-modified
// descending — most-recently-modified wins). Older chips are hidden and a
// "+N more" expand button is inserted; clicking it reveals all and removes
// itself.
(function () {
  "use strict";

  var CAP = 4;

  function apply(host) {
    if (!host || host.dataset.chipOverflowApplied === "true") return;
    var chips = Array.prototype.slice.call(host.querySelectorAll(".chip"));
    if (chips.length <= CAP) return;

    // Sort by data-chip-modified descending (numeric). Chips with no
    // attribute count as 0 (oldest). Stable: preserve original order
    // among ties.
    var withIdx = chips.map(function (el, i) {
      var v = parseInt(el.getAttribute("data-chip-modified") || "0", 10) || 0;
      return { el: el, idx: i, mod: v };
    });
    withIdx.sort(function (a, b) {
      if (b.mod !== a.mod) return b.mod - a.mod;
      return a.idx - b.idx;
    });

    var hiddenCount = 0;
    for (var i = CAP; i < withIdx.length; i++) {
      withIdx[i].el.hidden = true;
      hiddenCount++;
    }

    var more = document.createElement("button");
    more.type = "button";
    more.className = "chip chip-overflow-more";
    more.textContent = "+" + hiddenCount + " more";
    more.addEventListener("click", function () {
      for (var j = CAP; j < withIdx.length; j++) {
        withIdx[j].el.hidden = false;
      }
      if (more.parentNode) more.parentNode.removeChild(more);
      host.dataset.chipOverflowApplied = "expanded";
    });
    host.appendChild(more);

    host.dataset.chipOverflowApplied = "true";
  }

  function applyAll(root) {
    var hosts = (root || document).querySelectorAll("[data-chip-overflow-host]");
    for (var i = 0; i < hosts.length; i++) apply(hosts[i]);
  }

  // Apply to whatever is already in the DOM at script-load time (covers
  // both the browser case where htmx has already swapped in the spawn pane
  // and the test case where the DOM is fully set up before eval).
  applyAll(document);
  // Re-apply after every htmx swap so newly-rendered chip hosts are capped.
  document.addEventListener("htmx:afterSwap", function (e) {
    applyAll(e.target || document);
  });
})();
