'use strict';
window.Setup = (() => {
  const el = id => document.getElementById(id);
  const form = el('setup-form');
  const fields = form.elements;
  let step = 0, busy = false;
  async function request(path, body, method = 'POST', setup = false) {
    const headers = {'Content-Type': 'application/json'};

    const response = await fetch(path, {method, credentials: 'same-origin', headers, ...(body === undefined ? {} : {body:JSON.stringify(body)})});
    const result = await response.json();
    if (!response.ok) throw new Error(typeof result.detail === 'string' ? result.detail : 'Check the connection settings and required fields.');
    return result;
  }
  function bao() {return {url:fields.bao_url.value,mount:fields.bao_mount.value,mode:fields.bao_mode.value,
    token:fields.bao_token.value,role_id:fields.bao_role_id.value,secret_id:fields.bao_secret_id.value};}
  function clickhouse() {return {managed:fields.ch_managed.checked,url:fields.ch_url.value,user:fields.ch_user.value,database:fields.ch_database.value,password:fields.ch_password.value};}
  function showStep() {
    form.querySelectorAll('[data-step]').forEach(p => p.hidden=Number(p.dataset.step)!==step);
    document.querySelectorAll('.setup-steps li').forEach((li,i)=>li.classList.toggle('active',i===step));
    el('setup-back').hidden=step===0;el('setup-counter').textContent=`Step ${step+1} of 5`;
    el('setup-next').textContent=step===4?'Complete installation':'Continue →';
    el('setup-message').textContent='';el('setup-redirect').textContent=fields.public_url.value.replace(/\/$/,'')+'/auth/entra/callback';
  }
  function validStep() {
    for (const input of form.querySelector(`[data-step="${step}"]`).querySelectorAll('input,select')) {
      if (!input.checkValidity()) {input.reportValidity();return false;}
    }
    if (step===1 && fields.bao_mode.value!=='managed' && (fields.bao_mode.value==='token' ? !fields.bao_token.value : !fields.bao_role_id.value || !fields.bao_secret_id.value)) throw new Error('Enter the OpenBao credentials.');
    if (step===3 && fields.admin_password.value!==fields.admin_confirm.value) throw new Error('Passwords do not match.');
    if (step===4 && fields.entra_enabled.checked && (!fields.entra_tenant.value || !fields.entra_client.value || !fields.entra_secret.value)) throw new Error('Enter all Entra ID connection fields or skip this step.');
    return true;
  }
  async function action(callback) {
    if (busy) return;busy=true;
    form.querySelectorAll('button').forEach(b=>b.disabled=true);
    try {await callback();} catch(e) {el('setup-message').textContent=e.message;}
    finally {busy=false;form.querySelectorAll('button').forEach(b=>b.disabled=false);}
  }
  form.addEventListener('submit',e=>e.preventDefault());
  function showManaged(){ el('bao-external-fields').hidden=fields.bao_mode.value==='managed'; fields.ch_password.required=!fields.ch_managed.checked; fields.ch_url.disabled=fields.ch_managed.checked; fields.ch_user.disabled=fields.ch_managed.checked; fields.ch_database.disabled=fields.ch_managed.checked; fields.ch_password.disabled=fields.ch_managed.checked; el('ch-external-fields').hidden=fields.ch_managed.checked; }
  fields.ch_managed.addEventListener('change',showManaged);
  fields.bao_mode.addEventListener('change',()=>{showManaged();el('bao-token-fields').hidden=fields.bao_mode.value!=='token';el('bao-approle-fields').hidden=fields.bao_mode.value!=='approle';});
  fields.entra_enabled.addEventListener('change',()=>el('setup-entra-fields').hidden=!fields.entra_enabled.checked);
  el('setup-back').addEventListener('click',()=>{if(!busy){step=Math.max(0,step-1);showStep();}});
  el('test-bao').addEventListener('click',()=>action(async()=>{const result=await request('/api/setup/test/openbao',bao(),'POST',true);el('setup-message').textContent=result.message;}));
  el('test-ch').addEventListener('click',()=>action(async()=>{const result=await request('/api/setup/test/clickhouse',clickhouse(),'POST',true);el('setup-message').textContent=result.message;}));
  el('setup-next').addEventListener('click',()=>action(async()=>{
    if(!validStep())return;
    if(step===1)await request('/api/setup/test/openbao',bao(),'POST',true);
    if(step===2)await request('/api/setup/test/clickhouse',clickhouse(),'POST',true);
    if(step<4){step++;showStep();return;}
    el('setup-message').textContent='Verifying services and saving your installation…';
    await request('/api/setup/complete',{public_url:fields.public_url.value,bao:bao(),clickhouse:clickhouse(),
      admin_username:fields.admin_username.value,admin_password:fields.admin_password.value,
      entra:{enabled:fields.entra_enabled.checked,tenant:fields.entra_tenant.value,client_id:fields.entra_client.value,client_secret:fields.entra_secret.value}},'POST',true);
    form.reset();location.reload();
  }));
  async function initialize(start) {
    try {
      const state=await request('/api/setup/status',undefined,'GET');
      el('setup').hidden=!state.required;el('unavailable').hidden=state.required||state.ready;
      if(state.required){fields.public_url.value=location.origin;fields.bao_mode.value=state.managed_available?'managed':'token';fields.ch_managed.checked=!!state.managed_available;fields.bao_mode.dispatchEvent(new Event('change'));showManaged();showStep();return;}
      if(!state.ready){el('unavailable-message').textContent=state.error||'Waiting for configured services to become available.';return;}
      el('login').hidden=false;el('entra-login').hidden=!state.entra_enabled;
      await start();
    } catch(e) {el('unavailable').hidden=false;el('unavailable-message').textContent=e.message;}
  }
  el('retry-start').addEventListener('click',()=>location.reload());
  const secretKeys={gatus:'token',kuma:'password',entra:'client_secret'};
  function fillConnections(values) {
    for(const source of Object.keys(secretKeys)) {
      const f=el(`${source}-form`), value=values[source];
      if(!value)continue;
      for(const [key,item] of Object.entries(value)) {
        const input=f.elements.namedItem(key);if(!input)continue;
        if(input.type==='checkbox')input.checked=!!item;else input.value=item??'';
      }
      f.elements.namedItem(secretKeys[source]).value='';
      f.querySelector('[data-secret-status]').textContent=value[secretKeys[source]+'_set']?'Credential saved in OpenBao. Leave blank to keep it.':'No credential saved.';
    }
    el('settings-redirect').textContent=values.entra?.redirect_uri||'';el('entra-login').hidden=!values.entra?.enabled;
  }
  async function loadConnections(){fillConnections(await request('/api/settings/connections',undefined,'GET')); await loadMonitoring();}
  async function loadMonitoring(){
    const [config,dashboard]=await Promise.all([request('/api/settings/monitoring',undefined,'GET'),request('/api/dashboard',undefined,'GET')]);
    const f=el('monitoring-form'); const select=f.elements.interfaces; select.replaceChildren();
    const names=new Set([...(dashboard.interfaces||[]).map(i=>i.interface),...(config.interfaces||[])]);
    for(const name of names){const option=document.createElement('option');option.value=name;option.textContent=name;option.selected=(config.interfaces||[]).includes(name);select.append(option);}
    for(const name of ['interval','concurrency','timeout','stale_after'])f.elements.namedItem(name).value=config[name];
    f.elements.browser.checked=config.browser;
    f.elements.dns_servers.value=Object.entries(config.dns_servers||{}).map(([n,servers])=>`${n}=${servers.join(',')}`).join('\n');
  }
  el('monitoring-form').addEventListener('submit',async event=>{event.preventDefault();const f=event.currentTarget,status=f.querySelector('[data-result]');try{
    const dns={};for(const line of f.elements.dns_servers.value.split('\n')){if(!line.trim())continue;const at=line.indexOf('=');if(at<1)throw new Error('Use interface=DNS1,DNS2 for every DNS line.');dns[line.slice(0,at).trim()]=line.slice(at+1).split(',').map(v=>v.trim()).filter(Boolean);}
    const config={interfaces:[...f.elements.interfaces.selectedOptions].map(o=>o.value),dns_servers:dns,browser:f.elements.browser.checked};
    for(const name of ['interval','concurrency','timeout','stale_after'])config[name]=Number(f.elements.namedItem(name).value);
    await request('/api/settings/monitoring',config,'PUT');status.textContent='Monitoring settings saved. Changes apply to the next cycle.';
  }catch(e){status.textContent=e.message;}});
  function values(source) {
    const f=el(`${source}-form`), body={};
    for(const input of f.elements) {
      if(!input.name)continue;
      body[input.name]=input.type==='checkbox'?input.checked:input.type==='number'?Number(input.value):input.value;
    }
    if(!body[secretKeys[source]])delete body[secretKeys[source]];
    return body;
  }
  async function saveOrTest(source,test) {
    const f=el(`${source}-form`), status=f.querySelector('[data-result]');
    f.querySelectorAll('button').forEach(b=>b.disabled=true);status.textContent=test?'Testing…':'Saving…';
    try {
      const result=await request(`/api/settings/connections/${source}${test?'/test':''}`,values(source),test?'POST':'PUT');
      if(!test){fillConnections(result);status.textContent='Saved. Settings apply immediately.';}
      else status.textContent=result.message||`Connection verified. ${result.sites} websites found. ${(result.warnings||[]).join(' · ')}`;
    }catch(e){status.textContent=e.message;}finally{f.querySelectorAll('button').forEach(b=>b.disabled=false);}
  }
  for(const source of Object.keys(secretKeys)) {
    el(`${source}-form`).addEventListener('submit',e=>{e.preventDefault();saveOrTest(source,false);});
    document.querySelector(`[data-test="${source}"]`).addEventListener('click',()=>saveOrTest(source,true));
  }
  return {initialize,loadConnections};
})();
