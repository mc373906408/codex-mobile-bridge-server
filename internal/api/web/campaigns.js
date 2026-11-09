"use strict";
// Uses the same request, translation and scoped pagination helpers as the admin console.
globalThis.createRegistrationCampaigns = function ({ document, t, request, page, element, tableRow, empty, identity, date, notice }) {
  const get = id => document.getElementById(id);
  const root = get("campaign-management"), form = get("campaign-form");
  const base = "/v1/admin/registration-campaigns";
  let records = [], selected = null, editing = null, dirty = false, busy = false, loaded = false, initialized = false;
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  get("campaign-timezone").textContent = t("campaign_timezone", { zone });
  function localTime(value) {
    if (!value) return "";
    const d = new Date(value), pad = n => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }
  function editor(row) {
    editing = row?.id || ""; dirty = false; form.hidden = false;
    get("campaign-editor-title").textContent = t(row ? "campaign_edit" : "campaign_create");
    get("campaign-name").value = row?.name ?? t("campaign_default_name");
    get("campaign-days").value = String(row?.days ?? 7);
    get("campaign-starts").value = localTime(row?.starts_at);
    get("campaign-ends").value = localTime(row?.ends_at);
    get("campaign-limit").value = row?.max_grants == null ? "" : String(row.max_grants);
  }
  function hideEditor() { editing = null; dirty = false; form.hidden = true; }
  function button(key, fn) {
    const node = element("button", t(key), "record-action secondary"); node.type = "button";
    node.addEventListener("click", async () => {
      if (busy) return;
      try { await fn(); } catch (error) { notice(error.message, true); }
    });
    return node;
  }
  function lock(value) {
    busy = value;
    root.querySelectorAll("button").forEach(node => node.disabled = value);
    get("campaign-new").disabled = value || !loaded || records.some(row => row.status !== "ended");
  }
  async function change(row, action) {
    if (!confirm(t(action === "activate" ? "campaign_confirm_activate" : "campaign_confirm_stop", { name: row.name }))) return;
    lock(true);
    try { await request(`${base}/${row.id}/${action}`, "POST", {}); hideEditor(); await load(); notice(t("action_complete")); }
    finally { lock(false); }
  }
  async function select(row) {
    if (selected?.id !== row.id) {
      get("campaign-grants").replaceChildren(); empty(get("campaign-grants"), 4, t("loading"));
      get("campaign-grants-more").hidden = true;
    }
    selected = row;
    get("campaign-selected").textContent = row.name;
    await loadGrants();
  }
  async function loadGrants(more = false) {
    if (!selected) { empty(get("campaign-grants"), 4); return; }
    await page("campaign-grants", `${base}/${selected.id}/grants`, "grants", rows => {
      const parent = get("campaign-grants"); parent.replaceChildren();
      rows.forEach(row => tableRow(parent, [["account_column", identity(row.login, row.account_id)], ["campaign_days", t("code_days_value", { days: row.days })], ["time_column", date(row.granted_at)], ["expiry_column", date(row.expires_at)]]));
      if (!rows.length) empty(parent, 4);
    }, more);
  }
  function render(rows) {
    records = rows; const parent = get("campaigns"); parent.replaceChildren();
    rows.forEach(row => {
      const controls = element("div", "", "record-actions");
      if (row.status === "draft") {
        controls.append(button("campaign_edit", () => editor(row)), button("campaign_activate", () => change(row, "activate")));
      }
      if (row.status !== "ended") controls.append(button("campaign_stop", () => change(row, "stop")));
      controls.append(button("campaign_view_grants", () => select(row)));
      const status = element("div", t(`campaign_state_${row.status}`));
      if (row.end_reason) status.append(element("p", t(`campaign_reason_${row.end_reason}`), "fine"));
      const schedule = element("div", row.starts_at ? date(row.starts_at) : t("campaign_on_activation"));
      schedule.append(element("p", row.ends_at ? date(row.ends_at) : t("campaign_no_end"), "fine"));
      const stats = element("div", t("campaign_stats", { count: row.granted_count, days: row.granted_days }));
      stats.append(element("p", row.remaining_grants == null ? t("campaign_unlimited") : t("campaign_remaining", { count: row.remaining_grants }), "fine"));
      tableRow(parent, [["campaign_name", identity(row.name, t("code_days_value", { days: row.days }))], ["status_column", status], ["campaign_schedule", schedule], ["campaign_statistics", stats], ["actions_column", controls]]);
    });
    if (!rows.length) empty(parent, 5);
    const current = rows.find(row => row.status !== "ended");
    if (editing) {
      const activeEdit = rows.find(row => row.id === editing);
      if (!activeEdit || activeEdit.status !== "draft") hideEditor();
      else if (!dirty) editor(activeEdit);
    } else if (editing === "" && current) hideEditor();
    if (!initialized && current?.status === "draft") editor(current);
    loaded = initialized = true; lock(busy);
  }
  async function load(more = false) {
    let rows;
    try { await page("campaigns", base, "campaigns", value => { rows = value; }, more); }
    catch (error) { loaded = false; lock(busy); throw error; }
    if (!rows) return;
    render(rows);
    const row = records.find(row => row.id === selected?.id) || records[0];
    if (row) await select(row); else await loadGrants();
  }
  get("campaign-new").addEventListener("click", () => { if (!busy && loaded && !records.some(row => row.status !== "ended")) editor(null); });
  get("campaign-cancel").addEventListener("click", hideEditor);
  get("campaigns-more").addEventListener("click", () => load(true).catch(error => notice(error.message, true)));
  get("campaign-grants-more").addEventListener("click", () => loadGrants(true).catch(error => notice(error.message, true)));
  form.addEventListener("input", () => { dirty = true; });
  form.addEventListener("submit", async event => {
    event.preventDefault(); if (busy || editing === null) return;
    const value = id => get(id).value.trim();
    const stamp = id => value(id) ? new Date(value(id)).toISOString() : null;
    lock(true);
    try {
      const inValue = { name: value("campaign-name"), days: Number(value("campaign-days")), starts_at: stamp("campaign-starts"), ends_at: stamp("campaign-ends"), max_grants: value("campaign-limit") ? Number(value("campaign-limit")) : null };
      if (!inValue.name || !Number.isInteger(inValue.days) || inValue.days < 1 || inValue.days > 3650 || (inValue.max_grants !== null && (!Number.isInteger(inValue.max_grants) || inValue.max_grants < 1 || inValue.max_grants > 2147483647)) || (inValue.starts_at && inValue.ends_at && inValue.ends_at <= inValue.starts_at)) throw new Error(t("error_invalid_registration_campaign"));
      await request(editing ? `${base}/${editing}` : base, editing ? "PATCH" : "POST", inValue);
      hideEditor(); await load(); notice(t("campaign_saved"));
    } catch (error) { notice(error.message, true); }
    finally { lock(false); }
  });
  return { load };
};
