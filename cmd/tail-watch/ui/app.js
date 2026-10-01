const POLL_MS = 5000;
const REACHABILITY = ["icmp", "tsping"];
const FIELDS = [
  "name", "addr", "site", "severity", "parent", "network", "insecure_tls", "checks",
  "interval", "timeout", "fail_threshold", "recover_threshold", "flap_limit", "flap_window",
];
const INT_FIELDS = new Set(["fail_threshold", "recover_threshold", "flap_limit"]);

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const app = { monitors: [], signature: "", editing: null, deleting: null, probeSave: null, lastOk: true, peers: [], pickedName: "", checks: [] };

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data = null;
  if (res.status !== 204) {
    try {
      data = await res.json();
    } catch {
      data = null;
    }
  }
  return { status: res.status, ok: res.ok, data };
}

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (key === "class") node.className = value;
    else if (key === "text") node.textContent = value;
    else node.setAttribute(key, value);
  }
  for (const child of children) node.append(child);
  return node;
}

function showBanner(message, kind = "info") {
  const banner = $("#banner");
  banner.textContent = message;
  banner.className = `banner ${kind}`;
  banner.hidden = message === "";
}

async function refresh() {
  try {
    const res = await api("GET", "/api/v1/targets");
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    app.monitors = res.data ?? [];
    if (!app.lastOk) showBanner("");
    app.lastOk = true;
    render();
  } catch {
    app.lastOk = false;
    showBanner("Lost contact with the collector. Retrying every few seconds.", "error");
  }
}

function render() {
  const signature = JSON.stringify(app.monitors);
  if (signature === app.signature) return;
  app.signature = signature;
  const rows = $("#rows");
  rows.replaceChildren(...app.monitors.map(rowFor));
  $("#empty").hidden = app.monitors.length > 0;
  $(".table-wrap").hidden = app.monitors.length === 0;
}

function badge(state) {
  const label = state || "UNKNOWN";
  return el("span", { class: `badge state-${label.toLowerCase()}`, text: label });
}

function cell(label, ...children) {
  return el("td", { "data-label": label }, ...children);
}

function rowFor(m) {
  const name = el("div", { class: "name" }, el("strong", { text: m.name }));
  if (m.parent) name.append(el("span", { class: "muted", text: `behind ${m.parent}` }));
  const checks = el("div", { class: "checks" }, ...m.checks.map((c) => el("code", { text: c, title: c })));
  const source = el("span", {
    class: `badge source-${m.source}`,
    text: m.source === "config" ? "Config file" : "UI",
  });
  const actions = el("td", { class: "row-actions" });
  if (!m.readonly) {
    const edit = el("button", { type: "button", text: "Edit", "aria-label": `Edit ${m.name}` });
    edit.addEventListener("click", () => openEditor(m));
    const del = el("button", { type: "button", class: "quiet-danger", text: "Delete", "aria-label": `Delete ${m.name}` });
    del.addEventListener("click", () => openConfirm(m));
    actions.append(edit, del);
  }
  return el(
    "tr",
    { "data-name": m.name },
    cell("State", badge(m.state)),
    cell("Monitor", name),
    cell("Address", el("span", { class: "mono", text: m.addr })),
    cell("Site", document.createTextNode(m.site || "")),
    cell("Severity", document.createTextNode(m.severity)),
    cell("Checks", checks),
    cell("Source", source),
    actions,
  );
}

function fieldBox(name) {
  return $(`.field[data-field="${name}"]`);
}

function control(name) {
  return $(`#f-${name}`);
}

function clearErrors() {
  for (const box of document.querySelectorAll("#form .field")) {
    box.classList.remove("invalid");
    $(":scope > .error", box).textContent = "";
    $("input, select, textarea", box)?.removeAttribute("aria-invalid");
  }
  for (const row of document.querySelectorAll("#form .check-row")) {
    row.classList.remove("invalid");
    $(".check-error", row).textContent = "";
    for (const input of row.querySelectorAll("input, select")) input.removeAttribute("aria-invalid");
  }
  const formError = $("#form-error");
  formError.hidden = true;
  formError.textContent = "";
  const result = $("#check-result");
  result.hidden = true;
  result.textContent = "";
}

