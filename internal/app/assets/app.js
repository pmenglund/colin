(() => {
  const detailsKeyPrefix = "colin:details:";
  const refreshStateKey = "colin:auto-refresh";
  const refreshToggleSelector = "[data-refresh-toggle]";
  const refreshStatusSelector = "[data-testid='refresh-status']";
  const dataAgeSelector = "[data-data-age][data-generated-at]";
  const localTimeSelector = "[data-local-time][data-timestamp]";
  const liveRefreshSelector = "[data-live-refresh-mode]";
  const liveRefreshEvent = "colin:refresh";
  const codexOutputPanelSelector = "[data-codex-output-panel]";
  const codexOutputBodySelector = "[data-codex-output-body]";
  let liveEventSource = null;
  let staleTimer = null;
  let ageTimerStarted = false;
  const codexOutputStreams = new Map();
  const preservedCodexOutputPanels = new Map();

  function preserveDetailsState(root) {
    Array.from(root.querySelectorAll("details[data-preserve-open]")).forEach((element) => {
      if (!element.id || element.dataset.detailsBound) {
        return;
      }
      const state = sessionStorage.getItem(detailsKeyPrefix + element.id);
      if (state === "open") {
        element.open = true;
      }
      element.addEventListener("toggle", () => {
        sessionStorage.setItem(detailsKeyPrefix + element.id, element.open ? "open" : "closed");
      });
      element.dataset.detailsBound = "true";
    });
  }

  function refreshStatusBadge() {
    return document.querySelector(refreshStatusSelector);
  }

  function markRefreshStale(reason) {
    clearRefreshStaleTimer();
    const badge = refreshStatusBadge();
    if (!badge) {
      return;
    }
    const generatedAt = badge.getAttribute("data-generated-at") || "";
    badge.dataset.refreshStatus = "stale";
    badge.textContent = "Stale data";
    badge.classList.remove("badge-success", "badge-accent", "badge-info");
    badge.classList.add("badge-danger");
    const titleParts = ["Showing the last successful dashboard state"];
    if (generatedAt) {
      titleParts.push(`Last successful update at ${generatedAt}`);
    }
    if (reason) {
      titleParts.push(reason);
    }
    const title = titleParts.join(". ");
    badge.setAttribute("title", title);
    badge.setAttribute("aria-label", title);
  }

  function clearRefreshStaleTimer() {
    if (staleTimer === null) {
      return;
    }
    clearTimeout(staleTimer);
    staleTimer = null;
  }

  function scheduleRefreshStale(reason) {
    clearRefreshStaleTimer();
    staleTimer = window.setTimeout(() => {
      markRefreshStale(reason);
    }, 2000);
  }

  function autoRefreshPaused() {
    return sessionStorage.getItem(refreshStateKey) === "paused";
  }

  function setAutoRefreshPaused(paused) {
    sessionStorage.setItem(refreshStateKey, paused ? "paused" : "running");
  }

  function currentLiveRefreshTarget() {
    return document.querySelector(liveRefreshSelector);
  }

  function closeLiveEventSource() {
    if (!liveEventSource) {
      return;
    }
    liveEventSource.close();
    liveEventSource = null;
    clearRefreshStaleTimer();
  }

  function codexOutputPanelKey(panel) {
    return panel.getAttribute("data-codex-output-issue-id") || panel.id || "";
  }

  function codexOutputBody(panel) {
    return panel.querySelector(codexOutputBodySelector);
  }

  function updateCodexOutputCursor(panel, cursor) {
    const nextCursor = cursor || "";
    panel.dataset.codexOutputCursor = nextCursor;
    const body = codexOutputBody(panel);
    if (body) {
      body.setAttribute("data-codex-output-cursor", nextCursor);
    }
  }

  function closeCodexOutputStream(panel) {
    const key = codexOutputPanelKey(panel);
    if (!key) {
      return;
    }
    const stream = codexOutputStreams.get(key);
    if (!stream) {
      return;
    }
    stream.close();
    codexOutputStreams.delete(key);
  }

  function closeAllCodexOutputStreams() {
    Array.from(codexOutputStreams.values()).forEach((stream) => stream.close());
    codexOutputStreams.clear();
  }

  function replaceCodexOutputHTML(panel, html) {
    const body = codexOutputBody(panel);
    if (!body) {
      return;
    }
    body.outerHTML = html;
    applyLocalTimes(panel);
  }

  function prependCodexOutputHTML(panel, html) {
    const body = codexOutputBody(panel);
    if (!body) {
      return;
    }
    if (!body.querySelector(".worker-output-entry")) {
      body.innerHTML = "";
    }
    body.insertAdjacentHTML("afterbegin", html);
    applyLocalTimes(panel);
  }

  function loadCodexOutputPanel(panel) {
    if (panel.dataset.codexOutputLoaded === "true") {
      return Promise.resolve();
    }

    const loadUrl = panel.getAttribute("data-codex-output-load-url");
    const body = codexOutputBody(panel);
    if (!loadUrl || !body) {
      return Promise.resolve();
    }

    body.innerHTML = '<pre class="mockup-code">Loading Codex output...</pre>';
    return fetch(loadUrl)
      .then((response) => {
        if (!response.ok) {
          throw new Error(`Failed to load Codex output: HTTP ${response.status}`);
        }
        return response.text();
      })
      .then((html) => {
        replaceCodexOutputHTML(panel, html);
        panel.dataset.codexOutputLoaded = "true";
        const nextBody = codexOutputBody(panel);
        updateCodexOutputCursor(panel, nextBody ? nextBody.getAttribute("data-codex-output-cursor") : "");
      })
      .catch((error) => {
        const message = error && error.message ? error.message : "Failed to load Codex output";
        body.innerHTML = `<pre class="mockup-code">${message}</pre>`;
      });
  }

  function scheduleCodexOutputReconnect(panel) {
    if (panel.dataset.codexOutputReconnectPending === "true") {
      return;
    }
    panel.dataset.codexOutputReconnectPending = "true";
    window.setTimeout(() => {
      panel.dataset.codexOutputReconnectPending = "false";
      syncCodexOutputPanel(panel);
    }, 1000);
  }

  function openCodexOutputStream(panel) {
    const key = codexOutputPanelKey(panel);
    const eventsUrl = panel.getAttribute("data-codex-output-events-url");
    if (!key || !eventsUrl || codexOutputStreams.has(key)) {
      return;
    }

    const url = new URL(eventsUrl, window.location.origin);
    const cursor = panel.dataset.codexOutputCursor || "";
    if (cursor) {
      url.searchParams.set("after", cursor);
    }

    const source = new EventSource(url.toString());
    codexOutputStreams.set(key, source);
    source.addEventListener("ready", (event) => {
      const payload = JSON.parse(event.data);
      updateCodexOutputCursor(panel, payload.cursor || "");
    });
    source.addEventListener("output_entry", (event) => {
      const payload = JSON.parse(event.data);
      prependCodexOutputHTML(panel, payload.html || "");
      updateCodexOutputCursor(panel, payload.cursor || "");
    });
    source.addEventListener("reset", (event) => {
      const payload = JSON.parse(event.data);
      replaceCodexOutputHTML(panel, payload.html || "");
      updateCodexOutputCursor(panel, payload.cursor || "");
    });
    source.onerror = () => {
      closeCodexOutputStream(panel);
      if (panel.isConnected && panel.open && !autoRefreshPaused()) {
        scheduleCodexOutputReconnect(panel);
      }
    };
  }

  function syncCodexOutputPanel(panel) {
    if (!(panel instanceof HTMLDetailsElement)) {
      return;
    }
    if (!panel.open) {
      closeCodexOutputStream(panel);
      return;
    }
    if (autoRefreshPaused()) {
      closeCodexOutputStream(panel);
    }
    loadCodexOutputPanel(panel).then(() => {
      if (!panel.isConnected || !panel.open) {
        return;
      }
      if (autoRefreshPaused()) {
        closeCodexOutputStream(panel);
        return;
      }
      openCodexOutputStream(panel);
    });
  }

  function bindCodexOutputPanels(root) {
    Array.from(root.querySelectorAll(codexOutputPanelSelector)).forEach((panel) => {
      if (!(panel instanceof HTMLDetailsElement)) {
        return;
      }
      if (!panel.dataset.codexOutputBound) {
        panel.addEventListener("toggle", () => {
          syncCodexOutputPanel(panel);
        });
        panel.dataset.codexOutputBound = "true";
      }
      syncCodexOutputPanel(panel);
    });
  }

  function captureHydratedCodexOutputPanels(root) {
    preservedCodexOutputPanels.clear();
    Array.from(root.querySelectorAll(codexOutputPanelSelector)).forEach((panel) => {
      if (!(panel instanceof HTMLDetailsElement) || !panel.open || panel.dataset.codexOutputLoaded !== "true") {
        return;
      }
      const key = codexOutputPanelKey(panel);
      const body = codexOutputBody(panel);
      if (!key || !body) {
        return;
      }
      preservedCodexOutputPanels.set(key, {
        html: body.outerHTML,
        cursor: panel.dataset.codexOutputCursor || body.getAttribute("data-codex-output-cursor") || "",
      });
      closeCodexOutputStream(panel);
    });
  }

  function restoreHydratedCodexOutputPanels(root) {
    Array.from(root.querySelectorAll(codexOutputPanelSelector)).forEach((panel) => {
      if (!(panel instanceof HTMLDetailsElement)) {
        return;
      }
      const preserved = preservedCodexOutputPanels.get(codexOutputPanelKey(panel));
      const body = codexOutputBody(panel);
      if (!preserved || !body) {
        return;
      }
      body.outerHTML = preserved.html;
      panel.dataset.codexOutputLoaded = "true";
      updateCodexOutputCursor(panel, preserved.cursor);
      panel.open = true;
      applyLocalTimes(panel);
    });
    preservedCodexOutputPanels.clear();
  }

  function triggerLiveRefresh() {
    const target = currentLiveRefreshTarget();
    if (!target) {
      return;
    }
    const mode = target.getAttribute("data-live-refresh-mode");
    if (mode === "fragment") {
      if (window.htmx && typeof window.htmx.trigger === "function") {
        window.htmx.trigger(target, liveRefreshEvent);
      } else {
        target.dispatchEvent(new CustomEvent(liveRefreshEvent, { bubbles: true }));
      }
      return;
    }
    if (mode === "reload") {
      window.location.reload();
    }
  }

  function openLiveEventSource() {
    if (liveEventSource || autoRefreshPaused() || !currentLiveRefreshTarget()) {
      return;
    }

    liveEventSource = new EventSource("/api/v1/events");
    liveEventSource.addEventListener("open", () => {
      clearRefreshStaleTimer();
    });
    liveEventSource.addEventListener("snapshot", () => {
      clearRefreshStaleTimer();
      triggerLiveRefresh();
    });
    liveEventSource.onerror = () => {
      scheduleRefreshStale("Live update stream disconnected");
    };
  }

  function syncLiveEventSource(options) {
    if (autoRefreshPaused() || !currentLiveRefreshTarget()) {
      closeLiveEventSource();
      return;
    }
    openLiveEventSource();
    if (options && options.forceRefresh) {
      triggerLiveRefresh();
    }
  }

  function applyRefreshToggle(root) {
    Array.from(root.querySelectorAll(refreshToggleSelector)).forEach((button) => {
      if (!button.dataset.toggleBound) {
        button.addEventListener("click", (event) => {
          event.preventDefault();
          const paused = !autoRefreshPaused();
          setAutoRefreshPaused(paused);
          updateRefreshToggle(document);
          syncLiveEventSource({ forceRefresh: !paused });
          bindCodexOutputPanels(document);
        });
        button.dataset.toggleBound = "true";
      }
    });
    updateRefreshToggle(root);
  }

  function updateRefreshToggle(root) {
    const paused = autoRefreshPaused();
    Array.from(root.querySelectorAll(refreshToggleSelector)).forEach((button) => {
      button.textContent = paused ? ">" : "||";
      button.setAttribute("aria-label", paused ? "Resume automatic refresh" : "Pause automatic refresh");
      button.setAttribute("title", paused ? "Resume automatic refresh" : "Pause automatic refresh");
      button.setAttribute("aria-pressed", paused ? "true" : "false");
    });
  }

  function applyLocalTimes(root) {
    const formatter = new Intl.DateTimeFormat(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      timeZoneName: "short",
    });

    Array.from(root.querySelectorAll(localTimeSelector)).forEach((element) => {
      const timestamp = element.getAttribute("data-timestamp");
      if (!timestamp) {
        return;
      }
      const value = new Date(timestamp);
      if (Number.isNaN(value.getTime())) {
        return;
      }
      element.textContent = formatter.format(value);
    });
  }

  function ensureAgeTimer() {
    if (ageTimerStarted) {
      return;
    }
    ageTimerStarted = true;
    window.setInterval(() => {
      applyDataAges(document);
    }, 1000);
  }

  function formatAge(date) {
    let totalSeconds = Math.floor((Date.now() - date.getTime()) / 1000);
    if (Number.isNaN(totalSeconds) || totalSeconds < 0) {
      totalSeconds = 0;
    }

    const units = [
      { size: 24 * 60 * 60, suffix: "d" },
      { size: 60 * 60, suffix: "h" },
      { size: 60, suffix: "m" },
      { size: 1, suffix: "s" },
    ];
    const parts = [];
    let remaining = totalSeconds;
    for (const unit of units) {
      if (unit.suffix !== "s" && remaining < unit.size) {
        continue;
      }
      const value = unit.suffix === "s" && parts.length === 0 ? remaining : Math.floor(remaining / unit.size);
      if (value <= 0) {
        continue;
      }
      parts.push(`${value}${unit.suffix}`);
      remaining -= value * unit.size;
      if (parts.length === 2) {
        break;
      }
    }

    if (parts.length === 0) {
      return "0s old";
    }
    return `${parts.join("")} old`;
  }

  function applyDataAges(root) {
    Array.from(root.querySelectorAll(dataAgeSelector)).forEach((element) => {
      const timestamp = element.getAttribute("data-generated-at");
      if (!timestamp) {
        return;
      }
      const value = new Date(timestamp);
      if (Number.isNaN(value.getTime())) {
        return;
      }
      element.textContent = formatAge(value);
    });
  }

  function initialize(root) {
    preserveDetailsState(root);
    applyRefreshToggle(root);
    bindCodexOutputPanels(root);
    applyLocalTimes(root);
    applyDataAges(root);
    ensureAgeTimer();
    syncLiveEventSource();
  }

  function htmxRefreshTarget(event) {
    const target = event.detail && event.detail.target;
    if (target && target.matches && target.matches(liveRefreshSelector)) {
      return target;
    }
    return null;
  }

  function markHTMXRefreshStale(event) {
    const target = htmxRefreshTarget(event);
    if (!target) {
      return;
    }
    const xhr = event.detail && event.detail.xhr;
    const status = xhr && xhr.status ? `HTTP ${xhr.status}` : "request failed";
    markRefreshStale(`Refresh failed: ${status}`);
  }

  window.addEventListener("beforeunload", () => {
    closeAllCodexOutputStreams();
    closeLiveEventSource();
  });
  document.body.addEventListener("htmx:beforeSwap", (event) => {
    const target = htmxRefreshTarget(event);
    if (target) {
      captureHydratedCodexOutputPanels(target);
    }
  });
  document.body.addEventListener("htmx:afterSwap", () => {
    restoreHydratedCodexOutputPanels(document);
    initialize(document);
  });
  document.body.addEventListener("htmx:responseError", markHTMXRefreshStale);
  document.body.addEventListener("htmx:sendError", markHTMXRefreshStale);
  document.body.addEventListener("htmx:timeout", markHTMXRefreshStale);
  document.addEventListener("DOMContentLoaded", () => initialize(document));
})();
