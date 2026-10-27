// zipgo back-office: lists the managed site and every subdomain under it — or,
// for an "admin": "all" back-office, every domain on the server, each one a
// root of the tree — with an on/off switch each. Everything it shows comes from
// /api/sites, which reads the folders' .zipgoconfig.json; everything it changes
// goes back there.
"use strict";

const $ = (sel) => document.querySelector(sel);
const store = {
  get(k, d) { try { const v = localStorage.getItem("zipgo-admin:" + k); return v === null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem("zipgo-admin:" + k, JSON.stringify(v)); } catch { /* private mode */ } },
};

const state = {
  host: "",
  all: false,           // the back-office of every domain ("admin": "all")
  roots: [],            // ids of the top-level rows: [""], or one per domain
  domain: "",           // all: the domain chip picked ("" = every domain)
  sites: [],            // rows from the API, parent-first
  byId: new Map(),
  kids: new Map(),      // id → child ids
  sizes: null,          // id → {bytes, files}, filled in lazily
  view: store.get("view", "tree"),
  filter: "all",
  q: "",
  open: new Set(),       // expanded rows, per scope (see load)
  busy: new Set(),      // ids with a switch in flight
  busyFlags: new Set(), // "id\0name" of flags with a switch in flight
  sheet: null,          // id shown in the details sheet
};

// ---- API ---------------------------------------------------------------------

async function api(path, body) {
  const opts = body === undefined ? {} : {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
  };
  const res = await fetch(path, { ...opts, credentials: "same-origin", cache: "no-store" });
  if (res.status === 401) { location.reload(); throw new Error("Logged out"); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function load(data) {
  const all = data.scope === "all";
  if (all !== state.all || !state.roots.length) {
    // The two scopes' ids don't mix ("dev." vs "gabvdl.xyz/dev."): each keeps
    // its own expanded rows. Every domain starts folded — an overview.
    state.open = new Set(store.get(all ? "open:all" : "open", all ? [] : [""]));
    state.domain = all ? store.get("domain", "") : "";
  }
  state.all = all;
  state.host = data.host;
  state.roots = data.roots || [""];
  if (state.domain && !state.roots.includes(state.domain)) state.domain = "";
  state.sites = data.sites;
  state.byId = new Map(data.sites.map((s) => [s.id, s]));
  state.kids = new Map();
  for (const s of data.sites) {
    if (s.parent === null) continue;
    if (!state.kids.has(s.parent)) state.kids.set(s.parent, []);
    state.kids.get(s.parent).push(s.id);
  }
  document.title = "zipgo · " + (all ? "all domains" : data.host);
  render();
  if (state.sheet !== null) fillSheet();
}

async function refresh() {
  try { load(await api("api/sites")); } catch (e) { toast(e.message, null, true); }
}

async function loadSizes() {
  try { state.sizes = (await api("api/sizes")).sizes; render(); if (state.sheet !== null) fillSheet(); } catch { /* sizes are a nicety */ }
}

// ---- switching ---------------------------------------------------------------

function nested(id) {
  let n = 0;
  const walk = (k) => (state.kids.get(k) || []).forEach((c) => { n++; walk(c); });
  walk(id);
  return n;
}

// rootOf is the id of the top-level row a row sits under.
const rootOf = (id) => (state.all ? id.split("/")[0] : "");
const isRoot = (s) => s.parent === null;
// shown is whether a row belongs to the domain picked by a chip.
const shown = (s) => !state.domain || rootOf(s.id) === state.domain;
const plural = (n, w) => `${n} ${w}${n === 1 ? "" : "s"}`;

function toggleOpen(id) {
  state.open.has(id) ? state.open.delete(id) : state.open.add(id);
  store.set(state.all ? "open:all" : "open", [...state.open]);
  render();
}

async function flip(id, on, undoable = true) {
  const s = state.byId.get(id);
  state.busy.add(id);
  render();
  try {
    load(await api("api/switch", { id, on }));
    const n = nested(id);
    const msg = on ? `On · ${s.host}` : `Off · ${s.host}` + (n ? ` (+${n} nested)` : "");
    toast(msg, undoable ? () => flip(id, !on, false) : null);
  } catch (e) {
    toast(`${s.host}: ${e.message}`, null, true);
  } finally {
    state.busy.delete(id);
    render();
  }
}

// ---- feature flags -----------------------------------------------------------

async function flipFlag(id, name, on, undoable = true) {
  const s = state.byId.get(id);
  const key = id + "\0" + name;
  state.busyFlags.add(key);
  if (state.sheet !== null) fillSheet();
  try {
    load(await api("api/flag", { id, name, on }));
    toast(`${name} ${on ? "on" : "off"} · ${s.host}`, undoable ? () => flipFlag(id, name, !on, false) : null);
  } catch (e) {
    toast(`${name}: ${e.message}`, null, true);
  } finally {
    state.busyFlags.delete(key);
    if (state.sheet !== null) fillSheet();
  }
}

const flagCount = (s) => (s.flags || []).length;

// ---- rendering ---------------------------------------------------------------

function el(tag, props = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") e.className = v;
    else if (k === "text") e.textContent = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v !== false && v !== null && v !== undefined) e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children) if (c) e.append(c);
  return e;
}