function showErrors(errors) {
  const general = [];
  for (const message of errors) {
    const match = /^([a-z_]+)(?:\[(\d+)\])?: (.*)$/s.exec(message);
    const row = match && match[1] === "checks" && match[2] !== undefined
      ? document.querySelectorAll("#form .check-row")[Number(match[2])]
      : undefined;
    if (row) {
      const target = $(".check-error", row);
      const text = match[3].replace(/^target [^:]+: /, "");
      target.textContent = target.textContent ? `${target.textContent}\n${text}` : text;
      row.classList.add("invalid");
      for (const input of row.querySelectorAll("input, select")) input.setAttribute("aria-invalid", "true");
      continue;
    }
    const box = match && fieldBox(match[1]);
    if (!box) {
      general.push(message);
      continue;
    }
    const prefix = match[2] === undefined ? "" : `Line ${Number(match[2]) + 1}: `;
    const text = prefix + match[3].replace(/^target [^:]+: /, "");
    const target = $(":scope > .error", box);
    target.textContent = target.textContent ? `${target.textContent}\n${text}` : text;
    box.classList.add("invalid");
    $("input, select, textarea", box)?.setAttribute("aria-invalid", "true");
  }
  if (general.length > 0) {
    const formError = $("#form-error");
    formError.textContent = general.join("\n");
    formError.hidden = false;
  }
  const first = document.querySelector("#form .check-row.invalid input, #form .check-row.invalid select, #form .field.invalid input, #form .field.invalid select, #form .field.invalid textarea");
  if (first) first.focus();
}

function fillParents(selected, self) {
  const select = control("parent");
  select.replaceChildren(el("option", { value: "", text: "None" }));
  for (const m of app.monitors) {
    if (m.name === self) continue;
    if (m.checks.length !== 1 || !REACHABILITY.includes(m.checks[0])) continue;
    select.append(el("option", { value: m.name, text: m.name }));
  }
  select.value = selected || "";
}

// loadPeers refreshes the address suggestions while the form is open. An
// unreachable endpoint, an unseeded list and an empty list all degrade to
// free text: the datalist stays empty and nothing is shown.
async function loadPeers() {
  app.peers = [];
  fillPeers();
  updateRouteHint();
  try {
    const res = await api("GET", "/api/v1/peers");
    if (res.ok && res.data && res.data.seeded) app.peers = res.data.peers ?? [];
  } catch {
    app.peers = [];
  }
  fillPeers();
  updateRouteHint();
}

function peerLabel(p) {
  const state = p.online === null || p.online === undefined ? "unknown" : p.online ? "online" : "offline";
  return [p.ips[0] ?? "", ...(p.tags ?? []), state].filter((s) => s !== "").join(" · ");
}

function fillPeers() {
  $("#peers").replaceChildren(
    ...app.peers.map((p) => el("option", { value: peerValue(p), label: peerLabel(p) })),
  );
}

function peerValue(p) {
  return p.name || p.ips[0] || "";
}

// fillNameFromPeer copies a chosen suggestion into Name. It leaves a name the
// operator typed alone, replaces one it filled earlier, and never touches the
// read-only name of an existing monitor.
function fillNameFromPeer() {
  if (app.editing) return;
  const value = control("addr").value;
  if (!app.peers.some((p) => peerValue(p) === value)) return;
  const name = control("name");
  if (name.value === "" || name.value === app.pickedName) {
    name.value = value;
    app.pickedName = value;
  }
}

function parseIPv4(s) {
  const parts = s.split(".");
  if (parts.length !== 4) return null;
  const bytes = new Uint8Array(4);
  for (let i = 0; i < 4; i++) {
    if (!/^\d{1,3}$/.test(parts[i])) return null;
    const n = Number(parts[i]);
    if (n > 255) return null;
    bytes[i] = n;
  }
  return bytes;
}

