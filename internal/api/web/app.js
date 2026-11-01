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
      try {
        const result = await request(path, "POST", map(Object.fromEntries(new FormData(element))));
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
  form("verify", "/v1/auth/verify", () => ({ token: mailToken }), () => { mailToken = ""; notice(t("verified_notice")); });
  form("reset", "/v1/auth/reset", value => ({ ...value, token: mailToken }), () => { mailToken = ""; notice(t("password_reset_notice")); });
  form("create-user", "/v1/admin/users", undefined, async () => {
    document.getElementById("create-user").reset();
    notice(t("user_created_notice")); await load();
  });
  let exportURL = "";
  window.addEventListener("pagehide", () => {
    if (!exportURL) return;
    URL.revokeObjectURL(exportURL); exportURL = "";
    document.getElementById("code-download").removeAttribute("href");
    document.getElementById("code-export").hidden = true;
  });
  form("codes", "/v1/admin/codes", value => {
    const days = Number(value.days), count = Number(value.count);
    if (!Number.isInteger(days) || days < 1 || days > 3650 || !Number.isInteger(count) || count < 1 || count > 100) {
      throw new Error(t("error_invalid_code_batch"));
    }
    return { days, count };
  }, async result => {
    if (!result.export_once || !Array.isArray(result.codes) || !result.codes.length || result.codes.some(code =>
      !/^[a-f0-9-]{36}$/.test(code.id) || !/^[A-Z2-7]{39}$/.test(code.code) || !Number.isInteger(code.days))) {
      throw new Error(t("code_generation_uncertain"));
    }
    // Keep plaintext only in a temporary download; the metadata list cannot recover it.
    const csv = ["id,code,days", ...result.codes.map(code => `${code.id},${code.code},${code.days}`)].join("\r\n") + "\r\n";
    const nextURL = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
    if (exportURL) URL.revokeObjectURL(exportURL);
    exportURL = nextURL;
    const link = document.getElementById("code-download"); link.href = exportURL;
    link.download = `bridge-codes-${Date.now()}.csv`;
    document.getElementById("code-export-summary").textContent = t("code_batch_ready", { count: result.codes.length });
    document.getElementById("code-export").hidden = false;
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
  let users = [];
  function renderUsers() {
    const parent = document.getElementById("users"); parent.replaceChildren();
    const filter = document.getElementById("user-filter").value.trim().toLowerCase();
    const matches = users.filter(user => `${user.login} ${user.id}`.toLowerCase().includes(filter));
    document.getElementById("user-count").textContent = t("user_list_count", { shown: matches.length, total: users.length });
    matches.forEach(user => {
      const current = user.id === document.body.dataset.accountId;
      const buttons = [!current && action(t(user.disabled ? "enable" : "disable"), user.login,
        () => request(`/v1/admin/users/${user.id}/disable`, "POST", { disabled: !user.disabled }))];
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
  async function loadUsers() {
    const value = await request("/v1/admin/users"); users = value.users; renderUsers();
  }
  async function loadOverview() {
    const status = await request("/v1/admin/status");
    document.getElementById("metric-gateways").textContent = status.gateways_online;
    document.getElementById("metric-mobiles").textContent = status.mobiles_online;
    document.getElementById("metric-pushes").textContent = status.push_queued;
    document.getElementById("runtime").textContent = t("runtime_capacity_help");
  }
  async function loadDevices() {
    const [gateways, bindings] = await Promise.all([request("/v1/gateways"), request("/v1/bindings")]);
    const parent = document.getElementById("devices"); parent.replaceChildren();
    gateways.gateways.forEach(gateway => tableRow(parent, [
      ["device_column", identity(gateway.name, gateway.id)], ["type_column", t("gateway_type")],
      ["status_column", tag(t(gateway.revoked_at ? "revoked" : "registered"), gateway.revoked_at ? "muted" : "good")],
      ["actions_column", actions(!gateway.revoked_at && action(t("revoke"), gateway.name, () => request(`/v1/gateways/${gateway.id}`, "DELETE")))]]));
    bindings.bindings.forEach(binding => tableRow(parent, [
      ["device_column", identity(binding.id, `${binding.gateway_id} / ${binding.installation_id}`)], ["type_column", t("binding_type")],
      ["status_column", tag(t(binding.status === "active" ? "binding_active" : binding.status), binding.status === "active" ? "good" : "muted")],
      ["actions_column", actions(binding.status !== "revoked" && action(t("revoke"), binding.id, () => request(`/v1/bindings/${binding.id}`, "DELETE")))]]));
    if (!parent.childNodes.length) empty(parent, 4);
  }
  async function loadSessions() {
    const value = await request("/v1/auth/sessions"); const parent = document.getElementById("sessions"); parent.replaceChildren();
    value.sessions.forEach(session => {
      const expired = session.revoked_at || new Date(session.expires_at) <= new Date();
      tableRow(parent, [["session_column", identity(session.current ? t("current_session") : t("session_column"), session.id)],
        ["status_column", tag(t(session.revoked_at ? "revoked" : expired ? "expired" : "active"), expired ? "muted" : "good")],
        ["expiry_column", date(session.expires_at)],
        ["actions_column", actions(!expired && action(t("revoke"), session.id, () => request(`/v1/auth/sessions/${session.id}`, "DELETE")))]]);
    });
    if (!parent.childNodes.length) empty(parent, 4);
  }
  async function loadAudit() {
    const value = await request("/v1/admin/audit"); const parent = document.getElementById("audit"); parent.replaceChildren();
    value.audit.forEach(record => tableRow(parent, [["time_column", date(record.created_at)],
      ["event_column", text[`audit_${record.action.replaceAll(".", "_")}`] || record.action],
      ["actor_column", record.actor_id || t("system_actor")], ["target_column", record.target_id || t("batch")]]));
    if (!parent.childNodes.length) empty(parent, 4);
  }
  async function loadCodes() {
    const value = await request("/v1/admin/codes"), parent = document.getElementById("code-list");
    parent.replaceChildren();
    value.codes.forEach(code => tableRow(parent, [["code_id_column", element("span", code.id, "record-id")],
      ["code_days", t("code_days_value", { days: code.days })],
      ["status_column", tag(t(code.used_at ? "code_used" : code.revoked_at ? "revoked" : "code_unused"), code.used_at || code.revoked_at ? "muted" : "good")],
      ["created_column", date(code.created_at)],
      ["actions_column", actions(!code.used_at && !code.revoked_at && action(t("revoke"), code.id, () => request(`/v1/admin/codes/${code.id}`, "DELETE")))]]));
    if (!parent.childNodes.length) empty(parent, 5);
  }
  async function loadRedemptions() {
    const value = await request("/v1/admin/redemptions"), parent = document.getElementById("redemptions");
    parent.replaceChildren();
    value.redemptions.forEach(record => tableRow(parent, [["account_column", identity(record.account_id, record.code_id)],
      ["code_days", t("code_days_value", { days: record.days })], ["time_column", date(record.created_at)], ["expiry_column", date(record.expires_at)]]));
    if (!parent.childNodes.length) empty(parent, 4);
  }
  let loading = false;
  let refreshPending = false;
  async function load() {
    if (document.body.dataset.admin !== "true") return;
    if (loading) { refreshPending = true; return; }
    loading = true; const refresh = document.getElementById("refresh"); refresh.disabled = true;
    const panels = [["runtime", loadOverview], ["users", loadUsers], ["devices", loadDevices], ["sessions", loadSessions], ["audit", loadAudit]];
    if (hosted) panels.push(["code-list", loadCodes], ["redemptions", loadRedemptions]);
    try {
      const results = await Promise.allSettled(panels.map(async ([id, read]) => {
        const parent = document.getElementById(id); parent.setAttribute("aria-busy", "true");
        try { await read(); }
        catch (error) {
          if (parent.tagName === "TBODY") { parent.replaceChildren(); empty(parent, id === "code-list" ? 5 : id === "users" && hosted ? 6 : 4, error.message); }
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
  document.getElementById("user-filter")?.addEventListener("input", renderUsers);
  document.getElementById("refresh")?.addEventListener("click", () => { notice(""); load(); });
  document.querySelectorAll(".admin-nav a").forEach(link => link.addEventListener("click", () => {
    document.querySelectorAll(".admin-nav a").forEach(item => item.removeAttribute("aria-current"));
    link.setAttribute("aria-current", "location");
  }));
  document.getElementById("logout")?.addEventListener("click", async () => {
    try { await request("/v1/auth/logout", "POST", {}); location.assign("/admin"); } catch (error) { notice(error.message, true); }
  });
  await load();
}
start().catch(error => notice(error.message || document.body.dataset.loadError, true));
