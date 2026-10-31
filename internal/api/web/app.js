"use strict";
const csrf = document.querySelector('meta[name="csrf"]').content;
const message = document.getElementById("message");
const hosted = document.body.dataset.mode === "hosted";
let mailToken = new URLSearchParams(location.hash.slice(1)).get("token") || "";
if (location.hash) history.replaceState(null, "", location.pathname);
function notice(text, error = false) { message.textContent = text; message.dataset.error = String(error); }
async function request(path, method = "GET", data) {
  const response = await fetch(path, { method, credentials: "same-origin", redirect: "error", cache: "no-store",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: data === undefined ? undefined : JSON.stringify(data) });
  const value = await response.json();
  if (!response.ok) throw new Error(value.code || "Request failed");
  return value;
}
function form(id, path, map = value => value, after = () => location.assign("/")) {
  const element = document.getElementById(id); if (!element) return;
  element.addEventListener("submit", async event => {
    event.preventDefault(); const buttons = [...element.querySelectorAll("button")];
    if (buttons.some(button => button.disabled)) return;
    buttons.forEach(button => button.disabled = true);
    try { const data = map(Object.fromEntries(new FormData(element)), event); const result = await request(typeof path === "function" ? path(event) : path, id === "delete-account" ? "DELETE" : "POST", data); await after(result); }
    catch (error) { notice(error.message, true); }
    finally { buttons.forEach(button => button.disabled = false); }
  });
}
form("login", "/web/login");
form("register", "/v1/auth/register", undefined, () => notice("If this address can be registered, a verification email has been queued. Check your mailbox."));
form("mail", event => `/v1/auth/${event.submitter.value}`, undefined, () => notice("If this account is eligible, an email has been queued."));
form("verify", "/v1/auth/verify", () => ({ token: mailToken }), () => { mailToken = ""; notice("Email verified. You can now sign in."); });
form("reset", "/v1/auth/reset", value => ({ ...value, token: mailToken }), () => { mailToken = ""; notice("Password reset. Sign in again. Encryption keys are not recovered."); });
form("redeem", "/v1/account/redeem", value => ({ ...value, request_id: crypto.randomUUID() }), async () => { notice("Service days redeemed."); await load(); });
form("create-user", "/v1/admin/users", undefined, async () => { notice("Local account created."); await load(); });
form("delete-account", "/v1/account", value => { if (!confirm("Permanently delete this account and its cloud devices?")) throw new Error("Deletion cancelled."); return value; });
form("codes", "/v1/admin/codes", value => ({ days: Number(value.days), count: Number(value.count) }), async result => {
  const lines = ["id,code,days", ...result.codes.map(code => `${code.id},${code.code},${code.days}`)];
  const url = URL.createObjectURL(new Blob([lines.join("\r\n")], { type: "text/csv" }));
  const link = document.createElement("a"); link.href = url; link.download = `bridge-redemption-${Date.now()}.csv`; link.click(); URL.revokeObjectURL(url);
  notice("Codes exported once. Keep the download private; the server cannot recover the codes."); await load();
});
function date(value) { return value ? new Date(value).toLocaleString() : "not activated"; }
function row(parent, text, action, label = "Revoke") {
  const element = document.createElement("div"); element.className = "item";
  const content = document.createElement("span"); content.textContent = text; element.append(content);
  if (action) { const button = document.createElement("button"); button.className = "secondary"; button.textContent = label;
    button.addEventListener("click", async () => { if (!confirm(`${label}: ${text}?`)) return; button.disabled = true;
      try { await action(); await load(); } catch (error) { notice(error.message, true); button.disabled = false; } }); element.append(button); }
  parent.append(element);
}
async function list(id, path, render) { const parent = document.getElementById(id); if (!parent) return;
  const value = await request(path); parent.replaceChildren(); render(parent, value); if (!parent.childNodes.length) row(parent, "No records."); }
async function load() {
  if (!document.getElementById("entitlement")) return;
  const entitlement = await request("/v1/account/entitlement");
  document.getElementById("entitlement").textContent = entitlement.required ? `${entitlement.active ? "ACTIVE" : "INACTIVE"} / expires ${date(entitlement.expires_at)} / server UTC ${entitlement.server_time}` : "SELF-HOST / no official payment check";
  await list("devices", "/v1/gateways", (parent, value) => value.gateways.forEach(g => row(parent, `${g.name} / ${g.id}${g.revoked_at ? " / revoked" : ""}`, g.revoked_at ? null : () => request(`/v1/gateways/${g.id}`, "DELETE"))));
  const bindings = await request("/v1/bindings"); bindings.bindings.forEach(b => row(document.getElementById("devices"), `Binding ${b.id} / ${b.status}`, b.status === "revoked" ? null : () => request(`/v1/bindings/${b.id}`, "DELETE")));
  await list("sessions", "/v1/auth/sessions", (parent, value) => value.sessions.forEach(s => row(parent, `${s.current ? "This session / " : ""}${s.id} / ${s.revoked_at ? "revoked" : date(s.expires_at)}`, s.revoked_at ? null : () => request(`/v1/auth/sessions/${s.id}`, "DELETE"))));
  if (!document.getElementById("users")) return;
  const status = await request("/v1/admin/status"); document.getElementById("runtime").textContent = `${status.gateways_online} desktops / ${status.mobiles_online} mobile channels / ${status.push_queued} queued pushes. Capacity requires deployment-specific load testing.`;
  await list("users", "/v1/admin/users", (parent, value) => value.users.forEach(u => { row(parent, `${u.login} / ${u.id} / ${u.disabled ? "disabled" : "enabled"} / ${date(u.expires_at)}`, () => request(`/v1/admin/users/${u.id}/disable`, "POST", { disabled: !u.disabled }), u.disabled ? "Enable" : "Disable");
    if (hosted) row(parent, `${u.login}: revoke entitlement`, () => request(`/v1/admin/users/${u.id}/revoke-entitlement`, "POST", {}), "Revoke service time");
    else row(parent, `${u.login}: reset local password`, () => { const password = prompt("New password (12+ characters)"); if (!password) throw new Error("Reset cancelled."); return request(`/v1/admin/users/${u.id}/reset`, "POST", { password }); }, "Reset password"); }));
  await list("code-list", "/v1/admin/codes", (parent, value) => value.codes.forEach(c => row(parent, `${c.id} / ${c.days} days / ${c.used_at ? "used" : c.revoked_at ? "revoked" : "unused"}`, c.used_at || c.revoked_at ? null : () => request(`/v1/admin/codes/${c.id}`, "DELETE"))));
  await list("redemptions", "/v1/admin/redemptions", (parent, value) => value.redemptions.forEach(r => row(parent, `${r.account_id} / ${r.days} days / ${date(r.created_at)}`)));
  await list("audit", "/v1/admin/audit", (parent, value) => value.audit.forEach(a => row(parent, `${date(a.created_at)} / ${a.action} / ${a.target_id || "batch"}`)));
}
document.getElementById("logout")?.addEventListener("click", async () => { try { await request("/v1/auth/logout", "POST", {}); location.assign("/"); } catch (error) { notice(error.message, true); } });
load().catch(error => notice(error.message, true));
