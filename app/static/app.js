'use strict';
const $ = id => document.getElementById(id);
let data, user;
const enabled = site => !!site.enabled && site.source_enabled !== 0;
const esc = (tag, value, className) => { const el = document.createElement(tag); el.textContent = value; if (className) el.className = className; return el; };
async function api(path, options = {}) {
  const r = await fetch(path, {credentials: 'same-origin', ...options, headers: {'Content-Type': 'application/json', ...options.headers}});
  if (!r.ok) { let detail; try {detail = (await r.json()).detail;} catch {detail = r.statusText;}
    if (r.status === 401 && path !== '/auth/local') showLogin();
    throw new Error(typeof detail === 'string' ? detail : JSON.stringify(detail)); }
  return r.json();
}
function notice(message) { $('notice').textContent = message; $('notice').hidden = !message; }
function showLogin() { $('shell').hidden = true; $('login').hidden = false; user = null; }
function time(value) { return value ? new Date(value).toLocaleString() : 'No measurements yet'; }
async function start() {
  try {user = await api('/api/me'); $('identity').textContent = `${user.name || user.sub} · ${user.role}`;
    document.querySelectorAll('.admin').forEach(e => e.hidden = user.role !== 'admin');
    $('login').hidden = true; $('shell').hidden = false; await refresh();
    if (user.role === 'admin') await window.Setup.loadConnections();
  } catch (e) { if (user) notice(e.message); }
}
async function refresh() {
  try {data = await api('/api/dashboard'); render();}
  catch (e) {notice(`Unable to refresh: ${e.message}`);}
}
function render() {
  const interfaces = data.interfaces.filter(i => i.probeable);
  const results = data.results;
  $('site-count').textContent = data.sites.filter(enabled).length;
  $('network-count').textContent = interfaces.length;
  $('healthy-count').textContent = results.filter(r => r.status === 'up').length;
  $('issue-count').textContent = results.filter(r => ['down', 'degraded', 'error'].includes(r.status)).length;
  $('cycle').textContent = data.worker.running ? '● Checks running' : `Last cycle: ${time(data.worker.last_cycle)}`;
  $('updated').textContent = `Inventory: ${time(data.inventory_at)}`;
  const warnings = [...data.worker.gatus_warnings, ...(data.worker.kuma_warnings || [])];
  if (data.worker.error) warnings.push(`Monitoring error: ${data.worker.error}. Inspect service logs.`);
  if (!interfaces.length) warnings.push('No probeable IPv4 interfaces found. Check host networking and INTERFACES.');
  const unknown = data.sites.filter(enabled).length * interfaces.length - results.filter(r => r.status !== 'unknown').length;
  if (unknown > 0) warnings.push(`${unknown} paths have no fresh measurement.`);
  notice(warnings.join(' · '));
  renderMatrix(interfaces);
  const cards = $('network-cards'); cards.replaceChildren();
  for (const i of data.interfaces) {
    const card = esc('article', '', 'panel raw'); const title = esc('div', '', 'network-title');
    title.append(esc('h2', i.interface), esc('span', i.vlan_id == null ? 'Untagged / unknown' : `VLAN ${i.vlan_id}`, 'tag'));
    card.append(title); const dl = esc('dl', '', 'network-meta');
    for (const [key, value] of Object.entries({State:i.state, Type:i.kind, Parent:i.parent || '—', Master:i.master || '—', IPv4:i.addresses.join(', ') || '—', IPv6:i.ipv6.join(', ') || '—', MAC:i.mac || '—', MTU:i.mtu, Probing:i.probeable ? 'Enabled' : 'Not eligible', Routes:JSON.stringify(i.routes)})) {
      dl.append(esc('dt', key), esc('dd', String(value)));
    } card.append(dl); cards.append(card);
  }
  $('bridge').textContent = JSON.stringify(data.bridge_vlans, null, 2);
}
function renderMatrix(interfaces = data.interfaces.filter(i => i.probeable)) {
  const table = $('matrix'); table.replaceChildren();
  const head = document.createElement('thead'), tr = document.createElement('tr');
  tr.append(esc('th', 'WEBSITE / DESTINATION'));
  interfaces.forEach(i => {const th = esc('th', i.interface); th.append(esc('div', i.vlan_id == null ? 'Untagged / unknown' : `VLAN ${i.vlan_id}`, 'small muted')); tr.append(th);});
  head.append(tr); table.append(head); const body = document.createElement('tbody');
  const filter = $('filter').value.toLowerCase();
  const sites = data.sites.filter(s => `${s.name} ${s.url}`.toLowerCase().includes(filter));
  for (const s of sites) {
    const row = document.createElement('tr'), name = document.createElement('td');
    const title = esc('span', s.name + (enabled(s) ? '' : s.source_enabled === 0 ? ' · Paused in source' : ' · Paused'), 'site-name');
    if (user.role === 'admin') { const edit = esc('button', 'Edit', 'quiet edit'); edit.addEventListener('click', () => siteDialog(s)); title.append(edit); }
    name.append(title, esc('span', `${s.source} · ${s.url}`, 'site-url')); row.append(name);
    for (const i of interfaces) {
      const result = data.results.find(r => r.site_id === s.id && r.interface === i.interface);
      const cell = document.createElement('td'), state = enabled(s) ? result?.status || 'unknown' : 'unknown';
      const label = {up:'● Healthy', down:'● Down', degraded:'● Degraded', error:'● Error', unknown:enabled(s) ? '○ Unknown' : 'Ⅱ Paused'}[state] || state;
      const button = esc('button', label, `pill ${state}`);
      button.addEventListener('click', () => detail(s, i, result)); cell.append(button);
      cell.append(esc('span', result?.http?.total_ms != null ? `${result.http.total_ms.toFixed(0)} ms · HTTP ${result.http.code}${state === 'unknown' ? ' · stale' : ''}` : 'Awaiting measurement', 'latency'));
      row.append(cell);
    } body.append(row);
  } table.append(body); $('empty').hidden = sites.length > 0;
}
function siteDialog(site) {
  const form = $('site-form'); form.reset(); form.elements.id.value = site?.id || '';
  form.elements.name.value = site?.name || ''; form.elements.url.value = site?.url || '';
  form.elements.url.readOnly = ['gatus', 'kuma'].includes(site?.source); form.elements.enabled.checked = site ? !!site.enabled : true;
  $('delete-site').hidden = !site || ['gatus', 'kuma'].includes(site.source); $('site-title').textContent = site ? 'Edit website' : 'Add website';
  $('site-dialog').showModal();
}
async function detail(site, iface, result) {
  const requestKey = `${site.id}/${iface.interface}`; $('detail-dialog').dataset.key = requestKey;
  $('detail-title').textContent = `${site.name} / ${iface.interface}`;
  $('detail-json').textContent = JSON.stringify(result || {status:'unknown', reason:'No fresh measurement'}, null, 2);
  $('history-summary').textContent = 'Loading history…'; chart([]); $('detail-dialog').showModal();
  try {const history = await api(`/api/history?${new URLSearchParams({site_id:site.id, interface:iface.interface, hours:'24'})}`);
    if ($('detail-dialog').dataset.key !== requestKey) return;
    chart(history); $('history-summary').textContent = `${history.length} measurements · ${history.filter(r => r.status === 'up').length} healthy · Times include proxy overhead for HTTP/browser. TCP/TLS are direct.`;
  } catch (e) {$('history-summary').textContent = e.message;}
}
function chart(history) {
  const canvas = $('history-chart'), ctx = canvas.getContext('2d'); ctx.clearRect(0,0,900,160);
  const points = history.filter(r => r.http?.total_ms != null).reverse();
  ctx.font = '11px system-ui'; ctx.fillStyle = '#8d9db2';
  if (!points.length) {ctx.fillText('No HTTP timing samples', 24, 80); return;}
  const max = Math.max(1, ...points.map(r => r.http.total_ms));
  const from = new Date(points[0].at).getTime(), duration = Math.max(1,new Date(points.at(-1).at).getTime() - from);
  ctx.fillText(`${max.toFixed(0)} ms`, 12, 17); ctx.strokeStyle = '#223044';
  [30,80,130].forEach(y => {ctx.beginPath(); ctx.moveTo(55,y);ctx.lineTo(880,y);ctx.stroke();});
  ctx.strokeStyle = '#668cfa'; ctx.lineWidth = 2; ctx.beginPath();
  points.forEach((p,i) => {const x = 55 + (new Date(p.at).getTime()-from)/duration*820, y = 135-p.http.total_ms/max*110; if(i)ctx.lineTo(x,y);else ctx.moveTo(x,y);}); ctx.stroke();
  ctx.fillText(new Date(points[0].at).toLocaleTimeString(),55,155);ctx.fillText(new Date(points.at(-1).at).toLocaleTimeString(),790,155);
}
$('login-form').addEventListener('submit', async e => {e.preventDefault(); const f = e.currentTarget;
  try {await api('/auth/local',{method:'POST',body:JSON.stringify({username:f.username.value,password:f.password.value})});f.password.value='';$('login-error').textContent='';await start();}
  catch(err){$('login-error').textContent=err.message;}
});
$('logout').addEventListener('click', async () => {try {await api('/auth/logout',{method:'POST'});showLogin();}catch(e){notice(e.message);}});
$('filter').addEventListener('input', () => data && renderMatrix());
$('add').addEventListener('click', () => siteDialog());
$('site-form').addEventListener('submit', async e => {e.preventDefault();const f=e.currentTarget,id=f.elements.id.value;
  try{await api(id ? `/api/sites/${encodeURIComponent(id)}` : '/api/sites',{method:id?'PUT':'POST',body:JSON.stringify({name:f.elements.name.value,url:f.elements.url.value,enabled:f.elements.enabled.checked})});$('site-dialog').close();await refresh();}catch(err){alert(err.message);}
});
$('delete-site').addEventListener('click',async()=>{if(!confirm('Delete this website? Historical measurements are retained.'))return;try{await api(`/api/sites/${encodeURIComponent($('site-form').elements.id.value)}`,{method:'DELETE'});$('site-dialog').close();await refresh();}catch(e){alert(e.message);}});
$('user-form').addEventListener('submit',async e=>{e.preventDefault();const f=e.currentTarget;try{await api('/api/users',{method:'PUT',body:JSON.stringify({username:f.elements.username.value,password:f.elements.password.value,role:f.elements.role.value,disabled:f.elements.disabled.checked})});f.reset();notice('User saved. Existing local sessions for this account were revoked.');}catch(err){notice(err.message);}});
for(const [id,path] of [['run','/api/checks/run'],['sync','/api/gatus/sync'],['sync-kuma','/api/kuma/sync']]) $(id).addEventListener('click',async()=>{const b=$(id);b.disabled=true;try{const result=await api(path,{method:'POST'});await refresh();notice(result.queued?'Check cycle queued.':`Imported ${result.imported} sites. ${result.warnings.join(' · ')}`);}catch(e){notice(e.message);}finally{b.disabled=false;}});
document.querySelectorAll('[data-close]').forEach(b=>b.addEventListener('click',()=>$(b.dataset.close).close()));
document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>{document.querySelectorAll('.tab').forEach(t=>t.hidden=t.id!==b.dataset.tab);document.querySelectorAll('.nav').forEach(n=>n.classList.toggle('active',n===b));$('breadcrumb').textContent=b.dataset.tab==='overview'?'Overview':b.dataset.tab==='networks'?'Networks':'Administration';}));
window.Setup.initialize(start);setInterval(()=>{if(user && !document.hidden)refresh();},15000);