function matches(s) {
  if (state.q && !s.host.includes(state.q)) return false;
  switch (state.filter) {
    case "on": return !s.off;
    case "off": return s.off;
    case "lock": return s.locked;
    case "flags": return flagCount(s) > 0;
  }
  return true;
}

function subtreeMatches(id) {
  return matches(state.byId.get(id)) || (state.kids.get(id) || []).some(subtreeMatches);
}

const ago = (iso) => {
  const d = (Date.now() - Date.parse(iso)) / 86400000;
  if (d < 1) return "today";
  if (d < 2) return "yesterday";
  if (d < 60) return `${Math.floor(d)} d ago`;
  if (d < 730) return `${Math.floor(d / 30)} mo ago`;
  return `${Math.floor(d / 365)} y ago`;
};

const bytes = (n) => {
  const u = ["B", "KB", "MB", "GB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + " " + u[i];
};

function metaLine(s) {
  const bits = [];
  if (state.all && isRoot(s)) {
    // A domain's row says what its switch would take down.
    const rows = state.sites.filter((x) => !x.self && rootOf(x.id) === s.id);
    const off = rows.filter((x) => x.off).length;
    bits.push(plural(rows.length, "site") + (off ? ` · ${off} off` : ""));
  }
  if (s.modified) bits.push("deployed " + ago(s.modified));
  const z = state.sizes && state.sizes[s.id];
  if (z && s.type !== "proxy" && s.type !== "redirect" && s.type !== "admin") bits.push(bytes(z.bytes));
  return bits.join(" · ");
}

function row(s, depth, label, chev) {
  const why = s.self ? "this page"
    : s.configError ? "unreadable .zipgoconfig.json — fix it by hand"
    : s.offBy ? `off through ${s.offBy}`
    : s.off ? "off — answers 410 Gone" : "";
  const meta = s.self ? "" : metaLine(s);
  const main = el("button", { class: "main", type: "button", onclick: () => openSheet(s.id), "aria-label": `Details of ${s.host}` },
    el("span", { class: "l1" },
      el("span", { class: "name", text: label }),
      chev && chev.count ? el("span", { class: "cnt", text: String(chev.count) }) : null,
      el("span", { class: "badge", text: s.type }),
      s.locked ? el("span", { title: "password-protected", text: "🔒" }) : null,
      flagCount(s) ? flagChip(s) : null),
    // A domain's name is its host already.
    state.all && isRoot(s) ? null : el("span", { class: "host", text: s.host }),
    meta ? el("span", { class: "meta", text: meta }) : null,
    why ? el("span", { class: s.self ? "meta" : "why", text: why }) : null);

  const lockedOut = s.self || !!s.configError || !!s.offBy;
  const sw = s.self ? el("span", { class: "sw-gap" }) : el("button", {
    class: "sw" + (state.busy.has(s.id) ? " busy" : ""), type: "button", role: "switch",
    "aria-checked": String(!s.off), "aria-label": `${s.host} ${s.off ? "off" : "on"}`,
    disabled: lockedOut || state.busy.has(s.id),
    title: s.offBy ? `Switch ${s.offBy} on first` : "",
    onclick: () => flip(s.id, s.off),
  });
  const open = s.url ? el("a", { class: "open", href: s.url, target: "_blank", rel: "noopener noreferrer", "aria-label": `Open ${s.host}`, text: "↗" }) : el("span", { class: "open" });

  const r = el("div", { class: "row" + (s.offBy ? " dim" : "") + (s.off && !s.offBy ? " isoff" : "") + (state.all && isRoot(s) ? " root" : "") },
    chev ? chev.node : null, main, open, sw);
  r.style.setProperty("--d", depth);
  return r;
}

function flagChip(s) {
  const on = s.flags.filter((f) => f.on).length;
  const total = s.flags.length;
  return el("span", {
    class: "flagc",
    title: `${on} of ${total} feature flag${total === 1 ? "" : "s"} on`,
    text: `⚑ ${on}/${total}`,
  });
}

function renderTree(out) {
  const forced = state.q !== "" || state.filter !== "all";
  const walk = (id, depth) => {
    if (!subtreeMatches(id)) return;
    const s = state.byId.get(id);
    const kids = state.kids.get(id) || [];
    // A domain picked by its chip is shown unfolded.
    const isOpen = forced || state.open.has(id) || (state.domain !== "" && id === state.domain);
    const chev = kids.length
      ? el("button", {
          class: "chev", type: "button", "aria-expanded": String(isOpen), "aria-label": (isOpen ? "Collapse " : "Expand ") + s.host,
          text: isOpen ? "▾" : "▸",
          onclick: () => toggleOpen(id),
        })
      : el("span", { class: "chev none" });
    // A domain's row carries its count in its meta line instead.
    const count = kids.length && !(state.all && isRoot(s)) ? nested(id) : 0;
    out.append(row(s, depth, s.label, { node: chev, count }));
    if (isOpen) kids.forEach((k) => walk(k, depth + 1));
  };
  state.roots.filter((r) => !state.domain || r === state.domain).forEach((r) => walk(r, 0));
}

function renderList(out) {
  // Group by domain on the back-office of every domain, else by the managed
  // site's direct child each host sits under.
  const groups = new Map();
  for (const s of state.sites) {
    if (!shown(s) || !matches(s)) continue;
    const top = state.all ? rootOf(s.id) : s.id === "" ? "" : s.id.split("/")[0];
    if (!groups.has(top)) groups.set(top, []);
    groups.get(top).push(s);
  }
  const top = $(".top").offsetHeight;
  for (const [g, rows] of groups) {
    const head = g === "" ? state.host : state.byId.get(g).host;
    const sec = el("div", { class: "sec", text: `${head} · ${rows.length}` });
    sec.style.setProperty("--top", top + "px");
    out.append(sec);
    // Labels drop the root's host: "card-editor.dev", not the whole name.
    const suffix = (s) => state.byId.get(rootOf(s.id)).host;
    rows
      .sort((a, b) => a.host.split(".").reverse().join(".").localeCompare(b.host.split(".").reverse().join(".")))
      .forEach((s) => out.append(row(s, 0, isRoot(s) ? s.host : s.host.slice(0, -suffix(s).length - 1))));
  }
}

// renderDomains is the back-office of every domain's chip row: one chip per
// domain, picking one narrows the page (counts included) to it.
function renderDomains() {
  const box = $("#domains");
  box.hidden = !state.all;
  if (!state.all) return;
  const count = (r) => state.sites.filter((s) => !s.self && rootOf(s.id) === r).length;
  const pick = (d) => { state.domain = d; store.set("domain", d); render(); };
  box.replaceChildren(
    el("button", { class: "chip dchip", type: "button", "aria-pressed": String(state.domain === ""), text: `All ${state.roots.length}`, onclick: () => pick("") }),
    ...state.roots.map((r) => el("button", {
      class: "chip dchip", type: "button", "aria-pressed": String(state.domain === r), onclick: () => pick(r),
    }, r, el("span", { class: "n", text: String(count(r)) }))),
  );
}

function render() {
  const all = state.sites.filter((s) => !s.self && shown(s));
  const off = all.filter((s) => s.off).length;
  const locked = all.filter((s) => s.locked).length;
  const flagged = all.filter((s) => flagCount(s) > 0).length;
  $("#domain").textContent = state.all ? "All domains" : state.host;
  const sum = $("#summary");
  sum.replaceChildren(
    ...(state.all && !state.domain ? [el("b", { text: String(state.roots.length) }), " domains · "] : []),
    el("b", { text: String(all.length) }), " sites · ", el("b", { text: String(all.length - off) }), " on · ",
    el("b", { class: "o", text: String(off) }), " off");
  renderDomains();

  const chips = [["all", `All ${all.length}`], ["on", `On ${all.length - off}`], ["off", `Off ${off}`], ["lock", `🔒 ${locked}`]];
  if (flagged) chips.push(["flags", `⚑ ${flagged}`]);
  $("#chips").replaceChildren(...chips.map(([k, t]) => el("button", {
    class: "chip", type: "button", "aria-pressed": String(state.filter === k), text: t,
    onclick: () => { state.filter = k; render(); },
  })));
  document.querySelectorAll(".seg button").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.view === state.view)));

  const list = $("#list");
  const frag = document.createDocumentFragment();
  if (state.roots.length) {
    if (state.view === "tree") renderTree(frag); else renderList(frag);
  }
  if (!frag.childNodes.length) frag.append(el("p", { class: "empty", text: state.sites.length ? "Nothing matches." : "Loading…" }));
  list.replaceChildren(frag);
  list.setAttribute("aria-busy", "false");
}

