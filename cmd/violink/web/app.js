// Violink web UI. Plain JavaScript, no framework: the page asks /api/status
// once a second and redraws the tables; forms are only filled when you pick
// something, so typing is never interrupted.

"use strict";

let status = null;        // last /api/status reply
let page = "overview";
let selInput = null;      // name of the input open in the editor ("" = new)
let selSim = null;        // name of the simulation open in the editor ("" = new)

const $ = (id) => document.getElementById(id);

// ------------------------------------------------------------------ helpers

async function api(method, url, body) {
  const res = await fetch(url, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function toast(text, bad) {
  const t = $("toast");
  t.textContent = text;
  t.className = "show" + (bad ? " bad" : "");
  clearTimeout(t.timer);
  t.timer = setTimeout(() => (t.className = ""), 3500);
}

function html(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// Show control characters as escapes: "\r\n" -> \r\n, STX -> \x02
function escapeText(s) {
  return String(s ?? "").replace(/[\\\x00-\x1f\x7f]/g, (c) => {
    if (c === "\\") return "\\\\";
    if (c === "\r") return "\\r";
    if (c === "\n") return "\\n";
    if (c === "\t") return "\\t";
    return "\\x" + c.charCodeAt(0).toString(16).padStart(2, "0");
  });
}

// The other way: \r \n \t \\ \xHH typed in a field become the real characters.
function unescapeText(s) {
  return String(s ?? "").replace(/\\(x[0-9a-fA-F]{2}|.)/g, (m, e) => {
    if (e === "r") return "\r";
    if (e === "n") return "\n";
    if (e === "t") return "\t";
    if (e === "\\") return "\\";
    if (e[0] === "x" && e.length === 3) return String.fromCharCode(parseInt(e.slice(1), 16));
    return m;
  });
}

function age(t) {
  if (!t) return "–";
  const s = (Date.now() - new Date(t).getTime()) / 1000;
  if (s < 1) return "now";
  if (s < 60) return Math.round(s) + " s";
  if (s < 3600) return Math.round(s / 60) + " min";
  if (s < 86400) return Math.round(s / 3600) + " h";
  return Math.round(s / 86400) + " d";
}

function every(ms) {
  if (ms % 3600000 === 0) return ms / 3600000 + " h";
  if (ms % 60000 === 0) return ms / 60000 + " min";
  if (ms % 1000 === 0) return ms / 1000 + " s";
  return ms + " ms";
}

function clock(t) {
  return new Date(t).toLocaleTimeString([], { hour12: false });
}

function dotFor(text) {
  const s = (text || "").toLowerCase();
  if (s.startsWith("waiting") || s.includes("error")) return "bad";
  if (s.startsWith("disabled") || s.startsWith("stopped") || s.includes("0 client")) return "warn";
  if (s.startsWith("connected") || s.startsWith("open") || s.startsWith("listening") || s.startsWith("running") || s.startsWith("sending") || s.startsWith("virtual")) return "ok";
  return "";
}

function streamHTML(list) {
  if (!list.length) return '<span class="muted">nothing yet</span>';
  return list
    .map((r) => `<div class="${r.note || ""}"><span class="t">${clock(r.time)}</span>${html(escapeText(r.value))}</div>`)
    .join("");
}

function fillStream(el, list) {
  const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 20;
  el.innerHTML = streamHTML(list);
  if (atBottom) el.scrollTop = el.scrollHeight;
}

// ------------------------------------------------------------------ tabs

document.querySelectorAll("nav button").forEach((b) =>
  b.addEventListener("click", () => showPage(b.dataset.page))
);

window.addEventListener("hashchange", () => {
  const p = location.hash.slice(1);
  if (p !== page && $("page-" + p)) showPage(p);
});

function showPage(p) {
  page = p;
  document.querySelectorAll("nav button").forEach((b) => b.classList.toggle("active", b.dataset.page === p));
  document.querySelectorAll("section.page").forEach((s) => s.classList.toggle("active", s.id === "page-" + p));
  location.hash = p;
  refresh();
}

// ------------------------------------------------------------------ refresh loop

async function refresh() {
  try {
    status = await api("GET", "/api/status");
  } catch (e) {
    $("meta").textContent = "not connected to Violink";
    return;
  }
  const up = status.uptimeSec;
  const upText = up < 3600 ? Math.floor(up / 60) + " min" : up < 86400 ? Math.floor(up / 3600) + " h" : Math.floor(up / 86400) + " d";
  $("meta").textContent = `${status.version} · ${status.host} · up ${upText}`;

  if (page === "overview") drawOverview();
  if (page === "inputs") await drawInputsPage();
  if (page === "simulation") drawSimList();
  if (page === "monitor") await drawMonitor();
  if (page === "log") await drawLog();
  fillDatalists();
}

setInterval(refresh, 1000);

function fillDatalists() {
  const names = (status.inputs || []).map((i) => `<option value="${html(i.config.name)}">`).join("");
  if ($("input-names").innerHTML !== names) $("input-names").innerHTML = names;
}

// ------------------------------------------------------------------ overview

function drawOverview() {
  const ins = status.inputs || [];
  $("ov-inputs").innerHTML = ins.length
    ? ins.map((i) => `<tr>
        <td>${html(i.config.name)}${i.virtual ? '<span class="tag">virtual</span>' : ""}</td>
        <td>${html(i.config.type)}</td>
        <td><span class="dot ${dotFor(i.status)}"></span>${html(i.status)}${i.dropped ? ` <span class="err">${i.dropped} broken</span>` : ""}</td>
        <td class="value">${html(escapeText(i.last))}</td>
        <td>${age(i.lastTime)}</td>
        <td class="num">${i.count}</td></tr>`).join("")
    : '<tr><td colspan="6" class="empty">No inputs in violink.json yet. Add one on the Inputs page.</td></tr>';

  const scr = status.scripts || [];
  $("ov-scripts").innerHTML = scr.length
    ? scr.map((s) => `<tr>
        <td>${html(s.name)}</td>
        <td>${html((s.inputs || []).join(", ") || "–")} → ${html(s.output || "–")}${s.intervalMs ? `<span class="tag">every ${every(s.intervalMs)}</span>` : ""}</td>
        <td><span class="dot ${s.busy ? "warn" : dotFor(s.status)}"></span>${s.busy ? "busy" : html(s.status)}${s.dropped ? ` <span class="err">${s.dropped} dropped</span>` : ""}</td>
        <td class="num">${s.calls}</td>
        <td class="num">${s.errors}</td>
        <td class="err">${html(s.lastError)}</td></tr>`).join("")
    : '<tr><td colspan="6" class="empty">No scripts loaded. Put .js files in the scripts folder and restart.</td></tr>';

  const outs = status.outputs || [];
  $("ov-outputs").innerHTML = outs.length
    ? outs.map((o) => `<tr>
        <td>${html(o.config.name)}</td>
        <td>${html(o.config.type)}</td>
        <td><span class="dot ${dotFor(o.status)}"></span>${html(o.status)}</td>
        <td class="value">${html(escapeText(o.last))}</td>
        <td>${age(o.lastTime)}</td>
        <td class="num">${o.count}</td>
        <td class="num">${o.failed}</td></tr>`).join("")
    : '<tr><td colspan="7" class="empty">No outputs in violink.json.</td></tr>';
}

// ------------------------------------------------------------------ inputs page

const inForm = $("in-form");

async function drawInputsPage() {
  const ins = (status.inputs || []).filter((i) => !i.virtual);
  $("in-list").innerHTML = ins
    .map((i) => `<button class="${i.config.name === selInput ? "active" : ""}" data-name="${html(i.config.name)}">
        <span class="dot ${dotFor(i.status)}"></span>${html(i.config.name)}<small>${html(i.config.type)} · ${html(i.status)}</small></button>`)
    .join("") || '<p class="muted">No inputs yet.</p>';
  $("in-list").querySelectorAll("button").forEach((b) => b.addEventListener("click", () => openInput(b.dataset.name)));

  if (!$("in-type").options.length) {
    $("in-type").innerHTML = status.inputTypes.map((t) => `<option>${t}</option>`).join("");
    if (selInput === null && ins.length) openInput(ins[0].config.name);
    else if (selInput === null) newInput();
  }

  const name = selInput || inForm.elements["name"].value;
  if (name && status.inputs.some((i) => i.config.name === name)) {
    const list = await api("GET", "/api/recent?kind=input&name=" + encodeURIComponent(name)).catch(() => []);
    fillStream($("in-stream"), list);
  } else {
    $("in-stream").innerHTML = '<span class="muted">save the input first</span>';
  }
}

function openInput(name) {
  const i = status.inputs.find((x) => x.config.name === name);
  if (!i) return;
  selInput = name;
  fillInputForm(i.config);
  drawInputsPage();
}

function newInput() {
  selInput = "";
  fillInputForm({
    name: "", type: "tcp-client", enabled: true, address: "", baud: 9600, dataBits: 8, parity: "none", stopBits: "1",
    framing: { start: "", end: "\r\n", maxLength: 4096, keepMarkers: false, idleResetMs: 0 }, poll: [],
  });
  inForm.elements["name"].focus();
}

function fillInputForm(c) {
  for (const el of inForm.elements) {
    if (!el.name) continue;
    const path = el.name.split(".");
    let v = c;
    for (const p of path) v = v == null ? undefined : v[p];
    if (el.type === "checkbox") el.checked = !!v;
    else if (el.name.startsWith("framing.start") || el.name.startsWith("framing.end")) el.value = escapeText(v);
    else el.value = v ?? "";
  }
  $("in-polls").innerHTML = "";
  (c.poll || []).forEach((p) => addPollRow(p));
  showTypeFields();
}

function readInputForm() {
  const c = { framing: {}, poll: [] };
  for (const el of inForm.elements) {
    if (!el.name || el.name.startsWith("poll")) continue;
    let v = el.type === "checkbox" ? el.checked : el.type === "number" || ["baud", "dataBits"].includes(el.name) ? Number(el.value || 0) : el.value;
    if (el.name === "framing.start" || el.name === "framing.end") v = unescapeText(v);
    const path = el.name.split(".");
    if (path.length === 2) c[path[0]][path[1]] = v;
    else c[el.name] = v;
  }
  $("in-polls").querySelectorAll(".row").forEach((r) => {
    const cmd = unescapeText(r.querySelector(".poll-cmd").value);
    const ms = Number(r.querySelector(".poll-ms").value || 0);
    if (cmd) c.poll.push({ command: cmd, intervalMs: ms });
  });
  return c;
}

function addPollRow(p) {
  const row = document.createElement("div");
  row.className = "row";
  row.style.marginBottom = "8px";
  row.innerHTML = `
    <label>Command <input class="mono poll-cmd" value="${html(escapeText(p.command))}"></label>
    <label style="flex:0 0 140px">Every (ms) <input class="poll-ms" type="number" min="10" value="${p.intervalMs || 1000}"></label>
    <button type="button" class="btn danger">Remove</button>`;
  row.querySelector("button").addEventListener("click", () => row.remove());
  $("in-polls").appendChild(row);
}

// Show only the fields that belong to the chosen type.
function showTypeFields() {
  const type = $("in-type").value;
  inForm.querySelectorAll("[data-for]").forEach((el) => {
    el.style.display = el.dataset.for.split(" ").includes(type) ? "" : "none";
  });
  if (type === "com") loadPorts();
}

async function loadPorts() {
  const ports = await api("GET", "/api/ports").catch(() => []);
  $("ports").innerHTML = ports.map((p) => `<option value="${html(p)}">`).join("");
}

$("in-type").addEventListener("change", showTypeFields);
$("in-add-poll").addEventListener("click", () => addPollRow({ command: "", intervalMs: 1000 }));
$("in-new").addEventListener("click", newInput);

$("in-save").addEventListener("click", async () => {
  const c = readInputForm();
  try {
    await api("PUT", "/api/inputs", { original: selInput || "", input: c });
    selInput = c.name;
    toast(`Input ${c.name} saved and restarted`);
    refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

$("in-delete").addEventListener("click", async () => {
  if (!selInput) return newInput();
  if (!confirm(`Delete input "${selInput}" from violink.json?`)) return;
  try {
    await api("DELETE", "/api/inputs?name=" + encodeURIComponent(selInput));
    toast(`Input ${selInput} deleted`);
    selInput = null;
    $("in-type").innerHTML = ""; // makes the page pick the first input again
    refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

async function sendCommand() {
  const name = selInput;
  if (!name) return toast("Pick and save an input first", true);
  const text = unescapeText($("cmd-text").value) + unescapeText($("cmd-end").value);
  try {
    await api("POST", "/api/inputs/command", { name, text });
    toast("Sent to " + name);
  } catch (e) {
    toast(e.message, true);
  }
}
$("cmd-send").addEventListener("click", sendCommand);
$("cmd-text").addEventListener("keydown", (e) => { if (e.key === "Enter") sendCommand(); });

// ------------------------------------------------------------------ simulation page

const simForm = $("sim-form");

function drawSimList() {
  const sims = status.sims || [];
  $("sim-list").innerHTML = sims
    .map((s) => `<button class="${s.name === selSim ? "active" : ""}" data-name="${html(s.name)}">
        <span class="dot ${s.running ? "ok" : ""}"></span>${html(s.name)}<small>→ ${html(s.input)} · ${s.running ? "running, " + s.sent + " sent" : "stopped"}</small></button>`)
    .join("") || '<p class="muted">No simulations yet.</p>';
  $("sim-list").querySelectorAll("button").forEach((b) => b.addEventListener("click", () => openSim(b.dataset.name)));

  if (selSim === null) {
    if (sims.length) openSim(sims[0].name);
    else newSim();
  }
  const cur = sims.find((s) => s.name === selSim);
  $("sim-toggle").textContent = cur && cur.running ? "Stop" : "Start";
}

function openSim(name) {
  const s = (status.sims || []).find((x) => x.name === name);
  if (!s) return;
  selSim = name;
  simForm.elements["name"].value = s.name;
  simForm.input.value = s.input;
  simForm.intervalMs.value = s.intervalMs;
  simForm.order.value = s.order;
  simForm.checksum.value = s.checksum;
  simForm.autoStart.checked = s.autoStart;
  simForm.note.value = s.note || "";
  simForm.lines.value = (s.lines || []).join("\n");
  $("sim-out").innerHTML = "";
  drawSimList();
}

function newSim() {
  selSim = "";
  simForm.reset();
  simForm.intervalMs.value = 1000;
  simForm.lines.value = "$WIMWV,{walk:0:359:5:0},R,{wave:2:14:60:1},M,A";
  simForm.checksum.value = "nmea";
  $("sim-out").innerHTML = "";
}

function readSimForm() {
  return {
    name: simForm.elements["name"].value.trim(),
    input: simForm.input.value.trim(),
    intervalMs: Number(simForm.intervalMs.value || 1000),
    order: simForm.order.value,
    checksum: simForm.checksum.value,
    autoStart: simForm.autoStart.checked,
    note: simForm.note.value,
    lines: simForm.lines.value.split("\n").map((l) => l.replace(/\r$/, "")).filter((l) => l.trim() !== ""),
  };
}

$("sim-new").addEventListener("click", newSim);

$("sim-save").addEventListener("click", async () => {
  const s = readSimForm();
  try {
    await api("PUT", "/api/sims", { original: selSim || "", sim: s });
    selSim = s.name;
    toast(`Simulation ${s.name} saved`);
    refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

$("sim-toggle").addEventListener("click", async () => {
  if (!selSim) return toast("Save the simulation first", true);
  const cur = (status.sims || []).find((s) => s.name === selSim);
  try {
    await api("POST", `/api/sims/${cur && cur.running ? "stop" : "start"}?name=` + encodeURIComponent(selSim));
    refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

$("sim-preview").addEventListener("click", async () => {
  try {
    const lines = await api("POST", "/api/sims/preview", readSimForm());
    $("sim-out").innerHTML = lines.map((l) => `<div>${html(escapeText(l))}</div>`).join("");
  } catch (e) {
    $("sim-out").innerHTML = `<span class="err">${html(e.message)}</span>`;
  }
});

$("sim-delete").addEventListener("click", async () => {
  if (!selSim) return newSim();
  if (!confirm(`Delete simulation "${selSim}" and its file?`)) return;
  try {
    await api("DELETE", "/api/sims?name=" + encodeURIComponent(selSim));
    toast(`Simulation ${selSim} deleted`);
    selSim = null;
    refresh();
  } catch (e) {
    toast(e.message, true);
  }
});

$("inj-send").addEventListener("click", async () => {
  const name = $("inj-name").value.trim();
  try {
    await api("POST", "/api/inputs/inject", { name, text: unescapeText($("inj-text").value) });
    toast("Injected into " + name);
  } catch (e) {
    toast(e.message, true);
  }
});

// ------------------------------------------------------------------ monitor page

async function drawMonitor() {
  const outs = status.outputs || [];
  const grid = $("mon-outputs");
  // Build one card per output once; afterwards only fill the streams.
  const wanted = outs.map((o) => o.config.name).join("|");
  if (grid.dataset.names !== wanted) {
    grid.dataset.names = wanted;
    grid.innerHTML = outs.length
      ? outs.map((o, n) => `<div class="card"><h2>${html(o.config.name)} <span class="muted" style="font-weight:400" id="mon-st-${n}"></span></h2><div class="stream" id="mon-out-${n}"></div></div>`).join("")
      : '<p class="muted">No outputs in violink.json.</p>';
  }
  await Promise.all(outs.map(async (o, n) => {
    $("mon-st-" + n).textContent = `${o.config.type} · ${o.status}`;
    const list = await api("GET", "/api/recent?kind=output&name=" + encodeURIComponent(o.config.name)).catch(() => []);
    fillStream($("mon-out-" + n), list);
  }));

  const sel = $("mon-input");
  const names = (status.inputs || []).map((i) => i.config.name);
  if (sel.dataset.names !== names.join("|")) {
    const keep = sel.value;
    sel.dataset.names = names.join("|");
    sel.innerHTML = names.map((n) => `<option>${html(n)}</option>`).join("");
    if (names.includes(keep)) sel.value = keep;
  }
  if (sel.value) {
    const list = await api("GET", "/api/recent?kind=input&name=" + encodeURIComponent(sel.value)).catch(() => []);
    fillStream($("mon-input-stream"), list);
  }
}

// ------------------------------------------------------------------ log page

const levelRank = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };

async function drawLog() {
  const lines = await api("GET", "/api/log").catch(() => []);
  const min = Number($("log-level").value);
  const box = $("log-lines");
  box.innerHTML = lines
    .filter((l) => levelRank[l.level] >= min)
    .reverse()
    .map((l) => `<div class="log-line ${l.level}">${new Date(l.time).toLocaleString([], { hour12: false })}  ${l.level.padEnd(5)}  ${html(l.text)}</div>`)
    .join("") || '<span class="muted">nothing logged at this level</span>';
}
$("log-level").addEventListener("change", drawLog);

// ------------------------------------------------------------------ start

showPage(["overview", "inputs", "simulation", "monitor", "log"].includes(location.hash.slice(1)) ? location.hash.slice(1) : "overview");

// Some words carry more than their letters. Type them anywhere outside a field.
let typed = "";
document.addEventListener("keydown", (e) => {
  if (e.target.closest("input, textarea, select") || e.key.length !== 1) return;
  typed = (typed + e.key.toLowerCase()).slice(-16);
  if (typed.endsWith("violet")) {
    for (let n = 0; n < 40; n++) {
      const p = document.createElement("div");
      p.className = "petal";
      p.style.left = Math.random() * 100 + "vw";
      p.style.setProperty("--drift", (Math.random() * 200 - 100) + "px");
      p.style.animationDuration = 4 + Math.random() * 4 + "s";
      p.style.animationDelay = Math.random() * 2 + "s";
      p.style.background = ["#b58cff", "#cdb6ff", "#6b3fc4", "#f4efff"][n % 4];
      document.body.appendChild(p);
      setTimeout(() => p.remove(), 11000);
    }
    toast("Some messages take a lifetime to understand.");
  }
  if (typed.endsWith("elpsykongroo")) toast("El Psy Kongroo. The worldline holds.");
  if (typed.endsWith("orth")) toast("A bridge over the abyss stands here.");
});