function parseIPv6(s) {
  if (s.includes(".")) return null;
  const halves = s.split("::");
  if (halves.length > 2) return null;
  const groups = (part) => {
    if (part === "") return [];
    const out = [];
    for (const g of part.split(":")) {
      if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return null;
      out.push(parseInt(g, 16));
    }
    return out;
  };
  const head = groups(halves[0]);
  const tail = halves.length === 2 ? groups(halves[1]) : [];
  if (head === null || tail === null) return null;
  const missing = 8 - head.length - tail.length;
  if (missing < 0 || (halves.length === 1 && missing !== 0)) return null;
  const all = [...head, ...Array(missing).fill(0), ...tail];
  const bytes = new Uint8Array(16);
  all.forEach((g, i) => {
    bytes[2 * i] = g >> 8;
    bytes[2 * i + 1] = g & 0xff;
  });
  return bytes;
}

function parseIP(s) {
  return parseIPv4(s) ?? parseIPv6(s);
}

function parsePrefix(s) {
  const slash = s.lastIndexOf("/");
  if (slash < 0) return null;
  const addr = parseIP(s.slice(0, slash));
  if (!addr) return null;
  const bits = Number(s.slice(slash + 1));
  if (!Number.isInteger(bits) || bits < 0 || bits > addr.length * 8) return null;
  return { addr, bits };
}

function prefixContains(prefix, addr) {
  if (addr.length !== prefix.addr.length) return false;
  let left = prefix.bits;
  for (let i = 0; i < addr.length && left > 0; i++) {
    const take = Math.min(left, 8);
    const mask = (0xff << (8 - take)) & 0xff;
    if ((addr[i] & mask) !== (prefix.addr[i] & mask)) return false;
    left -= take;
  }
  return true;
}

// routeOwner returns the name of the peer that routes the typed address, using
// the longest matching prefix, or "" when the text is not an IP or no peer
// routes it.
function routeOwner(text, peers) {
  const addr = parseIP(text.trim());
  if (!addr) return "";
  let best = null;
  for (const p of peers) {
    const name = peerValue(p);
    for (const r of p.routes ?? []) {
      const prefix = parsePrefix(r);
      if (!prefix || !prefixContains(prefix, addr)) continue;
      if (!best || prefix.bits > best.bits || (prefix.bits === best.bits && name < best.name)) {
        best = { bits: prefix.bits, name };
      }
    }
  }
  return best ? best.name : "";
}

// updateRouteHint shows the routing peer under the address field. The parent
// select is never touched.
function updateRouteHint() {
  const hint = $("#addr-route");
  const owner = routeOwner(control("addr").value, app.peers);
  hint.textContent = owner ? `Handled by ${owner}` : "";
  hint.hidden = !owner;
}

// --- check conversion: pure, no DOM; ui_checks_test.go runs this block ---

function blankCheck(type) {
  return { type, scheme: "http", port: "", path: "/", text: "", raw: "" };
}

function bracketed(addr) {
  const a = addr.trim();
  return a.includes(":") && !a.startsWith("[") ? `[${a}]` : a;
}

function normalHost(host) {
  return host.trim().toLowerCase().replace(/^\[(.*)\]$/, "$1").replace(/\.$/, "");
}

// checkToString builds the check string the collector accepts. The host of an
// HTTP check is the address, so it cannot differ from it.
function checkToString(row, addr) {
  switch (row.type) {
    case "icmp":
    case "tsping":
      return row.type;
    case "tcp":
      return `tcp:${row.port.trim()}`;
    case "http": {
      const port = row.port.trim();
      const path = row.path.trim().startsWith("/") ? row.path.trim() : `/${row.path.trim()}`;
      const text = row.text === "" ? "" : `|${row.text}`;
      return `http:${row.scheme}://${bracketed(addr)}${port === "" ? "" : `:${port}`}${path}${text}`;
    }
    default:
      return row.raw.trim();
  }
}

