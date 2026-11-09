'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const web=path.resolve(__dirname,'../../internal/api/web');
const source=fs.readFileSync(path.join(web,'app.js'),'utf8');
class Node {
  constructor(tag='div'){this.tagName=tag.toUpperCase();this.dataset={};this.children=[];this.events={};this.value='';this.disabled=false;this.hidden=false;this.textContent='';}
  get childNodes(){return this.children;}
  append(...nodes){this.children.push(...nodes);}
  replaceChildren(...nodes){this.children=nodes;}
  setAttribute(name,value){this[name]=value;}
  removeAttribute(name){delete this[name];}
  addEventListener(name,handler){this.events[name]=handler;}
  querySelectorAll(selector){return selector==='button'?descendants(this).slice(1).filter(node=>node.tagName==='BUTTON'):[];}
  click(){if(!this.disabled)return this.events.click?.();}
}
function descendants(node){return [node,...node.children.flatMap(descendants)];}
const adminID='00000000-0000-4000-8000-000000000001';
async function fixture(language='en',route){
  const ids=['message','codes','code-download','code-export','code-export-summary','generated-code-list','code-list','refresh',
    'users','user-filter','user-count','devices','mobile-devices','bindings','sessions','audit','redemptions','last-updated','runtime',
    'metric-gateways','metric-mobiles','metric-pushes','selected-device-account','selected-session-account','device-management',
    'campaign-management','campaign-form','campaign-new','campaign-editor-title','campaign-timezone','campaign-name','campaign-days',
    'campaign-starts','campaign-ends','campaign-limit','campaign-save','campaign-cancel','campaigns','campaign-grants','campaign-selected','campaigns-more','campaign-grants-more',
    ...['users','devices','mobile-devices','bindings','sessions','audit','codes','redemptions'].map(id=>id+'-more')];
  const nodes=new Map(ids.map(id=>[id,new Node()]));
  for(const id of ['users','devices','mobile-devices','bindings','sessions','audit','redemptions','code-list'])nodes.get(id).tagName='TBODY';
  const form=nodes.get('codes');form.append(new Node('button'));
  for(const id of ['campaign-new','campaign-save','campaign-cancel','campaigns-more','campaign-grants-more'])nodes.get(id).tagName='BUTTON';
  nodes.get('campaign-management').append(...['campaign-new','campaign-form','campaigns','campaign-grants','campaigns-more','campaign-grants-more'].map(id=>nodes.get(id)));
  nodes.get('campaign-form').append(nodes.get('campaign-save'),nodes.get('campaign-cancel'));
  const listeners=new Map(),requests=[],exports=new Map(),confirmations=[];let batch,clipboard='',records=[];
  const texts=JSON.parse(fs.readFileSync(path.join(web,'locale_'+language+'.json'),'utf8'));
  const document={documentElement:{lang:language},body:{dataset:{admin:'true',mode:'hosted',accountId:adminID,accountLogin:'operator'}},
    querySelector:()=>({content:'fixture-csrf'}),querySelectorAll:()=>[],getElementById:id=>nodes.get(id),createElement:tag=>new Node(tag)};
  const location={hash:'',pathname:'/admin',search:'',assign(){}};
  const window={addEventListener:(name,handler)=>listeners.set(name,handler)};
  class FormData {*[Symbol.iterator](){yield ['days','30'];yield ['count','2'];}}
  class URL extends globalThis.URL {
    static createObjectURL(blob){const key='blob:fixture-'+exports.size;exports.set(key,blob);return key;}
    static revokeObjectURL(key){exports.delete(key);}
  }
  const context={document,window,location,history:{replaceState(){}},FormData,Node,URL,URLSearchParams,Blob,
    confirm:value=>{confirmations.push(value);return true;},prompt:()=>null,
    navigator:{clipboard:{writeText:async value=>{clipboard=value;}}},
    fetch:async (url,options={})=>{
      const method=options.method||'GET';const call={url,method,body:options.body,headers:options.headers};requests.push(call);
      let data=route?await route(call):undefined;
      if(data===undefined){
        const parsed=new globalThis.URL(url,'https://fixture.test'),pathname=parsed.pathname;
        if(pathname.includes('/assets/locale_'))data=texts;
        else if(pathname==='/v1/admin/codes'&&method==='POST'){
          data=batch;records=batch.codes.map(code=>({id:code.id,batch_id:code.batch_id,days:code.days,created_at:'2030-01-01T00:00:00Z',used_at:null,revoked_at:null}));
        }else if(pathname==='/v1/admin/codes')data={codes:records,next_cursor:''};
        else if(pathname==='/v1/admin/status')data={gateways_online:0,mobiles_online:0,push_queued:0};
        else{
          const key={'/v1/admin/users':'users','/v1/admin/audit':'audit','/v1/admin/redemptions':'redemptions','/v1/admin/registration-campaigns':'campaigns'}[pathname]
            ||(pathname.startsWith('/v1/admin/users/')&&['devices','bindings','sessions'].find(key=>pathname.endsWith('/'+key)));
          assert.ok(key&&method==='GET','Unexpected UI request: '+method+' '+url);data={[key]:[],next_cursor:''};
        }
      }
      return {ok:true,json:async()=>data};
    }};
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(path.join(web,'campaigns.js'),'utf8'),context);
  await vm.runInContext(source,context);
  return {nodes,texts,exports,requests,confirmations,
    async generate(codes){batch={export_once:true,codes};await form.events.submit({preventDefault(){}});},
    pagehide:()=>listeners.get('pagehide')(),get clipboard(){return clipboard;}};
}
module.exports={fixture,descendants,adminID};
