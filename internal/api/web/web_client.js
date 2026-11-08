'use strict';
let csrf='',busy=false;
const $=id=>document.getElementById(id);
const messages={invalid_credentials:'账号或密码不正确',rate_limited:'尝试过于频繁，请稍后再试',entitlement_required:'云服务已到期，请在桌面端续期',authentication_required:'登录已过期，请重新登录',csrf_required:'页面已过期，请刷新后重试',credentials_change_required:'请先在管理后台修改初始密码',gateway_web_offline:'电脑离线或尚未支持 Web 连接'};
async function api(path,value){const options={credentials:'same-origin',cache:'no-store',headers:{}};if(value!==undefined){options.method='POST';options.headers={'Content-Type':'application/json','X-CSRF-Token':csrf};options.body=JSON.stringify(value);}const response=await fetch('/_bridge/'+path,options);const data=await response.json();if(!response.ok)throw Error(messages[data.code]||'暂时无法连接，请稍后重试');return data;}
async function refresh() {
  const data = await api('gateways');
  $('gateways').replaceChildren();
  $('entitlement').textContent = data.entitlement.active ? '' : '云服务尚未激活或已到期，请在桌面端兑换服务天数。';
  for (const row of data.gateways) {
    const item = $('gateway-template').content.cloneNode(true);
    const link = item.querySelector('.gateway-card');
    link.querySelector('.gateway-name').textContent = row.name;
    link.querySelector('.gateway-state').textContent = row.online
      ? data.entitlement.active ? '在线 · 可连接' : '在线 · 云服务待激活'
      : row.desktop_online ? '网页连接未就绪，请检查桌面端网页访问设置或版本' : '离线 · 请在电脑上启动网关';
    link.dataset.online = String(row.online);
    if (row.online && data.entitlement.active) link.href = '/d/' + encodeURIComponent(row.id) + '/';
    else link.setAttribute('aria-disabled', 'true');
    $('gateways').append(item);
  }
  $('computer-count').textContent = String(data.gateways.length);
  $('empty').hidden = data.gateways.length !== 0;
  $('status').textContent = '';
  delete $('status').dataset.error;
}
async function start() {
  const session = await api('session');
  csrf = session.csrf;
  document.body.dataset.authenticated = String(session.authenticated);
  $('login').hidden = session.authenticated;
  $('computers').hidden = !session.authenticated;
  $('page-title').textContent = session.authenticated ? '选择一台电脑' : '连接你的电脑';
  $('page-description').textContent = session.authenticated
    ? '继续聊天、查看进度，处理待确认的操作。'
    : '使用与桌面客户端相同的云账号登录。';
  $('status').textContent = '';
  delete $('status').dataset.error;
  if (session.authenticated) {
    $('identity').textContent = session.login;
    await refresh();
  }
}
function report(error){$('status').textContent=error.message;$('status').dataset.error='true';}
$('login').onsubmit=async event=>{event.preventDefault();if(busy)return;busy=true;const button=event.target.querySelector('button');button.disabled=true;try{await api('login',{login:$('account').value,password:$('password').value});$('password').value='';await start();}catch(error){report(error);}finally{busy=false;button.disabled=false;}};
$('refresh').onclick=async()=>{const button=$('refresh');button.disabled=true;try{await refresh();}catch(error){report(error);}finally{button.disabled=false;}};
$('logout').onclick=async()=>{try{await api('logout',{});await start();}catch(error){report(error);}};
start().catch(report);