// checkFromString turns a stored check into a row. A string no typed row can
// express, or an HTTP check for a host other than the address, is a custom row
// that keeps the raw string.
function checkFromString(s, addr) {
  if (s === "icmp" || s === "tsping") return blankCheck(s);
  const tcp = /^tcp:(\d{1,5})$/.exec(s);
  if (tcp) return { ...blankCheck("tcp"), port: tcp[1] };
  const http = /^http:(https?):\/\/(\[[^\]/?#|]+\]|[^/:?#|[\]]+)(?::(\d+))?(\/[^#|]*)?(?:\|([\s\S]*))?$/.exec(s);
  if (http && normalHost(http[2]) === normalHost(addr)) {
    return { ...blankCheck("http"), scheme: http[1], port: http[3] ?? "", path: http[4] ?? "", text: http[5] ?? "" };
  }
  return { ...blankCheck("custom"), raw: s };
}

// --- end check conversion ---

const CHECK_TYPE_LABELS = [
  ["icmp", "Ping (ICMP)"],
  ["tsping", "Tailscale ping"],
  ["tcp", "TCP port"],
  ["http", "HTTP request"],
];

function checkInput(row, i, cls, label, field, props = {}) {
  const input = el("input", { class: cls, "aria-label": `Check ${i + 1} ${label}`, autocomplete: "off", spellcheck: "false", ...props });
  input.value = row[field];
  input.addEventListener("input", () => {
    row[field] = input.value;
  });
  return input;
}

function checkRowEl(row, i) {
  const box = el("div", { class: "check-row", role: "group", "aria-label": `Check ${i + 1}` });
  const type = el("select", { class: "check-type", "aria-label": `Check ${i + 1} type` });
  const labels = row.type === "custom" ? [...CHECK_TYPE_LABELS, ["custom", "Custom"]] : CHECK_TYPE_LABELS;
  for (const [value, label] of labels) type.append(el("option", { value, text: label }));
  type.value = row.type;
  type.addEventListener("change", () => {
    app.checks[i] = blankCheck(type.value);
    renderChecks(i);
  });
  box.append(type);
  if (row.type === "tcp") {
    box.append(checkInput(row, i, "check-port", "port", "port", { inputmode: "numeric", placeholder: "445" }));
  } else if (row.type === "http") {
    const scheme = el("select", { class: "check-scheme", "aria-label": `Check ${i + 1} scheme` },
      el("option", { value: "http", text: "http" }), el("option", { value: "https", text: "https" }));
    scheme.value = row.scheme;
    scheme.addEventListener("change", () => {
      row.scheme = scheme.value;
    });
    box.append(
      scheme,
      checkInput(row, i, "check-port", "port", "port", { inputmode: "numeric", placeholder: "port" }),
      checkInput(row, i, "check-path", "path", "path", { placeholder: "/" }),
      checkInput(row, i, "check-text", "must contain", "text", { placeholder: "must contain (optional)" }),
    );
  } else if (row.type === "custom") {
    box.append(checkInput(row, i, "check-raw", "text", "raw"));
  }
  const remove = el("button", { type: "button", class: "check-remove quiet-danger", text: "Remove", "aria-label": `Remove check ${i + 1}` });
  remove.addEventListener("click", () => {
    app.checks.splice(i, 1);
    renderChecks();
    $("#add-check").focus();
  });
  box.append(remove, el("p", { class: "error check-error" }));
  return box;
}

// renderChecks rebuilds the rows from app.checks and focuses the type select
// of row focusIndex, if given.
function renderChecks(focusIndex) {
  $("#check-rows").replaceChildren(...app.checks.map(checkRowEl));
  if (focusIndex !== undefined) $$(".check-type")[focusIndex]?.focus();
}

function notesFor(m) {
  const notes = [];
  if (m && m.state === "DOWN") {
    notes.push("This monitor is down. Saving closes its open incident; a new one opens if the new definition fails its checks.");
  }
  if (m && m.dependents && m.dependents.length > 0) {
    notes.push(`Other monitors depend on this one (${m.dependents.join(", ")}). Saving briefly releases them, and they may alert while it is down.`);
  }
  return notes.map((text) => el("p", { class: "notice warn", text }));
}

function openEditor(m) {
  app.editing = m ?? null;
  app.pickedName = "";
  clearErrors();
  $("#form").reset();
  const def = m ? m.definition ?? {} : {};
  $("#editor-title").textContent = m ? `Edit ${m.name}` : "Add monitor";
  $("#editor-notes").replaceChildren(...notesFor(m));
  for (const name of FIELDS) {
    if (name === "parent" || name === "checks") continue;
    const input = control(name);
    if (name === "insecure_tls") input.checked = Boolean(def.insecure_tls);
    else input.value = def[name] === undefined ? "" : String(def[name]);
  }
  app.checks = (def.checks ?? []).map((c) => checkFromString(c, def.addr ?? ""));
  renderChecks();
  fillParents(def.parent, m?.name);
  control("name").readOnly = Boolean(m);
  $("#editor details").open = ["interval", "timeout", "fail_threshold", "recover_threshold", "flap_limit", "flap_window"]
    .some((name) => def[name] !== undefined);
  $("#editor").showModal();
  loadPeers();
  (m ? control("addr") : control("name")).focus();
}

function collect() {
  const def = {};
  for (const name of FIELDS) {
    const input = control(name);
    if (name === "insecure_tls") {
      if (input.checked) def.insecure_tls = true;
    } else if (name === "checks") {
      const addr = control("addr").value;
      def.checks = app.checks.map((row) => checkToString(row, addr));
    } else {
      const value = input.value.trim();
      if (value === "") continue;
      def[name] = INT_FIELDS.has(name) && /^-?\d+$/.test(value) ? Number(value) : value;
    }
  }
  return def;
}

// persist runs the unchanged create or replace call for a definition whose
// probe passed or was confirmed.
async function persist(def) {
  const save = $("#save");
  save.disabled = true;
  try {
    const editing = app.editing;
    const res = editing
      ? await api("PUT", `/api/v1/targets/${encodeURIComponent(editing.name)}`, def)
      : await api("POST", "/api/v1/targets", def);
    if (res.ok) {
      $("#editor").close();
      showBanner(`Saved ${def.name}.`, "ok");
      await refresh();
      return;
    }
    showErrors(res.data?.errors ?? [`Request failed (HTTP ${res.status}).`]);
  } catch {
    showErrors(["Could not reach the collector."]);
  } finally {
    save.disabled = false;
  }
}

// submit probes the definition first. All checks passing saves at once; a
// failing check asks for confirmation; a failed probe saves nothing.
async function submit(event) {
  event.preventDefault();
  clearErrors();
  const def = collect();
  const save = $("#save");
  save.disabled = true;
  let probe = null;
  try {
    probe = await api("POST", "/api/v1/probe", def);
  } catch {
    probe = null;
  } finally {
    save.disabled = false;
  }
  if (!probe) {
    showErrors(["Could not reach the collector."]);
    return;
  }
  if (!probe.ok) {
    showErrors(probe.data?.errors ?? [`Probe failed (HTTP ${probe.status}).`]);
    return;
  }
  const failures = (probe.data?.results ?? [])
    .map((r, index) => ({ ...r, index }))
    .filter((r) => !r.ok);
  if (failures.length === 0) {
    await persist(def);
    return;
  }
  openProbeConfirm(def, failures);
}

function openProbeConfirm(def, failures) {
  app.probeSave = { def, failures };
  $("#probe-failures").replaceChildren(
    ...failures.map((f) => el("li", {}, el("code", { text: f.check }), document.createTextNode(`: ${f.error}`))),
  );
  $("#probe-confirm").showModal();
  $("#probe-confirm-no").focus();
}

// declineProbe closes the confirmation without saving and marks the failing
// check rows with the per-row error display, leaving every value intact.
function declineProbe() {
  $("#probe-confirm").close();
  const pending = app.probeSave;
  app.probeSave = null;
  if (!pending) return;
  const rows = document.querySelectorAll("#form .check-row");
  for (const f of pending.failures) {
    const row = rows[f.index];
    if (!row) continue;
    $(".check-error", row).textContent = f.error;
    row.classList.add("invalid");
    for (const input of row.querySelectorAll("input, select")) input.setAttribute("aria-invalid", "true");
  }
}

async function confirmProbeSave() {
  const pending = app.probeSave;
  app.probeSave = null;
  $("#probe-confirm").close();
  if (pending) await persist(pending.def);
}

async function check() {
  clearErrors();
  const res = await api("POST", "/api/v1/probe", collect()).catch(() => null);
  if (!res) {
    showErrors(["Could not reach the collector."]);
  } else if (res.ok) {
    const lines = (res.data?.results ?? []).map((r) => r.ok
      ? el("span", { class: "ok", text: `${r.check}: ok (${r.latency_ms} ms)` })
      : el("span", { class: "failed", text: `${r.check}: failed (${r.error})` }));
    const result = $("#check-result");
    if (lines.length > 0) result.replaceChildren(...lines);
    else result.textContent = "No checks to run.";
    result.hidden = false;
  } else {
    showErrors(res.data?.errors ?? [`Request failed (HTTP ${res.status}).`]);
  }
}

function openConfirm(m) {
  app.deleting = m;
  $("#confirm-title").textContent = `Delete ${m.name}?`;
  $("#confirm-text").textContent = "It stops being checked and is removed from the list.";
  const warning = $("#confirm-warning");
  const ok = $("#confirm-ok");
  ok.disabled = false;
  if (m.dependents && m.dependents.length > 0) {
    warning.textContent = `It is the parent of ${m.dependents.join(", ")}. Delete or change those first.`;
    warning.hidden = false;
    ok.disabled = true;
  } else if (m.state === "DOWN") {
    warning.textContent = "This monitor is down. Deleting it closes its open incident and sends a recovery marked as removed.";
    warning.hidden = false;
  } else {
    warning.hidden = true;
  }
  $("#confirm").showModal();
  $("#confirm-cancel").focus();
}

async function confirmDelete() {
  const m = app.deleting;
  if (!m) return;
  $("#confirm-ok").disabled = true;
  try {
    const res = await api("DELETE", `/api/v1/targets/${encodeURIComponent(m.name)}`);
    $("#confirm").close();
    if (res.ok) showBanner(`Deleted ${m.name}.`, "ok");
    else showBanner(res.data?.errors?.join(" ") ?? `Delete failed (HTTP ${res.status}).`, "error");
    await refresh();
  } catch {
    $("#confirm").close();
    showBanner("Could not reach the collector.", "error");
  }
}

$("#add").addEventListener("click", () => openEditor(null));
$("#add-check").addEventListener("click", () => {
  app.checks.push(blankCheck("icmp"));
  renderChecks(app.checks.length - 1);
});
$("#form").addEventListener("submit", submit);
$("#check").addEventListener("click", check);
$("#cancel").addEventListener("click", () => $("#editor").close());
$("#f-addr").addEventListener("input", () => {
  fillNameFromPeer();
  updateRouteHint();
});
$("#confirm-cancel").addEventListener("click", () => $("#confirm").close());
$("#confirm-ok").addEventListener("click", confirmDelete);
$("#probe-confirm-no").addEventListener("click", declineProbe);
$("#probe-confirm-yes").addEventListener("click", confirmProbeSave);
$("#probe-confirm").addEventListener("cancel", (event) => {
  event.preventDefault();
  declineProbe();
});
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});

refresh();
setInterval(() => {
  if (!document.hidden) refresh();
}, POLL_MS);
