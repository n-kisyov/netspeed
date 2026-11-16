(() => {
  const SPEED_SCALE = [
    { mbps: 0, label: "0" },
    { mbps: 25, label: "25" },
    { mbps: 100, label: "100" },
    { mbps: 500, label: "500" },
    { mbps: 1000, label: "1G+" }
  ];

  let appState = null;
  let historyItems = [];
  let displayMbps = 0;
  let targetMbps = 0;
  let clockOffsetMs = 0;
  let lastSeenAlertID = 0;
  let toastTimer = null;
  let activeTab = "dashboard";
  let sse = null;

  const $ = (id) => document.getElementById(id);

  function normUnitSpeed(mbps, unit) {
    if (unit === "MB/s") return { val: mbps / 8, unit: "MB/s" };
    if (unit === "Kbps") return { val: mbps * 1000, unit: "Kbps" };
    return { val: mbps, unit: "Mbps" };
  }

  function nextUnit(u) {
    if (u === "Mbps") return "MB/s";
    if (u === "MB/s") return "Kbps";
    return "Mbps";
  }

  function mbpsToProgress(mbps) {
    if (mbps <= 0) return 0;
    if (mbps <= 25) return (mbps / 25) * 0.25;
    if (mbps <= 100) return 0.25 + ((mbps - 25) / 75) * 0.25;
    if (mbps <= 500) return 0.5 + ((mbps - 100) / 400) * 0.25;
    return Math.min(1, 0.75 + ((mbps - 500) / 500) * 0.25);
  }

  function polarPoint(cx, cy, r, deg) {
    const rad = (deg * Math.PI) / 180;
    return { x: cx + r * Math.cos(rad), y: cy + r * Math.sin(rad) };
  }

  function describeArc(cx, cy, r, startDeg, endDeg) {
    const start = polarPoint(cx, cy, r, startDeg);
    const end = polarPoint(cx, cy, r, endDeg);
    const sweep = Math.abs(endDeg - startDeg);
    const largeArc = sweep > 180 ? 1 : 0;
    return `M ${start.x.toFixed(2)} ${start.y.toFixed(2)} A ${r} ${r} 0 ${largeArc} 1 ${end.x.toFixed(2)} ${end.y.toFixed(2)}`;
  }

  function initGaugeTicks() {
    const cx = 180, cy = 170, r = 122;
    const g = $("gaugeTicks");
    g.innerHTML = "";
    const ns = "http://www.w3.org/2000/svg";

    // Draw background full 240-deg track: 150 deg (bottom-left) clockwise to 390 deg (30 deg bottom-right)
    $("gaugeTrackArc").setAttribute("d", describeArc(cx, cy, r, 150, 390));

    SPEED_SCALE.forEach((mark, idx) => {
      const pct = idx / (SPEED_SCALE.length - 1);
      const deg = 150 + pct * 240;
      const p1 = polarPoint(cx, cy, r - 18, deg);
      const p2 = polarPoint(cx, cy, r - 11, deg);
      const tp = polarPoint(cx, cy, r + 22, deg);

      const line = document.createElementNS(ns, "line");
      line.setAttribute("x1", p1.x.toFixed(1));
      line.setAttribute("y1", p1.y.toFixed(1));
      line.setAttribute("x2", p2.x.toFixed(1));
      line.setAttribute("y2", p2.y.toFixed(1));
      line.setAttribute("class", "gauge-tick-line");
      g.appendChild(line);

      const text = document.createElementNS(ns, "text");
      text.setAttribute("x", tp.x.toFixed(1));
      text.setAttribute("y", tp.y.toFixed(1));
      text.setAttribute("class", "gauge-tick-text");
      text.textContent = mark.label;
      g.appendChild(text);
    });

    for (let i = 0; i < 8; i++) {
      if (i % 2 === 0) continue;
      const deg = 150 + (i / 8) * 240;
      const p1 = polarPoint(cx, cy, r - 15, deg);
      const p2 = polarPoint(cx, cy, r - 11, deg);
      const line = document.createElementNS(ns, "line");
      line.setAttribute("x1", p1.x.toFixed(1));
      line.setAttribute("y1", p1.y.toFixed(1));
      line.setAttribute("x2", p2.x.toFixed(1));
      line.setAttribute("y2", p2.y.toFixed(1));
      line.setAttribute("class", "gauge-tick-line");
      line.setAttribute("opacity", "0.5");
      g.appendChild(line);
    }
  }

  function renderGauge() {
    const cx = 180, cy = 170, r = 122;
    const frac = mbpsToProgress(displayMbps);
    const activeArc = $("gaugeActiveArc");
    const needle = $("gaugeNeedle");
    const dot = $("gaugeNeedleDot");
    const phase = appState ? appState.phase : "idle";
    const accent = phase === "upload" ? "var(--accent-upload)" : "var(--accent-download)";

    if (frac <= 0.002) {
      activeArc.setAttribute("d", "");
    } else {
      activeArc.setAttribute("d", describeArc(cx, cy, r, 150, 150 + frac * 240));
    }
    activeArc.style.stroke = accent;

    const deg = 150 + frac * 240;
    const tip = polarPoint(cx, cy, r - 4, deg);
    const tail = polarPoint(cx, cy, 72, deg);
    needle.setAttribute("x1", tail.x.toFixed(1));
    needle.setAttribute("y1", tail.y.toFixed(1));
    needle.setAttribute("x2", tip.x.toFixed(1));
    needle.setAttribute("y2", tip.y.toFixed(1));
    needle.style.stroke = accent;

    if (frac <= 0.002 && !(appState && appState.running)) {
      needle.setAttribute("opacity", "0");
      dot.setAttribute("opacity", "0");
    } else {
      needle.setAttribute("opacity", "1");
      dot.setAttribute("opacity", "1");
      dot.setAttribute("cx", tip.x.toFixed(1));
      dot.setAttribute("cy", tip.y.toFixed(1));
    }

    if (appState && appState.running) {
      const conv = normUnitSpeed(displayMbps, appState.config.unit);
      $("liveSpeedValue").textContent = conv.val.toFixed(conv.unit === "Mbps" ? 1 : 2);
      $("liveUnitLabel").textContent = conv.unit;
    }
  }

  function animLoop() {
    displayMbps += (targetMbps - displayMbps) * 0.22;
    if (Math.abs(targetMbps - displayMbps) < 0.02) displayMbps = targetMbps;
    renderGauge();
    requestAnimationFrame(animLoop);
  }

  function showToast(msg, kind = "info") {
    const el = $("toast");
    el.textContent = msg;
    el.classList.remove("hidden", "warning");
    if (kind === "warning") el.classList.add("warning");
    if (toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.add("hidden"), 4500);
  }

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function drawSparkline(canvas, samples, strokeVar, fillRgba) {
    if (!canvas) return;
    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    const w = Math.max(10, Math.floor(rect.width * dpr));
    const h = Math.max(10, Math.floor(rect.height * dpr));
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w;
      canvas.height = h;
    }
    const ctx = canvas.getContext("2d");
    ctx.clearRect(0, 0, w, h);

    ctx.strokeStyle = cssVar("--border");
    ctx.lineWidth = dpr;
    ctx.beginPath();
    ctx.moveTo(0, h - dpr);
    ctx.lineTo(w, h - dpr);
    ctx.stroke();

    if (!samples || samples.length < 2) return;
    const maxV = Math.max(1, ...samples) * 1.15;
    const pts = samples.map((v, i) => ({
      x: (i / (samples.length - 1)) * w,
      y: h - (v / maxV) * (h - 4 * dpr) - 2 * dpr
    }));

    ctx.fillStyle = fillRgba;
    ctx.beginPath();
    ctx.moveTo(pts[0].x, h - dpr);
    pts.forEach((p) => ctx.lineTo(p.x, p.y));
    ctx.lineTo(pts[pts.length - 1].x, h - dpr);
    ctx.closePath();
    ctx.fill();

    ctx.strokeStyle = cssVar(strokeVar);
    ctx.lineWidth = 2 * dpr;
    ctx.lineJoin = "round";
    ctx.beginPath();
    pts.forEach((p, idx) => (idx === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y)));
    ctx.stroke();
  }

  function drawTrendChart(canvas, items) {
    if (!canvas) return;
    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    const w = Math.max(20, Math.floor(rect.width * dpr));
    const h = Math.max(40, Math.floor(rect.height * dpr));
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w;
      canvas.height = h;
    }
    const ctx = canvas.getContext("2d");
    ctx.clearRect(0, 0, w, h);

    const padY = 8 * dpr;
    const plotH = h - padY * 2;
    ctx.strokeStyle = cssVar("--border");
    ctx.lineWidth = dpr;
    for (let i = 0; i <= 3; i++) {
      const y = padY + (i / 3) * plotH;
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(w, y);
      ctx.stroke();
    }

    const recent = items.slice(0, 24).reverse();
    if (recent.length < 2) return;

    const maxMbps = Math.max(10, ...recent.map((r) => Math.max(r.download_mbps, r.upload_mbps))) * 1.15;
    const maxPing = Math.max(10, ...recent.map((r) => r.ping_ms)) * 1.25;

    const plotSeries = (getter, maxVal, colorVar, fillAlpha) => {
      const pts = recent.map((r, i) => ({
        x: (i / (recent.length - 1)) * w,
        y: padY + plotH * (1 - Math.min(1, Math.max(0, getter(r) / maxVal)))
      }));
      if (fillAlpha) {
        ctx.fillStyle = fillAlpha;
        ctx.beginPath();
        ctx.moveTo(pts[0].x, padY + plotH);
        pts.forEach((p) => ctx.lineTo(p.x, p.y));
        ctx.lineTo(pts[pts.length - 1].x, padY + plotH);
        ctx.closePath();
        ctx.fill();
      }
      ctx.strokeStyle = cssVar(colorVar);
      ctx.lineWidth = 2 * dpr;
      ctx.lineJoin = "round";
      ctx.beginPath();
      pts.forEach((p, i) => (i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y)));
      ctx.stroke();

      ctx.fillStyle = cssVar(colorVar);
      pts.forEach((p) => {
        ctx.beginPath();
        ctx.arc(p.x, p.y, 3 * dpr, 0, Math.PI * 2);
        ctx.fill();
      });
    };

    plotSeries((r) => r.download_mbps, maxMbps, "--accent-download", "rgba(56, 189, 248, 0.14)");
    plotSeries((r) => r.upload_mbps, maxMbps, "--accent-upload", "rgba(167, 139, 250, 0.14)");
    plotSeries((r) => r.ping_ms, maxPing, "--accent-ping", null);
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme === "light" ? "light" : "dark");
  }

  function gradeClass(grade) {
    if (grade === "A+" || grade === "A") return "grade-good";
    if (grade === "B") return "grade-fair";
    return "grade-poor";
  }

  function formatDurationMMSS(ms) {
    if (ms <= 0) return "0s";
    const sec = Math.floor(ms / 1000);
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    if (h > 0) return `${h}h ${m}m ${s}s`;
    if (m > 0) return `${m}m ${s}s`;
    return `${s}s`;
  }

  function updateCountdown() {
    if (!appState || !appState.config.schedule_enabled || !appState.next_run_unix_ms) {
      $("nextRunLine").textContent = "Next Run: Disabled";
      return;
    }
    const approxServerNow = Date.now() + clockOffsetMs;
    const rem = Math.max(0, appState.next_run_unix_ms - approxServerNow);
    const dt = new Date(appState.next_run_unix_ms);
    $("nextRunLine").textContent = `Next Run: ${dt.toLocaleTimeString()} (in ${formatDurationMMSS(rem)})`;
  }

  function renderState(s) {
    appState = s;
    clockOffsetMs = s.server_time_unix_ms - Date.now();
    applyTheme(s.config.theme);

    if (s.last_alert && s.last_alert.id > lastSeenAlertID) {
      lastSeenAlertID = s.last_alert.id;
      showToast(`${s.last_alert.title}: ${s.last_alert.body}`, s.last_alert.kind);
      if (s.config.tray_notifications && "Notification" in window && Notification.permission === "granted") {
        try {
          new Notification(s.last_alert.title, { body: s.last_alert.body });
        } catch (_) {}
      }
    }

    // Header
    $("unitBtn").textContent = s.config.unit;
    $("themeBtn").textContent = `Theme: ${s.config.theme === "light" ? "Light" : "Dark"}`;
    $("platformBadge").textContent = s.platform || "linux";
    if (s.config.schedule_enabled) {
      $("autoPill").textContent = `AUTO ${s.config.schedule_interval_minutes}m`;
      $("autoPill").classList.remove("hidden");
    } else {
      $("autoPill").classList.add("hidden");
    }

    // Gauge target
    if (s.running) {
      $("goCircleBtn").classList.add("hidden");
      $("liveReadout").classList.remove("hidden");
      let phaseTxt = s.phase.toUpperCase();
      if (s.phase === "init") phaseTxt = "DISCOVER";
      $("livePhaseLabel").textContent = phaseTxt;
      $("livePhaseLabel").style.color = s.phase === "upload" ? "var(--accent-upload)" : "var(--accent-download)";
      targetMbps = s.phase === "download" || s.phase === "upload" ? s.current_mbps : 0;
    } else {
      $("goCircleBtn").classList.remove("hidden");
      $("liveReadout").classList.add("hidden");
      const hasDone = s.phase === "done" || s.last_session_result !== null;
      targetMbps = s.download_mbps > 0 ? s.download_mbps : 0;
      $("goCircleBtn").querySelector(".go-sub").textContent = hasDone ? "TEST AGAIN" : "START TEST";
    }
    $("statusMsg").textContent = s.status_msg || "Ready";

    // Metrics
    $("pingVal").textContent = s.ping_ms > 0 ? s.ping_ms.toFixed(1) : "—";
    const gb = $("gradeBadge");
    if (s.bufferbloat_grade) {
      gb.textContent = `Grade ${s.bufferbloat_grade}`;
      gb.className = `grade-badge ${gradeClass(s.bufferbloat_grade)}`;
    } else {
      gb.className = "grade-badge hidden";
    }
    const jStr = s.jitter_ms > 0 ? s.jitter_ms.toFixed(1) : "—";
    const lStr = s.ping_ms > 0 ? s.packet_loss_pct.toFixed(1) : "—";
    $("jitterLossLine").textContent = `Jitter: ${jStr} ms · Loss: ${lStr}%`;
    if (s.download_ping_ms > 0 || s.upload_ping_ms > 0) {
      $("loadedPingLine").textContent = `Loaded: ${Math.round(s.download_ping_ms)} / ${Math.round(s.upload_ping_ms)} ms`;
    } else if (s.loaded_ping_ms > 0) {
      $("loadedPingLine").textContent = `Loaded: ${Math.round(s.loaded_ping_ms)} ms`;
    } else {
      $("loadedPingLine").textContent = "Loaded: — / — ms";
    }

    const dlConv = normUnitSpeed(s.download_mbps, s.config.unit);
    $("dlVal").textContent = s.download_mbps > 0 ? dlConv.val.toFixed(2) : "—";
    $("dlUnit").textContent = dlConv.unit;
    $("dlBytesLabel").textContent = s.download_bytes > 0 ? `${(s.download_bytes / (1024 * 1024)).toFixed(1)} MB` : "";
    drawSparkline($("dlSpark"), s.dl_samples, "--accent-download", "rgba(56, 189, 248, 0.22)");

    const ulConv = normUnitSpeed(s.upload_mbps, s.config.unit);
    $("ulVal").textContent = s.upload_mbps > 0 ? ulConv.val.toFixed(2) : "—";
    $("ulUnit").textContent = ulConv.unit;
    $("ulBytesLabel").textContent = s.upload_bytes > 0 ? `${(s.upload_bytes / (1024 * 1024)).toFixed(1)} MB` : "";
    drawSparkline($("ulSpark"), s.ul_samples, "--accent-upload", "rgba(167, 139, 250, 0.22)");

    // Footer
    let srvTxt = "Server: Auto discovery...";
    if (s.server && s.server.name) {
      srvTxt = s.server.distance_km > 0
        ? `Server: ${s.server.name} [${Math.round(s.server.distance_km)} km]`
        : `Server: ${s.server.name}`;
    }
    $("footerServer").textContent = srvTxt;
    const isp = s.client && s.client.isp ? s.client.isp : "Discovering network...";
    const ip = s.client && s.client.ip ? (s.config.hide_ip ? "•••.•••.•••.•••" : s.client.ip) : "";
    $("ipToggleBtn").textContent = ip ? `Client ISP: ${isp} · ${ip}` : `Client ISP: ${isp}`;

    const sessionReady = !s.running && s.last_session_result !== null;
    $("copyResultBtn").disabled = !sessionReady;
    $("saveCardBtn").disabled = !sessionReady;

    // Servers & URL tab
    document.querySelectorAll("#providerGroup [data-provider]").forEach((btn) => {
      btn.classList.toggle("selected", btn.dataset.provider === s.config.provider);
    });
    if (document.activeElement !== $("customUrlInput") && s.config.custom_url && !$("customUrlInput").value) {
      $("customUrlInput").value = s.config.custom_url;
    }
    const urlErr = $("customUrlError");
    if (s.custom_url_err) {
      urlErr.textContent = s.custom_url_err;
      urlErr.classList.remove("hidden");
    } else {
      urlErr.classList.add("hidden");
    }

    const recentWrap = $("recentUrlsWrap");
    const recentList = $("recentUrlsList");
    recentList.innerHTML = "";
    const recent = s.config.saved_custom_urls || [];
    if (recent.length > 0) {
      recentWrap.classList.remove("hidden");
      recent.slice(0, 4).forEach((u) => {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "btn btn-sm btn-choice";
        if (s.config.provider === "custom" && s.config.custom_url === u) b.classList.add("selected");
        b.textContent = u.length > 34 ? u.slice(0, 31) + "..." : u;
        b.title = u;
        b.addEventListener("click", () => {
          $("customUrlInput").value = u;
          patchConfig({ custom_url: u, apply_custom_url: true });
        });
        recentList.appendChild(b);
      });
    } else {
      recentWrap.classList.add("hidden");
    }

    $("refreshServersBtn").textContent = s.servers_loading ? "Refreshing..." : "Refresh";
    $("refreshServersBtn").disabled = s.servers_loading;
    const srvErrEl = $("serversError");
    if (s.servers_err) {
      srvErrEl.textContent = `Discovery notice: ${s.servers_err}`;
      srvErrEl.classList.remove("hidden");
    } else {
      srvErrEl.classList.add("hidden");
    }
    renderServerList(s);

    // Schedule & Service tab
    const schedBtn = $("schedToggleBtn");
    schedBtn.classList.toggle("on", s.config.schedule_enabled);
    schedBtn.textContent = s.config.schedule_enabled ? "ON — Running in background" : "OFF — Manual Only";
    document.querySelectorAll("#schedPresets [data-mins]").forEach((b) => {
      b.classList.toggle("selected", s.config.schedule_enabled && Number(b.dataset.mins) === s.config.schedule_interval_minutes);
    });
    updateCountdown();
    $("lastSchedLine").textContent = s.last_scheduled_summary ? `Last Scheduled Result: ${s.last_scheduled_summary}` : "";

    const startupBtn = $("startupToggleBtn");
    startupBtn.classList.toggle("on", s.startup_enabled);
    startupBtn.disabled = !s.startup_supported;
    startupBtn.textContent = s.startup_supported
      ? `Startup: ${s.startup_enabled ? "Launch Server at Boot (systemd)" : "Off (Manual Launch)"}`
      : "Startup: Linux/Pi systemd only";

    const alertsBtn = $("alertsToggleBtn");
    alertsBtn.classList.toggle("on-ul", s.config.tray_notifications);
    alertsBtn.textContent = `Web Alerts: ${s.config.tray_notifications ? "On" : "Off"}`;

    document.querySelectorAll("#alertThreshGroup [data-thresh]").forEach((b) => {
      const val = Number(b.dataset.thresh);
      const active = Math.abs((s.config.alert_min_download_mbps || 0) - val) < 0.5;
      b.classList.toggle("selected-danger", active && val > 0);
      b.classList.toggle("selected", active && val === 0);
    });

    document.querySelectorAll("#threadsGroup [data-threads]").forEach((b) => {
      b.classList.toggle("selected", Number(b.dataset.threads) === s.config.threads);
    });
    document.querySelectorAll("#durGroup [data-dur]").forEach((b) => {
      b.classList.toggle("selected-ul", Number(b.dataset.dur) === s.config.duration_seconds);
    });

    if (s.systemd_unit_preview) {
      $("systemdPreview").textContent = s.systemd_unit_preview;
    }
  }

  function renderServerList(s) {
    const container = $("serversList");
    container.innerHTML = "";
    const servers = s.servers || [];
    if (servers.length === 0) {
      const empty = document.createElement("div");
      empty.className = "muted-line";
      empty.textContent = s.servers_loading ? "Discovering nearby Ookla and Cloudflare servers..." : "No servers discovered yet. Click Refresh.";
      container.appendChild(empty);
      return;
    }

    servers.forEach((srv) => {
      const row = document.createElement("div");
      const isSelected = (s.config.preferred_server_id && srv.id === s.config.preferred_server_id) ||
        (!s.config.preferred_server_id && s.server && srv.id === s.server.id);
      row.className = "list-item server-item" + (isSelected ? " selected" : "");
      row.addEventListener("click", () => patchConfig({ preferred_server_id: srv.id }));

      const left = document.createElement("div");
      left.className = "item-main";
      const title = document.createElement("div");
      title.className = "item-title";
      title.textContent = srv.sponsor || srv.name;
      const sub = document.createElement("div");
      sub.className = "item-sub";
      const dist = srv.distance_km > 0 ? `${Math.round(srv.distance_km)} km · ` : "";
      sub.textContent = `${dist}${srv.city}${srv.country ? ", " + srv.country : ""} · ${String(srv.provider || "").toUpperCase()}`;
      left.appendChild(title);
      left.appendChild(sub);

      const right = document.createElement("div");
      right.className = "latency-pill";
      right.textContent = srv.latency_ms > 0 && srv.latency_ms < 9000 ? `${srv.latency_ms.toFixed(1)} ms` : "—";

      row.appendChild(left);
      row.appendChild(right);
      container.appendChild(row);
    });
  }

  async function refreshHistory() {
    try {
      const resp = await fetch("/api/history", { cache: "no-store" });
      if (!resp.ok) return;
      const data = await resp.json();
      historyItems = data.items || [];
      renderHistory();
    } catch (_) {}
  }

  function formatSummaryForResult(r, unit) {
    const dl = normUnitSpeed(r.download_mbps, unit);
    const ul = normUnitSpeed(r.upload_mbps, unit);
    const trig = (r.trigger || "manual").toUpperCase();
    const grade = r.bufferbloat_grade || "—";
    const dt = new Date(r.timestamp).toLocaleString();
    return [
      `NetSpeed Result (${trig})`,
      `Time: ${dt}`,
      `Provider: ${String(r.provider || "").toUpperCase()}`,
      `Server: ${r.server ? r.server.name : ""}`,
      `Idle Ping: ${r.ping_ms.toFixed(2)} ms (Jitter: ${r.jitter_ms.toFixed(2)} ms, Loss: ${r.packet_loss_pct.toFixed(1)}%)`,
      `Bufferbloat Grade: ${grade}`,
      `Download: ${dl.val.toFixed(2)} ${dl.unit} (Loaded Ping: ${(r.download_ping_ms || 0).toFixed(1)} ms)`,
      `Upload: ${ul.val.toFixed(2)} ${ul.unit} (Loaded Ping: ${(r.upload_ping_ms || 0).toFixed(1)} ms)`
    ].join("\n");
  }

  function renderHistory() {
    const unit = appState ? appState.config.unit : "Mbps";
    $("historyTitle").textContent = `Saved Test History (${historyItems.length} runs)`;

    const trendCard = $("trendCard");
    if (historyItems.length >= 2) {
      trendCard.classList.remove("hidden");
      const n = Math.min(24, historyItems.length);
      const maxDl = Math.max(0, ...historyItems.slice(0, n).map((r) => r.download_mbps));
      const conv = normUnitSpeed(maxDl, unit);
      $("trendHeading").textContent = `RECENT TREND (LAST ${n} RUNS · MAX ${Math.round(conv.val)} ${conv.unit})`;
      drawTrendChart($("trendCanvas"), historyItems);
    } else {
      trendCard.classList.add("hidden");
    }

    const list = $("historyList");
    list.innerHTML = "";
    if (historyItems.length === 0) {
      const empty = document.createElement("div");
      empty.className = "muted-line";
      empty.textContent = "No completed speed tests saved yet.";
      list.appendChild(empty);
      return;
    }

    historyItems.forEach((r) => {
      const row = document.createElement("div");
      row.className = "list-item";

      const left = document.createElement("div");
      left.className = "item-main";
      const title = document.createElement("div");
      title.className = "item-title";
      const dt = new Date(r.timestamp);
      const stamp = isNaN(dt.getTime()) ? r.timestamp : dt.toLocaleString();
      title.textContent = `${stamp} · ${(r.trigger || "manual").toUpperCase()} · ${String(r.provider || "").toUpperCase()}`;
      const sub = document.createElement("div");
      sub.className = "item-sub";
      sub.textContent = r.server && r.server.name ? r.server.name : "Unknown server";
      left.appendChild(title);
      left.appendChild(sub);

      const right = document.createElement("div");
      right.className = "hist-metrics";

      if (r.bufferbloat_grade) {
        const g = document.createElement("span");
        g.className = `grade-badge ${gradeClass(r.bufferbloat_grade)}`;
        g.textContent = `Grade ${r.bufferbloat_grade}`;
        right.appendChild(g);
      }

      const pingSpan = document.createElement("span");
      pingSpan.className = "ping-color";
      pingSpan.textContent = `Ping ${r.ping_ms.toFixed(1)} ms`;
      right.appendChild(pingSpan);

      const dl = normUnitSpeed(r.download_mbps, unit);
      const dlSpan = document.createElement("span");
      dlSpan.className = "dl-color";
      dlSpan.textContent = `↓ ${dl.val.toFixed(1)} ${dl.unit}`;
      right.appendChild(dlSpan);

      const ul = normUnitSpeed(r.upload_mbps, unit);
      const ulSpan = document.createElement("span");
      ulSpan.className = "ul-color";
      ulSpan.textContent = `↑ ${ul.val.toFixed(1)} ${ul.unit}`;
      right.appendChild(ulSpan);

      const copyBtn = document.createElement("button");
      copyBtn.type = "button";
      copyBtn.className = "btn btn-sm";
      copyBtn.textContent = "Copy";
      copyBtn.addEventListener("click", () => copyText(formatSummaryForResult(r, unit)));
      right.appendChild(copyBtn);

      const pngBtn = document.createElement("button");
      pngBtn.type = "button";
      pngBtn.className = "btn btn-sm";
      pngBtn.textContent = "PNG";
      pngBtn.addEventListener("click", () => downloadCardPNG(r.id));
      right.appendChild(pngBtn);

      const delBtn = document.createElement("button");
      delBtn.type = "button";
      delBtn.className = "btn btn-sm btn-danger-outline";
      delBtn.textContent = "Delete";
      delBtn.addEventListener("click", async () => {
        const resp = await fetch(`/api/history?id=${encodeURIComponent(r.id)}`, { method: "DELETE" });
        if (resp.ok) {
          const d = await resp.json();
          historyItems = d.items || [];
          renderHistory();
          showToast("History entry removed");
        }
      });
      right.appendChild(delBtn);

      row.appendChild(left);
      row.appendChild(right);
      list.appendChild(row);
    });
  }

  async function copyText(text) {
    if (!text) return;
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        showToast("Copied result summary to clipboard");
        return;
      }
    } catch (_) {}

    // Plain HTTP LAN fallback (Raspberry Pi accessed via http://<pi-ip>:8080)
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand("copy");
    } catch (_) {}
    document.body.removeChild(ta);
    if (ok) {
      showToast("Copied result summary to clipboard");
    } else {
      showToast("Copy blocked by browser; export PNG or CSV instead", "warning");
    }
  }

  async function downloadCardPNG(historyID) {
    const url = historyID ? `/api/card?id=${encodeURIComponent(historyID)}` : "/api/card";
    try {
      const resp = await fetch(url, { cache: "no-store" });
      if (!resp.ok) {
        const err = await resp.json().catch(() => ({ error: "Card export failed" }));
        showToast(err.error || "Card export failed", "warning");
        return;
      }
      const savedPath = resp.headers.get("X-NetSpeed-Card-Path") || "";
      const disp = resp.headers.get("Content-Disposition") || "";
      const match = disp.match(/filename="([^"]+)"/);
      const fname = match ? match[1] : "netspeed-card.png";
      const blob = await resp.blob();
      const objUrl = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = objUrl;
      a.download = fname;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(objUrl);
      showToast(savedPath ? `Saved card PNG: ${savedPath}` : `Downloaded ${fname}`);
    } catch (e) {
      showToast(`Card export failed: ${e.message}`, "warning");
    }
  }

  async function patchConfig(patch) {
    try {
      const resp = await fetch("/api/config", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(patch)
      });
      const data = await resp.json().catch(() => ({}));
      if (data && data.config) {
        renderState(data);
      } else if (data && data.state) {
        renderState(data.state);
      }
      if (!resp.ok && data.error) {
        showToast(data.error, "warning");
      }
    } catch (e) {
      showToast(`Failed saving config: ${e.message}`, "warning");
    }
  }

  function switchTab(tab) {
    activeTab = tab;
    document.querySelectorAll(".tab-btn").forEach((b) => {
      b.classList.toggle("active", b.dataset.tab === tab);
    });
    document.querySelectorAll(".tab-panel").forEach((p) => {
      p.classList.toggle("active", p.id === `tab-${tab}`);
    });
    if (tab === "history") {
      refreshHistory();
    } else if (tab === "dashboard" && appState) {
      drawSparkline($("dlSpark"), appState.dl_samples, "--accent-download", "rgba(56, 189, 248, 0.22)");
      drawSparkline($("ulSpark"), appState.ul_samples, "--accent-upload", "rgba(167, 139, 250, 0.22)");
    }
  }

  function connectSSE() {
    if (sse) sse.close();
    sse = new EventSource("/api/events");
    let prevCount = appState ? appState.history_count : -1;
    sse.onmessage = (ev) => {
      try {
        const s = JSON.parse(ev.data);
        const countChanged = prevCount !== -1 && s.history_count !== prevCount;
        prevCount = s.history_count;
        renderState(s);
        if (countChanged || (activeTab === "history" && s.phase === "done" && !s.running)) {
          refreshHistory();
        }
      } catch (_) {}
    };
    sse.onerror = () => {
      sse.close();
      setTimeout(connectSSE, 3000);
    };
  }

  function bindEvents() {
    document.querySelectorAll(".tab-btn").forEach((btn) => {
      btn.addEventListener("click", () => switchTab(btn.dataset.tab));
    });

    $("unitBtn").addEventListener("click", () => {
      if (!appState) return;
      patchConfig({ unit: nextUnit(appState.config.unit) });
    });

    $("themeBtn").addEventListener("click", () => {
      if (!appState) return;
      patchConfig({ theme: appState.config.theme === "dark" ? "light" : "dark" });
    });

    $("goCircleBtn").addEventListener("click", async () => {
      const resp = await fetch("/api/test/start", { method: "POST" });
      if (resp.ok) {
        const s = await resp.json();
        renderState(s);
      }
    });

    $("stopBtn").addEventListener("click", async () => {
      const resp = await fetch("/api/test/stop", { method: "POST" });
      if (resp.ok) {
        const s = await resp.json();
        renderState(s);
      }
    });

    $("ipToggleBtn").addEventListener("click", () => {
      if (!appState) return;
      patchConfig({ hide_ip: !appState.config.hide_ip });
    });

    $("copyResultBtn").addEventListener("click", () => {
      if (!appState || !appState.last_session_result) return;
      copyText(appState.clipboard_summary || formatSummaryForResult(appState.last_session_result, appState.config.unit));
    });

    $("saveCardBtn").addEventListener("click", () => {
      if (!appState || !appState.last_session_result) return;
      downloadCardPNG("");
    });

    document.querySelectorAll("#providerGroup [data-provider]").forEach((btn) => {
      btn.addEventListener("click", () => patchConfig({ provider: btn.dataset.provider }));
    });

    $("customUrlForm").addEventListener("submit", (e) => {
      e.preventDefault();
      patchConfig({ custom_url: $("customUrlInput").value, apply_custom_url: true });
    });

    $("refreshServersBtn").addEventListener("click", async () => {
      const resp = await fetch("/api/servers?refresh=1");
      if (resp.ok) renderState(await resp.json());
    });

    $("schedToggleBtn").addEventListener("click", () => {
      if (!appState) return;
      patchConfig({ schedule_enabled: !appState.config.schedule_enabled });
    });

    document.querySelectorAll("#schedPresets [data-mins]").forEach((btn) => {
      btn.addEventListener("click", () => {
        patchConfig({ schedule_enabled: true, schedule_interval_minutes: Number(btn.dataset.mins) });
      });
    });

    $("customMinForm").addEventListener("submit", (e) => {
      e.preventDefault();
      const mins = parseInt($("customMinInput").value.trim(), 10);
      if (mins > 0) {
        patchConfig({ schedule_enabled: true, schedule_interval_minutes: mins });
      }
    });

    $("startupToggleBtn").addEventListener("click", () => {
      if (!appState || !appState.startup_supported) return;
      patchConfig({ startup_enabled: !appState.startup_enabled });
    });

    $("alertsToggleBtn").addEventListener("click", async () => {
      if (!appState) return;
      const next = !appState.config.tray_notifications;
      if (next && "Notification" in window && Notification.permission === "default") {
        try { await Notification.requestPermission(); } catch (_) {}
      }
      patchConfig({ tray_notifications: next });
    });

    document.querySelectorAll("#alertThreshGroup [data-thresh]").forEach((btn) => {
      btn.addEventListener("click", () => patchConfig({ alert_min_download_mbps: Number(btn.dataset.thresh) }));
    });

    document.querySelectorAll("#threadsGroup [data-threads]").forEach((btn) => {
      btn.addEventListener("click", () => patchConfig({ threads: Number(btn.dataset.threads) }));
    });

    document.querySelectorAll("#durGroup [data-dur]").forEach((btn) => {
      btn.addEventListener("click", () => patchConfig({ duration_seconds: Number(btn.dataset.dur) }));
    });

    $("clearHistoryBtn").addEventListener("click", async () => {
      const resp = await fetch("/api/history?all=1", { method: "DELETE" });
      if (resp.ok) {
        historyItems = [];
        renderHistory();
        showToast("History cleared");
      }
    });

    window.addEventListener("resize", () => {
      if (appState) {
        drawSparkline($("dlSpark"), appState.dl_samples, "--accent-download", "rgba(56, 189, 248, 0.22)");
        drawSparkline($("ulSpark"), appState.ul_samples, "--accent-upload", "rgba(167, 139, 250, 0.22)");
      }
      if (activeTab === "history" && historyItems.length >= 2) {
        drawTrendChart($("trendCanvas"), historyItems);
      }
    });
  }

  initGaugeTicks();
  bindEvents();
  refreshHistory();
  connectSSE();
  setInterval(updateCountdown, 1000);
  requestAnimationFrame(animLoop);
})();
