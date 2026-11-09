'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {fixture,descendants}=require('./admin_fixture.cjs');
const base='/v1/admin/registration-campaigns';
const text=node=>descendants(node).map(n=>n.textContent).join(' ');
const campaign=(id,status='draft')=>({id,name:'Campaign '+id,days:7,status,granted_count:0,granted_days:0,remaining_grants:null});
const control=(ui,id,key)=>descendants(ui.nodes.get('campaigns').children.find(row=>text(row).includes('Campaign '+id))).find(node=>node.tagName==='BUTTON'&&node.textContent===ui.texts[key]);
const submit=ui=>ui.nodes.get('campaign-form').events.submit({preventDefault(){}});
const refresh=async ui=>{await ui.nodes.get('refresh').click();await new Promise(resolve=>setImmediate(resolve));};

for(const language of ['en','zh-CN']) {
 test('campaign draft edits, timezone, locked activation and next period in '+language,async()=>{
  let row=campaign('a');
  const ui=await fixture(language,({url,method,body})=>{
   if(url===base&&method==='GET')return {campaigns:[row],next_cursor:''};
   if(url.endsWith('/grants'))return {grants:[],next_cursor:''};
   if(method==='PATCH'){row={...row,...JSON.parse(body)};return row;}
   if(url.endsWith('/activate')){row={...row,status:'running'};return row;}
   if(url.endsWith('/stop')){row={...row,status:'ended',end_reason:'stopped'};return row;}
   if(url===base&&method==='POST'){row={...campaign('b'),...JSON.parse(body)};return row;}
  });
  assert.equal(ui.nodes.get('campaign-new').disabled,true);
  assert.equal(ui.nodes.get('campaign-form').hidden,false);
  assert.match(ui.nodes.get('campaign-timezone').textContent,new RegExp(Intl.DateTimeFormat().resolvedOptions().timeZone));
  ui.nodes.get('campaign-name').value='Unsaved name';ui.nodes.get('campaign-form').events.input();
  await refresh(ui);
  assert.equal(ui.nodes.get('campaign-name').value,'Unsaved name','refresh discarded editor');
  ui.nodes.get('campaign-starts').value='2031-01-02T03:04:05';
  await submit(ui);
  const write=ui.requests.find(r=>r.method==='PATCH');
  const value=JSON.parse(write.body);
  assert.equal(value.starts_at,new Date('2031-01-02T03:04:05').toISOString());
  assert.equal(value.ends_at,null);assert.equal(value.max_grants,null);
  assert.equal(write.headers['X-CSRF-Token'],'fixture-csrf');
  // Restore a recognizable title so the row helper can find the campaign.
  row.name='Campaign a';await refresh(ui);
  await control(ui,'a','campaign_activate').click();
  assert.equal(ui.nodes.get('campaign-form').hidden,true);
  assert.equal(control(ui,'a','campaign_edit'),undefined);
  assert.ok(text(ui.nodes.get('campaigns')).includes(ui.texts.campaign_state_running));
  await control(ui,'a','campaign_stop').click();
  assert.ok(text(ui.nodes.get('campaigns')).includes(ui.texts.campaign_reason_stopped));
  assert.equal(ui.nodes.get('campaign-new').disabled,false);
  assert.equal(control(ui,'a','campaign_activate'),undefined);
  assert.equal(ui.confirmations.length,2);
  await ui.nodes.get('campaign-new').click();
  assert.equal(ui.nodes.get('campaign-days').value,'7');await submit(ui);
  assert.equal(ui.nodes.get('campaign-new').disabled,true);
  assert.equal(ui.requests.filter(r=>r.method==='POST'&&r.url===base).length,1);
 });
}

test('changing campaign fences stale grant pages and renders names as text',async()=>{
 let resolve;const slow=new Promise(r=>resolve=r);
 const a=campaign('a','ended'),b=campaign('b','ended');
 const ui=await fixture('en',({url})=>{
  if(url===base)return {campaigns:[a,b],next_cursor:''};
  if(url===base+'/a/grants')return {grants:[{id:'1',login:'first',account_id:'first-id',days:7}],next_cursor:'old-page'};
  if(url.includes('cursor=old-page'))return slow;
  if(url===base+'/b/grants')return {grants:[{id:'2',login:'<img onerror=bad>',account_id:'second-id',days:7}],next_cursor:''};
 });
 const pending=ui.nodes.get('campaign-grants-more').click();
 await control(ui,'b','campaign_view_grants').click();
 resolve({grants:[{id:'3',login:'wrong-campaign',account_id:'wrong',days:7}],next_cursor:''});await pending;
 assert.equal(ui.nodes.get('campaign-selected').textContent,'Campaign b');
 assert.ok(!text(ui.nodes.get('campaign-grants')).includes('wrong-campaign'));
 assert.ok(text(ui.nodes.get('campaign-grants')).includes('<img onerror=bad>'));
 assert.equal(descendants(ui.nodes.get('campaign-grants')).some(n=>n.tagName==='IMG'),false);
 assert.equal(ui.nodes.get('campaign-grants-more').hidden,true);
});

test('failed reads preserve unsaved draft and prevent creating a conflicting period',async()=>{
 let failed=false;
 const ui=await fixture('en',({url})=>{
  if(url===base){if(failed)throw new Error('synthetic unavailable');return {campaigns:[campaign('a')],next_cursor:''};}
  if(url.endsWith('/grants'))return {grants:[],next_cursor:''};
 });
 ui.nodes.get('campaign-name').value='keep this edit';ui.nodes.get('campaign-form').events.input();
 failed=true;await refresh(ui);
 assert.equal(ui.nodes.get('campaign-name').value,'keep this edit');
 assert.equal(ui.nodes.get('campaign-new').disabled,true);
 failed=false;await refresh(ui);
 assert.equal(ui.nodes.get('campaign-name').value,'keep this edit','retry discarded unsaved edits');
});