// ---- toast -------------------------------------------------------------------

let toastTimer;
function toast(msg, undo, err = false) {
  const t = $("#toast");
  t.querySelector("span").textContent = msg;
  const b = t.querySelector("button");
  b.hidden = !undo;
  b.onclick = () => { t.classList.remove("show"); undo && undo(); };
  t.classList.toggle("err", err);
  t.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove("show"), err ? 7000 : 5000);
}

// ---- details sheet -----------------------------------------------------------

function openSheet(id) {
  state.sheet = id;
  $("#generated").hidden = true;
  $("#lock-input").value = "";
  fillSheet();
  $("#sheet").showModal();
}

function fillSheet() {
  const s = state.byId.get(state.sheet);
  if (!s) { $("#sheet").close(); return; }
  $("#sheet-title").textContent = isRoot(s) ? s.host : s.label;
  const link = $("#sheet-link");
  link.textContent = s.host + (s.url ? " ↗" : "");
  if (s.url) link.href = s.url; else link.removeAttribute("href");

  const z = state.sizes && state.sizes[s.id];
  const facts = [
    ["Type", s.type],
    ["State", s.self ? "this page" : s.offBy ? `off through ${s.offBy}` : s.off ? "off (410 Gone)" : "on"],
    ["Deployed", s.modified ? `${new Date(s.modified).toLocaleString()} (${ago(s.modified)})` : "—"],
    ["Size", z ? `${bytes(z.bytes)} · ${z.files} file${z.files === 1 ? "" : "s"}` : state.sizes ? "—" : "…"],
    ["Folder", s.id === "" ? "(root)" : s.id + (state.all && isRoot(s) ? " (domain)" : "")],
  ];
  if (s.configError) facts.push(["Config", s.configError]);
  $("#sheet-facts").replaceChildren(...facts.flatMap(([k, v]) => [el("dt", { text: k }), el("dd", { text: v })]));
  fillFlags(s);

  const lockable = !s.self && !s.configError && s.type !== "folder";
  $("#sheet-lock").hidden = !lockable;
  if (!lockable) return;
  $("#lock-state").textContent = s.password
    ? "Visitors see zipgo's login page. Changing the password logs everyone out."
    : s.locked ? "Protected by HTTP basic auth (edit it in .zipgoconfig.json). A password adds zipgo's login page in front."
    : "Public. A password puts the whole site behind zipgo's login page — deploys keep it.";
  $("#lock-input").placeholder = s.password ? "New password" : "Password";
  $("#lock-save").textContent = s.password ? "Change" : "Set";
  $("#lock-remove").hidden = !s.password;
}

