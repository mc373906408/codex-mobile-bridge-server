"use strict";
const csrf = document.querySelector('meta[name="csrf"]').content;
const message = document.getElementById("message");
const hosted = document.body.dataset.mode === "hosted";
const language = document.documentElement.lang;
let mailToken = new URLSearchParams(location.hash.slice(1)).get("token") || "";
if (location.hash) history.replaceState(null, "", location.pathname + location.search);
function notice(text, error = false) { message.textContent = text; message.dataset.error = String(error); }
document.querySelectorAll(".language-nav a").forEach(link => {
  link.addEventListener("click", event => {
    if (!mailToken) return;
    // Keep the mail token in memory and the fragment, never in a server query.
    event.preventDefault();
    const target = new URL(link.href);
    target.hash = new URLSearchParams({ token: mailToken }).toString();
    location.assign(target.href);
  });
});
async function start() {
  const localeResponse = await fetch(`/assets/locale_${language}.json`, { credentials: "same-origin", cache: "no-store" });
  if (!localeResponse.ok) throw new Error(document.body.dataset.loadError);
  const text = await localeResponse.json();
  const t = (key, values = {}) => (text[key] || key).replace(/\{(\w+)\}/g, (_, name) => String(values[name] ?? ""));

  async function request(path, method = "GET", data) {
    let response;
    try {
      response = await fetch(path, { method, credentials: "same-origin", redirect: "error", cache: "no-store",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: data === undefined ? undefined : JSON.stringify(data) });
    } catch {
      const error = new Error(t("network_error")); error.code = "network_error"; throw error;
    }
    const value = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(text[`error_${value.code}`] || t("request_failed"));
      error.code = value.code;
      error.status = response.status;
      throw error;
    }
    return value;
  }
  function form(id, path, map = value => value, after = () => location.assign("/")) {
    const element = document.getElementById(id);
    if (!element) return;
    element.addEventListener("submit", async event => {
      event.preventDefault();
      const buttons = [...element.querySelectorAll("button")];
      if (buttons.some(button => button.disabled)) return;
      buttons.forEach(button => button.disabled = true);
      const epoch = exportEpoch;
      try {
        const result = await request(path, "POST", map(Object.fromEntries(new FormData(element))));
        if (id === "codes" && epoch !== exportEpoch) return;
        await after(result);
      } catch (error) {
        notice(id === "codes" && error.code === "network_error" ? t("code_generation_uncertain") : error.message, true);
      } finally { buttons.forEach(button => button.disabled = false); }
    });
  }
  form("login", "/web/login", undefined, result => location.assign(result.credentials_change_required ? "/admin/setup" : "/admin"));
  form("administrator-setup", "/v1/admin/auth/setup", value => {
    if (value.password !== value.password_confirmation) throw new Error(t("password_mismatch"));
    return { login: value.login, password: value.password };
  }, () => location.assign("/admin?changed=1"));
  form("reset", "/v1/auth/reset", value => ({ ...value, token: mailToken }), () => { mailToken = ""; notice(t("password_reset_notice")); });
  form("create-user", "/v1/admin/users", undefined, async () => {
    document.getElementById("create-user").reset();
    notice(t("user_created_notice")); await load();
  });
  let exportURL = "";
  let exportEpoch = 0;
  const generatedCodes = new Map();
  let codeRecords = [];
  function clearCodeExport() {
    exportEpoch++;
    if (exportURL) URL.revokeObjectURL(exportURL);
    exportURL = "";
    generatedCodes.clear();
    document.getElementById("code-download")?.removeAttribute("href");
    document.getElementById("generated-code-list")?.replaceChildren();
    const panel = document.getElementById("code-export");
    if (panel) panel.hidden = true;
    if (document.getElementById("code-list")) renderCodes();
  }
  window.addEventListener("pagehide", clearCodeExport);
  form("codes", "/v1/admin/codes", value => {
    const days = Number(value.days), count = Number(value.count);
    if (!Number.isInteger(days) || days < 1 || days > 3650 || !Number.isInteger(count) || count < 1 || count > 100) {
      throw new Error(t("error_invalid_code_batch"));
    }
    return { days, count };
  }, async result => {
    if (!result.export_once || !Array.isArray(result.codes) || !result.codes.length || result.codes.length > 100 || result.codes.some(code =>
      !/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(code.id) ||
      !/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(code.batch_id) || code.batch_id !== result.codes[0].batch_id ||
      !/^(?:[A-Z2-7]{16}|[A-Z2-7]{39})$/.test(code.code) || !Number.isInteger(code.days) || code.days < 1 || code.days > 3650) ||
      new Set(result.codes.map(code => code.id)).size !== result.codes.length) {
      throw new Error(t("code_generation_uncertain"));
    }
    // Only the latest generation response stays in page memory, never browser storage.
    const csv = ["id,code,days,batch_id", ...result.codes.map(code => `${code.id},${code.code},${code.days},${code.batch_id}`)].join("\r\n") + "\r\n";
    const nextURL = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
    clearCodeExport();
    exportURL = nextURL;
    result.codes.forEach(code => generatedCodes.set(code.id, { code: code.code, days: code.days, batch_id: code.batch_id }));
    const link = document.getElementById("code-download"); link.href = exportURL;
    link.download = `bridge-codes-${Date.now()}.csv`;
    document.getElementById("code-export-summary").textContent = t("code_batch_ready", { count: result.codes.length }) + " / " + t("code_batch_id") + ": " + result.codes[0].batch_id;
    document.getElementById("code-export").hidden = false;
    renderCodes();
    link.click(); notice(t("codes_generated_notice")); await load();
  });
  function date(value) { return value ? new Date(value).toLocaleString(language) : t("not_activated"); }
  function element(tag, content, className = "") {
    const node = document.createElement(tag);
    node.textContent = content; node.className = className;
    return node;
  }
  function tag(content, state = "") { return element("span", content, `record-tag ${state}`); }
  function identity(primary, secondary) {
    const node = element("div", "", "record-identity");
    node.append(element("strong", primary), element("span", secondary, "record-id"));
    return node;
  }
  function action(label, item, execute) {
    const button = element("button", label, "secondary record-action");
    button.type = "button";
    button.addEventListener("click", async () => {
      if (!confirm(t("confirm_action", { action: label, item }))) return;
      button.disabled = true;
      try { await execute(); notice(t("action_complete")); await load(); }
      catch (error) { notice(error.message, true); button.disabled = false; }
    });
    return button;
  }
  function actions(...buttons) {
    const node = element("div", "", "record-actions");
    node.append(...buttons.filter(Boolean));
    if (!node.childNodes.length) node.append(element("span", t("no_actions"), "fine"));
    return node;
  }
  function tableRow(parent, columns) {
    const row = document.createElement("tr");
    columns.forEach(([label, value]) => {
      const cell = document.createElement("td"); cell.dataset.label = t(label);
      if (value instanceof Node) cell.append(value); else cell.textContent = String(value);
      row.append(cell);
    });
    parent.append(row);
  }
  function empty(parent, columns, content = t("no_records")) {
    const row = document.createElement("tr"); const cell = element("td", content, "empty-records");
    cell.colSpan = columns; row.append(cell); parent.append(row);
  }
  const pages = new Map();
  async function page(key, path, field, render, more = false) {
    let state = pages.get(key);
    if (more && (!state || state.path !== path || state.loading || !state.next)) return;
    if (!more) {
      state = { path, items: [], next: "", loading: false };
      pages.set(key, state);
    }
    const button = document.getElementById(`${key}-more`);
    state.loading = true;
    if (button) button.disabled = true;
    try {
      const url = more ? path + (path.includes("?") ? "&" : "?") + new URLSearchParams({ cursor: state.next }) : path;
      const value = await request(url);
      if (pages.get(key) !== state) return;
      if (!Array.isArray(value[field]) || typeof value.next_cursor !== "string") throw new Error(t("request_failed"));
      state.items = more ? [...state.items, ...value[field]] : value[field];
      state.next = value.next_cursor;
      render(state.items);
    } catch (error) {
      if (pages.get(key) !== state) return;
      if (!more) { state.items = []; state.next = ""; render([]); }
      throw error;
    } finally {
      state.loading = false;
      if (button && pages.get(key) === state) { button.hidden = !state.next; button.disabled = false; }
    }
  }
  let selectedAccount = { id: document.body.dataset.accountId, login: document.body.dataset.accountLogin };
  function selectedAccountLabel() {
    for (const id of ["selected-device-account", "selected-session-account"]) {
      const label = document.getElementById(id);
      if (label) label.textContent = t("managing_account", { login: selectedAccount.login, id: selectedAccount.id });
    }
  }
  async function selectAccount(user) {
    selectedAccount = { id: user.id, login: user.login };
    selectedAccountLabel();
    for (const key of ["devices", "mobile-devices", "bindings", "sessions"]) {
      pages.delete(key);
      document.getElementById(key)?.replaceChildren();
      const more = document.getElementById(`${key}-more`);
      if (more) more.hidden = true;
    }
    renderUsers();
    const results = await Promise.allSettled([loadDevices(), loadSessions()]);
    if (selectedAccount.id !== user.id) return;
    if (results.some(result => result.status === "rejected")) notice(t("panels_load_failed"), true);
    document.getElementById("device-management")?.scrollIntoView?.({ block: "start" });
  }
  let users = [];
  const topups = hosted && document.getElementById("topup-dialog")
    ? createAdminTopups({document,t,request,notice,reload:async()=>{await loadUsers();await loadAudit();},date}) : null;
  function renderUsers() {
    const parent = document.getElementById("users"); parent.replaceChildren();
    const filter = document.getElementById("user-filter").value.trim().toLowerCase();
    const matches = users;
    document.getElementById("user-count").textContent = t("user_list_count", { shown: matches.length, total: users.length });
    matches.forEach(user => {
      const current = user.id === document.body.dataset.accountId;
      const buttons = [!current && action(t(user.disabled ? "enable" : "disable"), user.login,
        () => request(`/v1/admin/users/${user.id}/disable`, "POST", { disabled: !user.disabled }))];
      const manage = element("button", t(selectedAccount.id === user.id ? "selected_account" : "manage_account"), "secondary record-action");
      manage.type = "button"; manage.disabled = selectedAccount.id === user.id;
      manage.addEventListener("click", () => selectAccount(user));
      buttons.unshift(manage);
      if (topups && !user.administrator && user.verified && !user.disabled) {
        const topup=element("button",t("topup_title"),"record-action topup-action");
        topup.type="button";topup.addEventListener("click",()=>topups.open(user));buttons.unshift(topup);
      }
      if (!user.administrator) {
        if (hosted) buttons.push(action(t("revoke_entitlement"), user.login, () => request(`/v1/admin/users/${user.id}/revoke-entitlement`, "POST", {})));
        else buttons.push(action(t("reset_password"), user.login, () => {
          const password = prompt(t("new_password_prompt"));
          if (!password) throw new Error(t("reset_cancelled"));
          return request(`/v1/admin/users/${user.id}/reset`, "POST", { password });
        }));
      }
      const columns = [["account_column", identity(user.login, user.id)],
        ["role_column", tag(t(current ? "current_administrator" : user.administrator ? "administrator_role" : "user_role"))],
        ["status_column", tag(t(user.disabled ? "disabled" : "enabled"), user.disabled ? "muted" : "good")]];
      if (hosted) columns.push(["email_column", tag(t(user.verified ? "verified" : "unverified"), user.verified ? "good" : "muted")],
        ["expiry_column", user.administrator ? t("not_applicable") : date(user.expires_at)]);
      columns.push(["actions_column", actions(...buttons)]); tableRow(parent, columns);
    });
    if (!matches.length) empty(parent, hosted ? 6 : 4, filter ? t("no_search_results") : t("no_records"));
  }
  async function loadUsers(more = false) {
    const search = document.getElementById("user-filter").value.trim();
    const path = search ? "/v1/admin/users?" + new URLSearchParams({ q: search }) : "/v1/admin/users";
    await page("users", path, "users", items => { users = items; renderUsers(); }, more);
  }
  async function loadOverview() {
    const status = await request("/v1/admin/status");
    document.getElementById("metric-gateways").textContent = status.gateways_online;
    document.getElementById("metric-mobiles").textContent = status.mobiles_online;
    document.getElementById("metric-pushes").textContent = status.push_queued;
    document.getElementById("runtime").textContent = t("runtime_capacity_help");
  }
  async function loadDeviceRole(role, more = false) {
    const account = { ...selectedAccount }, key = role === "gateway" ? "devices" : "mobile-devices";
    const resource = role === "gateway" ? "gateways" : "installations";
    await page(key, `/v1/admin/users/${account.id}/devices?role=${role}`, "devices", items => {
      const parent = document.getElementById(key); parent.replaceChildren();
      items.forEach(device => tableRow(parent, [
        ["device_column", identity(device.name, device.id)], ["type_column", t(role === "gateway" ? "gateway_type" : "mobile_type")],
        ["status_column", tag(t(device.revoked_at ? "revoked" : "registered"), device.revoked_at ? "muted" : "good")],
        ["actions_column", actions(!device.revoked_at && action(t("revoke"), `${account.login} / ${device.name} / ${device.id}`,
          () => request(`/v1/admin/users/${account.id}/${resource}/${device.id}`, "DELETE")))]]));
      if (!items.length) empty(parent, 4);
    }, more);
  }
  async function loadBindings(more = false) {
    const account = { ...selectedAccount };
    await page("bindings", `/v1/admin/users/${account.id}/bindings`, "bindings", items => {
      const parent = document.getElementById("bindings"); parent.replaceChildren();
      items.forEach(binding => tableRow(parent, [
        ["device_column", identity(`${binding.gateway_name} / ${binding.device_name}`, binding.id)], ["type_column", t("binding_type")],
        ["status_column", tag(t(binding.status === "active" ? "binding_active" : binding.status), binding.status === "active" ? "good" : "muted")],
        ["actions_column", actions(binding.status !== "revoked" && action(t("revoke"), `${account.login} / ${binding.id}`,
          () => request(`/v1/admin/users/${account.id}/bindings/${binding.id}`, "DELETE")))]]));
      if (!items.length) empty(parent, 4);
    }, more);
  }
  async function loadDevices() {
    selectedAccountLabel();
    const results = await Promise.allSettled([loadDeviceRole("gateway"), loadDeviceRole("mobile"), loadBindings()]);
    if (results.some(result => result.status === "rejected")) throw new Error(t("panels_load_failed"));
  }
  async function loadSessions(more = false) {
    const account = { ...selectedAccount };
    await page("sessions", `/v1/admin/users/${account.id}/sessions`, "sessions", items => {
    const parent = document.getElementById("sessions"); parent.replaceChildren();
    items.forEach(session => {
      const expired = session.revoked_at || new Date(session.expires_at) <= new Date();
      tableRow(parent, [["session_column", identity(session.current ? t("current_session") : t("session_column"), session.id)],
        ["status_column", tag(t(session.revoked_at ? "revoked" : expired ? "expired" : "active"), expired ? "muted" : "good")],
        ["expiry_column", date(session.expires_at)],
        ["actions_column", actions(!expired && action(t("revoke"), `${account.login} / ${session.id}`, () => request(`/v1/admin/users/${account.id}/sessions/${session.id}`, "DELETE")))]]);
    });
    if (!parent.childNodes.length) empty(parent, 4);
    }, more);
  }
  async function loadAudit(more = false) {
    await page("audit", "/v1/admin/audit", "audit", items => {
    const parent = document.getElementById("audit"); parent.replaceChildren();
    items.forEach(record => {
      const event = element("div", text[`audit_${record.action.replaceAll(".", "_")}`] || record.action);
      if (record.details?.before || record.details?.after) {
        const details = element("details", "");
        details.append(element("summary", t("audit_changes")), element("pre", JSON.stringify(record.details, null, 2), "audit-details"));
        event.append(details);
      }
      tableRow(parent, [["time_column", date(record.created_at)], ["event_column", event],
        ["actor_column", identity(record.actor_id || t("system_actor"), [record.session_id, record.device_id].filter(Boolean).join(" / "))],
        ["target_column", identity(record.target_id || t("batch"), record.account_id || "")]]);
    });
    if (!parent.childNodes.length) empty(parent, 4);
    }, more);
  }
  function copyCodeButton(code, value) {
    const button = element("button", t("copy_code"), "secondary record-action code-copy");
    button.type = "button";
    button.addEventListener("click", async () => {
      button.disabled = true;
      try { await navigator.clipboard.writeText(code); notice(t("code_copied_notice")); }
      catch {
        const range = document.createRange(); range.selectNodeContents(value);
        const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range);
        notice(t("code_copy_failed"), true);
      } finally { button.disabled = false; }
    });
    return button;
  }
  function codeIdentity(record) {
    const node = element("div", "", "code-identity");
    const generated = generatedCodes.get(record.id);
    if (generated) {
      const value = element("code", generated.code, "code-value");
      node.append(value);
      if (!record.used_at && !record.revoked_at) node.append(copyCodeButton(generated.code, value));
    } else {
      node.append(element("span", t("code_plaintext_unavailable")), element("span", t("code_plaintext_help"), "fine"));
    }
    const details = element("details", "", "code-record-details");
    details.append(element("summary", t("code_id_column")), element("span", record.id, "record-id"));
    node.append(details);
    node.append(element("span", t("code_batch_id") + ": " + record.batch_id, "fine record-id"));
    return node;
  }
  function renderGeneratedCodes() {
    const parent = document.getElementById("generated-code-list");
    if (!parent) return;
    parent.replaceChildren();
    let number = 0;
    generatedCodes.forEach((generated, id) => {
      const row = element("li", "", "generated-code");
      const content = element("div", "", "generated-code-content");
      const value = element("code", generated.code, "code-value");
      content.append(element("span", `${t("code_number", { number: ++number })} / ${t("code_days_value", { days: generated.days })}`, "fine"), value);
      row.append(content);
      const record = codeRecords.find(record => record.id === id);
      if (record?.used_at || record?.revoked_at) row.append(tag(t(record.used_at ? "code_used" : "revoked"), "muted"));
      else row.append(copyCodeButton(generated.code, value));
      parent.append(row);
    });
  }
  function renderCodes() {
    const parent = document.getElementById("code-list");
    parent.replaceChildren();
    codeRecords.forEach(code => tableRow(parent, [["code_column", codeIdentity(code)],
      ["code_days", t("code_days_value", { days: code.days })],
      ["status_column", tag(t(code.used_at ? "code_used" : code.revoked_at ? "revoked" : "code_unused"), code.used_at || code.revoked_at ? "muted" : "good")],
      ["created_column", date(code.created_at)],
      ["actions_column", actions(!code.used_at && !code.revoked_at && action(t("revoke"), code.id, () => request(`/v1/admin/codes/${code.id}`, "DELETE")))]]));
    if (!parent.childNodes.length) empty(parent, 5);
    renderGeneratedCodes();
  }
  async function loadCodes(more = false) {
    await page("codes", "/v1/admin/codes", "codes", items => { codeRecords = items; renderCodes(); }, more);
  }
  async function loadRedemptions(more = false) {
    await page("redemptions", "/v1/admin/redemptions", "redemptions", items => {
    const parent = document.getElementById("redemptions");
    parent.replaceChildren();
    items.forEach(record => tableRow(parent, [["account_column", identity(record.account_id, record.code_id)],
      ["code_days", t("code_days_value", { days: record.days })], ["time_column", date(record.created_at)], ["expiry_column", date(record.expires_at)]]));
    if (!parent.childNodes.length) empty(parent, 4);
    }, more);
  }
  let loading = false;
  let refreshPending = false;
  const campaigns = hosted && document.getElementById("campaign-management")
    ? createRegistrationCampaigns({ document, t, request, page, element, tableRow, empty, identity, date, notice }) : null;
  async function load() {
    if (document.body.dataset.admin !== "true") return;
    if (loading) { refreshPending = true; return; }
    loading = true; const refresh = document.getElementById("refresh"); refresh.disabled = true;
    const panels = [["runtime", loadOverview], ["users", loadUsers], ["devices", loadDevices], ["sessions", loadSessions], ["audit", loadAudit]];
    if (hosted) panels.push(["code-list", loadCodes], ["redemptions", loadRedemptions]);
    if (campaigns) panels.push(["campaigns", () => campaigns.load()]);
    try {
      const results = await Promise.allSettled(panels.map(async ([id, read]) => {
        const parent = document.getElementById(id); parent.setAttribute("aria-busy", "true");
        try { await read(); }
        catch (error) {
          if (parent.tagName === "TBODY") { parent.replaceChildren(); empty(parent, id === "code-list" || id === "campaigns" ? 5 : id === "users" && hosted ? 6 : 4, error.message); }
          else parent.textContent = error.message;
          throw error;
        } finally { parent.setAttribute("aria-busy", "false"); }
      }));
      if (results.some(result => result.status === "rejected")) notice(t("panels_load_failed"), true);
      document.getElementById("last-updated").textContent = t("last_updated", { time: date(new Date()) });
    } finally {
      loading = false; refresh.disabled = false;
      if (refreshPending) { refreshPending = false; await load(); }
    }
  }
  document.getElementById("user-filter")?.addEventListener("input", () => {
    users = []; renderUsers();
    loadUsers().catch(error => notice(error.message, true));
  });
  for (const [key, read] of [["users", loadUsers], ["devices", more => loadDeviceRole("gateway", more)],
    ["mobile-devices", more => loadDeviceRole("mobile", more)], ["bindings", loadBindings], ["sessions", loadSessions],
    ["audit", loadAudit], ["codes", loadCodes], ["redemptions", loadRedemptions]]) {
    document.getElementById(`${key}-more`)?.addEventListener("click", () => read(true).catch(error => notice(error.message, true)));
  }
  document.getElementById("refresh")?.addEventListener("click", () => { notice(""); load(); });
  document.querySelectorAll(".admin-nav a").forEach(link => link.addEventListener("click", () => {
    document.querySelectorAll(".admin-nav a").forEach(item => item.removeAttribute("aria-current"));
    link.setAttribute("aria-current", "location");
  }));
  document.getElementById("logout")?.addEventListener("click", async () => {
    try { await request("/v1/auth/logout", "POST", {}); clearCodeExport(); location.assign("/admin"); } catch (error) { notice(error.message, true); }
  });
  await load();
}
start().catch(error => notice(error.message || document.body.dataset.loadError, true));
