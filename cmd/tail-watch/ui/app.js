const POLL_MS = 5000;
const REACHABILITY = ["icmp", "tsping"];
const FIELDS = [
  "name", "addr", "site", "severity", "parent", "network", "insecure_tls", "checks",
  "interval", "timeout", "fail_threshold", "recover_threshold", "flap_limit", "flap_window",
];
const INT_FIELDS = new Set(["fail_threshold", "recover_threshold", "flap_limit"]);

const $ = (selector, root = document) => root.querySelector(selector);

const app = { monitors: [], signature: "", editing: null, deleting: null, lastOk: true };

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
    $(".error", box).textContent = "";
    const input = $("input, select, textarea", box);
    input.removeAttribute("aria-invalid");
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
    const box = match && fieldBox(match[1]);
    if (!box) {
      general.push(message);
      continue;
    }
    const prefix = match[2] === undefined ? "" : `Line ${Number(match[2]) + 1}: `;
    const text = prefix + match[3].replace(/^target [^:]+: /, "");
    const target = $(".error", box);
    target.textContent = target.textContent ? `${target.textContent}\n${text}` : text;
    box.classList.add("invalid");
    $("input, select, textarea", box).setAttribute("aria-invalid", "true");
  }
  if (general.length > 0) {
    const formError = $("#form-error");
    formError.textContent = general.join("\n");
    formError.hidden = false;
  }
  const first = document.querySelector("#form .field.invalid input, #form .field.invalid select, #form .field.invalid textarea");
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
  clearErrors();
  $("#form").reset();
  const def = m ? m.definition ?? {} : {};
  $("#editor-title").textContent = m ? `Edit ${m.name}` : "Add monitor";
  $("#editor-notes").replaceChildren(...notesFor(m));
  for (const name of FIELDS) {
    if (name === "parent") continue;
    const input = control(name);
    if (name === "checks") input.value = (def.checks ?? []).join("\n");
    else if (name === "insecure_tls") input.checked = Boolean(def.insecure_tls);
    else input.value = def[name] === undefined ? "" : String(def[name]);
  }
  fillParents(def.parent, m?.name);
  control("name").readOnly = Boolean(m);
  $("#editor details").open = ["interval", "timeout", "fail_threshold", "recover_threshold", "flap_limit", "flap_window"]
    .some((name) => def[name] !== undefined);
  $("#editor").showModal();
  (m ? control("addr") : control("name")).focus();
}

function collect() {
  const def = {};
  for (const name of FIELDS) {
    const input = control(name);
    if (name === "insecure_tls") {
      if (input.checked) def.insecure_tls = true;
    } else if (name === "checks") {
      def.checks = input.value.split("\n").map((line) => line.trim()).filter((line) => line !== "");
    } else {
      const value = input.value.trim();
      if (value === "") continue;
      def[name] = INT_FIELDS.has(name) && /^-?\d+$/.test(value) ? Number(value) : value;
    }
  }
  return def;
}

async function submit(event) {
  event.preventDefault();
  clearErrors();
  const def = collect();
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

async function check() {
  clearErrors();
  const res = await api("POST", "/api/v1/validate", collect()).catch(() => null);
  if (!res) {
    showErrors(["Could not reach the collector."]);
  } else if (res.ok) {
    const result = $("#check-result");
    result.textContent = "Looks valid.";
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
$("#form").addEventListener("submit", submit);
$("#check").addEventListener("click", check);
$("#cancel").addEventListener("click", () => $("#editor").close());
$("#confirm-cancel").addEventListener("click", () => $("#confirm").close());
$("#confirm-ok").addEventListener("click", confirmDelete);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});

refresh();
setInterval(() => {
  if (!document.hidden) refresh();
}, POLL_MS);
