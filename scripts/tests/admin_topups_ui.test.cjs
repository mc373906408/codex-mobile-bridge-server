'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm'),crypto=require('node:crypto');
const web=path.resolve(__dirname,'../../internal/api/web');
const user={id:'11111111-1111-4111-8111-111111111111',login:'client@example.test',expires_at:null};
function fixture(language='zh-CN',storage=new Map()){
  class Node{constructor(){this.events={};this.disabled=false;this.hidden=false;this.dataset={};this.value='';this.textContent='';this.open=false;}addEventListener(name,fn){this.events[name]=fn;}focus(){}showModal(){this.open=true;}close(){this.open=false;}reportValidity(){return true;}}
  const nodes=new Map(['topup-dialog','topup-form','topup-submit','topup-result','topup-resume','topup-close','topup-account','topup-expiry'].map(id=>[id,new Node()]));
  const form=nodes.get('topup-form');form.elements={days:new Node(),note:new Node()};
  const calls=[],notices=[],texts=JSON.parse(fs.readFileSync(path.join(web,'locale_'+language+'.json'),'utf8'));
  let respond=async(_path,_method,input)=>({...input,account_id:user.id,expires_at:'2030-02-01T00:00:00Z'}),reloads=0,blocked=false;
  const context=vm.createContext({crypto,TextEncoder,sessionStorage:{getItem:key=>storage.get(key)||null,setItem:(key,value)=>{if(blocked)throw Error('blocked');storage.set(key,value);},removeItem:key=>storage.delete(key)}});
  vm.runInContext(fs.readFileSync(path.join(web,'topups.js'),'utf8'),context);
  const ui=context.createAdminTopups({document:{getElementById:id=>nodes.get(id),body:{dataset:{accountId:'test-admin'}}},t:(key,values={})=>texts[key].replace(/\{(\w+)\}/g,(_,k)=>values[k]??''),request:async(...args)=>{calls.push(args);return respond(...args);},notice:message=>notices.push(message),reload:async()=>reloads++,date:value=>value||texts.not_activated});
  return {ui,nodes,form,calls,notices,storage,texts,setResponse:fn=>respond=fn,blockStorage:()=>blocked=true,get reloads(){return reloads;},submit:()=>form.events.submit({preventDefault(){}})};
}
for(const language of ['zh-CN','en'])test('direct top-up names its account, defaults to 30 days and confirms the receipt in '+language,async()=>{
  const f=fixture(language);f.ui.open(user);assert.equal(f.form.elements.days.value,30);assert.match(f.nodes.get('topup-account').textContent,/client@example.test/);
  f.form.elements.days.value='7';f.form.elements.note.value=' support ';
  await f.submit();assert.equal(f.calls.length,1);assert.equal(f.calls[0][0],'/v1/admin/users/'+user.id+'/topups');assert.equal(f.calls[0][2].days,7);assert.equal(f.calls[0][2].note,'support');
  assert.equal(f.nodes.get('topup-dialog').open,false);assert.equal(f.reloads,1);assert.equal(f.storage.size,0);assert.match(f.notices[0],/7/);
});
test('uncertain top-up survives a reload and an explicit retry uses exactly the same request',async()=>{
  const f=fixture();f.ui.open(user);f.setResponse(async()=>{throw Error('lost response');});await f.submit();
  assert.equal(f.form.elements.days.disabled,true);assert.equal(f.nodes.get('topup-dialog').open,true);
  const next=fixture('en',f.storage);assert.equal(next.nodes.get('topup-resume').hidden,false);next.nodes.get('topup-resume').events.click();await next.submit();
  assert.deepEqual(JSON.parse(JSON.stringify(next.calls[0])),JSON.parse(JSON.stringify(f.calls[0])));assert.equal(next.storage.size,0);
});
test('pending submission blocks duplicates, edits and closing, then rejects an unrelated receipt',async()=>{
  const f=fixture();let finish;f.setResponse(()=>new Promise(resolve=>finish=resolve));f.ui.open(user);const pending=f.submit();await f.submit();
  assert.equal(f.calls.length,1);assert.equal(f.nodes.get('topup-close').disabled,true);assert.equal(f.form.elements.days.disabled,true);
  finish({account_id:'other-account',request_id:f.calls[0][2].request_id,days:30,expires_at:'2030-01-01'});await pending;
  assert.equal(f.storage.size,1);assert.equal(f.nodes.get('topup-dialog').open,true);assert.equal(f.reloads,0);
});
test('invalid days or blocked session storage never submit a top-up',async()=>{
  const f=fixture();f.ui.open(user);
  for(const value of ['0','-1','3651','1.5']){f.form.elements.days.value=value;await f.submit();}
  assert.equal(f.calls.length,0);f.form.elements.days.value='30';f.blockStorage();await f.submit();assert.equal(f.calls.length,0);
  assert.equal(f.nodes.get('topup-result').textContent,f.texts.topup_storage_failed);
});
