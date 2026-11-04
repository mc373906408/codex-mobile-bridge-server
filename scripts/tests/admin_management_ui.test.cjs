'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {fixture,descendants,adminID}=require('./admin_fixture.cjs');
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function deferred(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};}
function user(n){return {id:'00000000-0000-4000-8000-'+String(n).padStart(12,'0'),login:'person-'+n,verified:true,administrator:false,disabled:false};}
function text(node){return descendants(node).map(item=>item.textContent).join(' ');}
function manage(ui,login){const row=ui.nodes.get('users').children.find(row=>text(row).includes(login));return descendants(row).find(node=>node.tagName==='BUTTON'&&node.textContent===ui.texts.manage_account);}

for(const language of ['en','zh-CN']){
  test('server search and cursor pagination reach unloaded accounts in '+language,async()=>{
    const first=user(2),second=user(3),searched=user(214);
    const ui=await fixture(language,({url})=>{
      const parsed=new URL(url,'https://fixture.test');
      if(parsed.pathname!=='/v1/admin/users')return;
      if(parsed.searchParams.get('q'))return {users:[searched],next_cursor:''};
      if(parsed.searchParams.get('cursor'))return {users:[second],next_cursor:''};
      return {users:[first],next_cursor:'position-token'};
    });
    assert.equal(ui.nodes.get('users-more').hidden,false);
    await ui.nodes.get('users-more').click();
    assert.ok(text(ui.nodes.get('users')).includes(first.login));
    assert.ok(text(ui.nodes.get('users')).includes(second.login));
    assert.equal(ui.nodes.get('users-more').hidden,true);
    ui.nodes.get('user-filter').value='person-214';
    ui.nodes.get('user-filter').events.input();await tick();
    assert.ok(text(ui.nodes.get('users')).includes(searched.login));
    assert.ok(!text(ui.nodes.get('users')).includes(first.login+' '));
    assert.ok(ui.requests.some(call=>call.url==='/v1/admin/users?q=person-214'));
  });
}

test('late search responses and failures cannot replace a newer search',async()=>{
  const slow=deferred();
  const ui=await fixture('en',({url})=>{
    if(url==='/v1/admin/users?q=slow')return slow.promise;
    if(url==='/v1/admin/users?q=current')return {users:[user(77)],next_cursor:''};
  });
  ui.nodes.get('user-filter').value='slow';ui.nodes.get('user-filter').events.input();
  ui.nodes.get('user-filter').value='current';ui.nodes.get('user-filter').events.input();await tick();
  slow.reject(new Error('synthetic old read failure'));await tick();
  assert.ok(text(ui.nodes.get('users')).includes('person-77'));
  assert.equal(ui.nodes.get('message').textContent,'');
});

test('changing selected accounts fences delayed devices and revokes only the displayed target',async()=>{
  const first=user(2),second=user(3),slow=deferred();
  const ui=await fixture('en',({url,method})=>{
    if(url==='/v1/admin/users')return {users:[first,second],next_cursor:''};
    if(url===`/v1/admin/users/${first.id}/devices?role=gateway`)return slow.promise;
    if(url===`/v1/admin/users/${second.id}/devices?role=gateway`)return {devices:[{id:'desktop-second',name:'Visible desktop',role:'gateway',revoked_at:null}],next_cursor:''};
    if(method==='DELETE')return {revoked:true};
  });
  const pending=manage(ui,first.login).click();
  await manage(ui,second.login).click();
  assert.ok(ui.nodes.get('selected-device-account').textContent.includes(second.login));
  assert.ok(ui.nodes.get('selected-session-account').textContent.includes(second.id));
  slow.resolve({devices:[{id:'old-desktop',name:'Late first desktop',role:'gateway'}],next_cursor:''});await pending;
  assert.ok(text(ui.nodes.get('devices')).includes('Visible desktop'));
  assert.ok(!text(ui.nodes.get('devices')).includes('Late first desktop'));
  await descendants(ui.nodes.get('devices')).find(node=>node.tagName==='BUTTON').click();
  const writes=ui.requests.filter(call=>call.method==='DELETE');
  assert.equal(writes.length,1);
  assert.equal(writes[0].url,`/v1/admin/users/${second.id}/gateways/desktop-second`);
  assert.equal(writes[0].headers['X-CSRF-Token'],'fixture-csrf');
  assert.ok(ui.confirmations[0].includes(second.login));
  assert.equal(ui.requests.some(call=>['/v1/gateways','/v1/bindings','/v1/auth/sessions'].includes(call.url)),false);
});

test('pagination only appends once while a page is loading and shows audit state as text',async()=>{
  const waiting=deferred();
  const ui=await fixture('en',({url})=>{
    if(url==='/v1/admin/audit')return {audit:[{id:'12',action:'binding.access',actor_id:adminID,account_id:user(2).id,target_id:'binding',created_at:'2030-01-01T00:00:00Z',details:{before:{status:'pending'},after:{status:'active'}}}],next_cursor:'audit-next'};
    if(url==='/v1/admin/audit?cursor=audit-next')return waiting.promise;
  });
  const button=ui.nodes.get('audit-more'),pending=button.click();
  button.click();
  assert.equal(ui.requests.filter(call=>call.url.includes('cursor=audit-next')).length,1);
  waiting.resolve({audit:[{id:'11',action:'synthetic.<script>',details:{after:{status:'<img onerror=bad>'}},created_at:'2029-01-01T00:00:00Z'}],next_cursor:''});
  await pending;
  assert.equal(ui.nodes.get('audit').children.length,2);
  assert.ok(text(ui.nodes.get('audit')).includes('pending'));
  assert.ok(text(ui.nodes.get('audit')).includes('<img onerror=bad>'));
  assert.equal(descendants(ui.nodes.get('audit')).some(node=>node.tagName==='IMG'||node.tagName==='SCRIPT'),false);
});
