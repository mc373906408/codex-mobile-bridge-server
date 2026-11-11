"use strict";

function createAdminTopups({document, t, request, notice, reload, date}) {
  const dialog=document.getElementById("topup-dialog");
  if (!dialog) return null;
  const form=document.getElementById("topup-form"),days=form.elements.days,note=form.elements.note;
  const submit=document.getElementById("topup-submit"),feedback=document.getElementById("topup-result");
  const resume=document.getElementById("topup-resume"),close=document.getElementById("topup-close");
  const storageKey="bridge-admin-topup:"+document.body.dataset.accountId;
  let pending=null,target=null,busy=false;
  try {
    const saved=JSON.parse(sessionStorage.getItem(storageKey)||"null");
    if (saved && /^[0-9a-f-]{36}$/.test(saved.account_id) && /^[0-9a-f-]{36}$/.test(saved.request_id) &&
        Number.isInteger(saved.days) && saved.days>=1 && saved.days<=3650 && typeof saved.note==="string" && typeof saved.login==="string") pending=saved;
  } catch {}
  function clearPending() {pending=null;try{sessionStorage.removeItem(storageKey);}catch{}}
  function render() {
    resume.hidden=!pending;
    days.disabled=note.disabled=busy||!!pending;
    submit.disabled=close.disabled=busy;
    submit.textContent=t(pending?"topup_retry":"topup_confirm");
  }
  function open(user) {
    if (busy) return;
    target=pending?{id:pending.account_id,login:pending.login}:user;
    if (!target) return;
    document.getElementById("topup-account").textContent=target.login+" · "+target.id;
    document.getElementById("topup-expiry").textContent=t("topup_current_expiry",{date:pending?t("topup_check_result"):date(target.expires_at)});
    days.value=pending?pending.days:30;note.value=pending?pending.note:"";
    feedback.textContent=pending?t("topup_uncertain"):"";
    feedback.dataset.error=String(!!pending);
    render();dialog.showModal();(pending?submit:days).focus();
  }
  form.addEventListener("submit",async event=>{
    event.preventDefault();
    if (busy||!target||!form.reportValidity()) return;
    const recovering=!!pending;
    const operation=pending||{account_id:target.id,login:target.login,days:Number(days.value),note:note.value.trim(),request_id:crypto.randomUUID()};
    if (!Number.isInteger(operation.days)||operation.days<1||operation.days>3650||new TextEncoder().encode(operation.note).length>1000) {
      feedback.textContent=t("error_invalid_topup");feedback.dataset.error="true";return;
    }
    try {sessionStorage.setItem(storageKey,JSON.stringify(operation));}
    catch {feedback.textContent=t("topup_storage_failed");feedback.dataset.error="true";return;}
    pending=operation;busy=true;feedback.textContent=t("topup_working");feedback.dataset.error="false";render();
    try {
      const result=await request(`/v1/admin/users/${operation.account_id}/topups`,"POST",{days:operation.days,note:operation.note,request_id:operation.request_id});
      if (result.request_id!==operation.request_id||result.account_id!==operation.account_id||result.days!==operation.days||!Number.isFinite(Date.parse(result.expires_at))) throw Error("unconfirmed");
      clearPending();
      const completed=t("topup_complete",{login:operation.login,days:result.days,date:date(result.expires_at)});
      feedback.textContent=completed;notice(completed);dialog.close();
      try {await reload();} catch {notice(completed+" "+t("panels_load_failed"),true);}
    } catch(error) {
      if (!recovering && error.status>=400 && error.status<500) clearPending();
      feedback.textContent=pending?t("topup_uncertain"):error.message;feedback.dataset.error="true";
    } finally {busy=false;render();}
  });
  close.addEventListener("click",()=>{if(!busy)dialog.close();});
  dialog.addEventListener("cancel",event=>{if(busy)event.preventDefault();});
  resume.addEventListener("click",()=>open(null));
  render();
  return {open};
}