function fillFlags(s) {
  const flags = s.self || s.configError ? [] : s.flags || [];
  $("#sheet-flags").hidden = flags.length === 0;
  $("#flag-list").replaceChildren(...flags.map((f) => {
    const busy = state.busyFlags.has(s.id + "\0" + f.name);
    const paths = f.paths && f.paths.length ? (f.on ? "covers " : "closed: ") + f.paths.join(", ") : "";
    return el("li", { class: "flag" + (f.on ? "" : " isoff") },
      el("div", { class: "fl-text" },
        el("span", { class: "fl-name", text: f.name }),
        f.description ? el("span", { class: "fl-desc", text: f.description }) : null,
        paths ? el("span", { class: "fl-paths", text: paths }) : null),
      el("button", {
        class: "sw" + (busy ? " busy" : ""), type: "button", role: "switch",
        "aria-checked": String(f.on), "aria-label": `${f.name} ${f.on ? "on" : "off"}`,
        disabled: busy, onclick: () => flipFlag(s.id, f.name, !f.on),
      }));
  }));
}

async function lock(action, password) {
  const s = state.byId.get(state.sheet);
  const btns = document.querySelectorAll("#sheet-lock button");
  btns.forEach((b) => (b.disabled = true));
  try {
    const data = await api("api/password", { id: s.id, action, password });
    load(data);
    $("#lock-input").value = "";
    if (data.password) {
      $("#generated-value").textContent = data.password;
      $("#generated").hidden = false;
    } else {
      $("#generated").hidden = true;
    }
    toast(action === "remove" ? `🔓 ${s.host} is public` : `🔒 ${s.host} has a password`);
  } catch (e) {
    toast(e.message, null, true);
  } finally {
    btns.forEach((b) => (b.disabled = false));
  }
}

// ---- wiring ------------------------------------------------------------------

$("#search").addEventListener("input", (e) => { state.q = e.target.value.trim().toLowerCase(); render(); });
document.querySelectorAll(".seg button").forEach((b) => b.addEventListener("click", () => {
  state.view = b.dataset.view; store.set("view", state.view); render();
}));
$("#lock-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const pw = $("#lock-input").value;
  if (pw.length < 8) { toast("Use at least 8 characters", null, true); return; }
  lock("set", pw);
});
$("#lock-generate").addEventListener("click", () => lock("generate"));
$("#lock-remove").addEventListener("click", () => {
  const s = state.byId.get(state.sheet);
  if (confirm(`Remove the password? ${s.host} becomes public.`)) lock("remove");
});
$("#generated-copy").addEventListener("click", async () => {
  try { await navigator.clipboard.writeText($("#generated-value").textContent); toast("Copied"); } catch { /* select it by hand */ }
});
$("#sheet").addEventListener("close", () => { state.sheet = null; });
// Someone may have run `zipgo off` or deployed meanwhile: catch up on return.
document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });

render();
refresh().then(loadSizes);
