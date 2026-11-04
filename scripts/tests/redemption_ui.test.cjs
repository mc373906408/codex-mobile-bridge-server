'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {fixture,descendants}=require('./admin_fixture.cjs');
const batch='00000000-0000-4000-8000-000000000099';
function codeRecord(code,index=1){return {id:'00000000-0000-4000-8000-'+String(index).padStart(12,'0'),batch_id:batch,code,days:30};}

for(const language of ['en','zh-CN']){
  test('actual generated 16-character codes display, copy and export verbatim in '+language,async()=>{
    const ui=await fixture(language),code='K7D3NY5V2M6Q4BTA';
    await ui.generate([codeRecord(code),codeRecord('Q4B6T2K7D3NY5VMA',2)]);
    const nodes=descendants(ui.nodes.get('generated-code-list'));
    const values=nodes.filter(node=>node.className==='code-value');
    assert.equal(values.length,2);assert.equal(values[0].textContent,code);
    await nodes.find(node=>node.className?.includes('code-copy')).click();
    assert.equal(ui.clipboard,code);assert.equal(ui.nodes.get('message').textContent,ui.texts.code_copied_notice);
    assert.equal(ui.nodes.get('code-export').hidden,false);
    const csv=await ui.exports.get(ui.nodes.get('code-download').href).text();
    assert.ok(csv.startsWith('id,code,days,batch_id\r\n'));assert.ok(csv.includes(','+code+',30,'+batch+'\r\n'));
    assert.ok(ui.nodes.get('code-export-summary').textContent.includes(batch));
    assert.ok(descendants(ui.nodes.get('code-list')).some(node=>node.textContent===ui.texts.code_batch_id+': '+batch));
    ui.pagehide();assert.equal(ui.nodes.get('generated-code-list').children.length,0);
    assert.equal(ui.exports.size,0);assert.equal(ui.nodes.get('code-export').hidden,true);
    assert.equal(descendants(ui.nodes.get('code-list')).some(node=>node.textContent===code),false);
  });
}
test('rolling updates still display and export the previous 39-character random code format',async()=>{
  const ui=await fixture(),legacy='ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFG';
  assert.equal(legacy.length,39);await ui.generate([codeRecord(legacy)]);
  assert.equal(descendants(ui.nodes.get('generated-code-list')).find(node=>node.className==='code-value').textContent,legacy);
});
test('generated-code validation rejects wrong lengths, alphabets and duplicate record IDs without dropping a confirmed batch',async()=>{
  const ui=await fixture(),valid=codeRecord('K7D3NY5V2M6Q4BTA');await ui.generate([valid]);
  const url=ui.nodes.get('code-download').href;
  for(const code of ['ABCDEFGHIJKLMNO','ABCDEFGHIJKLMNOPQ','1234567890123456','k7d3ny5v2m6q4bta','00000000-0000-4000-8000-000000000001']){
    await ui.generate([codeRecord(code)]);
    assert.equal(ui.nodes.get('message').textContent,ui.texts.code_generation_uncertain);
    assert.equal(ui.nodes.get('code-download').href,url);
  }
  await ui.generate([valid,{...valid,code:'Q4B6T2K7D3NY5VMA'}]);
  assert.equal(ui.nodes.get('message').textContent,ui.texts.code_generation_uncertain);
  assert.equal(ui.requests.some(request=>request.url==='/v1/account/redeem'),false);
});
test('mixed or missing batch identities cannot replace a confirmed export',async()=>{
  const ui=await fixture(),valid=codeRecord('K7D3NY5V2M6Q4BTA');await ui.generate([valid]);
  const url=ui.nodes.get('code-download').href;
  for(const batch_id of [undefined,'invalid','00000000-0000-4000-8000-000000000098']){
    await ui.generate([valid,{...codeRecord('Q4B6T2K7D3NY5VMA',2),batch_id}]);
    assert.equal(ui.nodes.get('message').textContent,ui.texts.code_generation_uncertain);
    assert.equal(ui.nodes.get('code-download').href,url);
  }
});
