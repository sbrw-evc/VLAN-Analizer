import assert from 'node:assert/strict';
import http from 'node:http';
import fs from 'node:fs/promises';
import { chromium } from 'playwright';

const origin = process.env.APP_URL || 'http://127.0.0.1:8000';
const verify = process.env.MODE === 'verify';
const password = 'Integration-Admin-Password-2026';
const mock = http.createServer((req, res) => {
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify([{name:'Gatus portal',url:'http://127.0.0.1:8080/gatus'}]));
});
await new Promise(resolve => mock.listen(8766, '127.0.0.1', resolve));
const browser = await chromium.launch(process.env.CHROMIUM_PATH ? {executablePath: process.env.CHROMIUM_PATH} : {});
const page = await browser.newPage();
const errors = [];
page.on('pageerror', error => errors.push(error.message));
page.on('dialog', async dialog => { errors.push(dialog.message()); await dialog.dismiss(); });
page.setDefaultTimeout(45000);
async function api(path, method = 'GET', body) {
  return page.evaluate(async ({path,method,body}) => {
    const r = await fetch(path,{method,headers:{'Content-Type':'application/json'},...(body===undefined?{}:{body:JSON.stringify(body)})});
    const value=await r.json(); if(!r.ok)throw new Error(JSON.stringify(value)); return value;
  },{path,method,body});
}
async function waitStatus(predicate) {
  for (let i=0;i<90;i++) {
    try {const r=await fetch(origin+'/api/setup/status');if(predicate(await r.json()))return;} catch {}
    await new Promise(resolve=>setTimeout(resolve,1000));
  }
  throw new Error('Application did not reach expected setup status');
}
async function fill(form, values) {
  for (const [field,value] of Object.entries(values)) {
    const el=page.locator(`[data-testid=${form}] [name=${field}]`);
    if(typeof value==='boolean')await el.setChecked(value);else await el.fill(String(value));
  }
}
try {
  await waitStatus(s => verify ? s.ready : s.required && s.managed_available);
  await page.goto(origin);
  if (!verify) {
    await page.getByTestId('setup').waitFor();
    await page.getByTestId('setup-next').click();
    await page.locator('[data-step="1"]').waitFor();
    assert.equal(await page.locator('[data-mode=managed]').getAttribute('aria-checked'),'true');
    await page.getByTestId('setup-next').click();
    await page.locator('[data-step="2"]').waitFor();
    await page.getByTestId('setup-next').click();
    await page.locator('[data-step="3"]').waitFor();
    await page.locator('[name=admin_password]').fill(password);
    await page.locator('[name=admin_confirm]').fill(password);
    await page.getByTestId('setup-next').click();
    await page.locator('[data-step="4"]').waitFor();
    await page.getByTestId('setup-next').click();
  }
  await page.getByTestId('signin').waitFor();
  await fill('signin-form',{username:'admin',password});
  await page.locator('[data-testid=signin-form] button[type=submit]').click();
  await page.locator('.app-shell').waitFor();
  await page.goto(origin+'/settings/monitoring');
  await page.waitForFunction(()=>document.querySelector('[data-testid=monitoring-form] [name=interval]')?.value);
  if (!verify) {
    await fill('monitoring-form',{concurrency:2,browser:false});
    await page.locator('[data-testid=monitoring-form] button[type=submit]').click();
    await page.getByText('Monitoring settings saved').waitFor();
    await page.goto(origin+'/settings/connections');
    await fill('gatus-form',{enabled:true,url:'http://127.0.0.1:8766',token:'ci-gatus-token'});
    await page.locator('[data-testid=gatus-form] button[type=submit]').click();
    await page.getByText('Gatus saved').waitFor();
    await fill('kuma-form',{enabled:true,host:'127.0.0.1',port:3307,database:'kuma',user:'vlan_reader',password:'integration-kuma-password',tls:false,instance:'ci'});
    await page.locator('[data-testid=kuma-form] button[type=submit]').click();
    await page.getByText('Uptime Kuma saved').waitFor();
    await api('/api/gatus/sync','POST');
    await api('/api/kuma/sync','POST');
    await page.goto(origin);
    await page.getByTestId('add-site').click();
    await page.locator('#site-form [name=name]').fill('Manual portal');
    await page.locator('#site-form [name=url]').fill('http://127.0.0.1:8080/manual');
    await page.locator('#site-form [name=enabled]').setChecked(false);
    await page.locator('button[form=site-form][type=submit]').click();
    await page.getByTestId('site-dialog').waitFor({state:'detached'});
    await page.getByTestId('matrix').filter({hasText:'Manual portal'}).waitFor();
  }
  const scan=await api('/api/settings/monitoring');
  assert.equal(scan.concurrency,2);assert.equal(scan.browser,false);
  const connections=await api('/api/settings/connections');
  assert.equal(connections.kuma.password_set,true);assert.equal(connections.gatus.token_set,true);
  assert.equal(connections.kuma.password,undefined);assert.equal(connections.gatus.token,undefined);
  const dashboard=await api('/api/dashboard');
  assert(dashboard.sites.some(s=>s.source==='manual'));
  assert(dashboard.sites.some(s=>s.source==='gatus'));
  assert(dashboard.sites.some(s=>s.source==='kuma' && s.source_enabled===0));
  const closed=await page.evaluate(async()=>{const r=await fetch('/api/setup/complete',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});return r.status;});
  assert.equal(closed,409);
  await page.goto(origin+'/networks');
  await page.locator('[data-testid=interfaces] .iface-card').first().waitFor();
  await page.goto(origin+'/vlans');
  await page.getByTestId('host-environment').waitFor();
  const vlans=await api('/api/network/vlans');
  assert.equal(vlans.available,true);
  assert(vlans.environment.backend,'the host network agent detected no backend');
  assert(vlans.trunks.length>0,'no trunk-capable interfaces reported');
  assert.deepEqual(errors,[]);
  console.log(verify?'Restart persistence and automatic OpenBao unseal passed.':`Browser wizard, login, monitoring, manual/Gatus/Kuma imports, inventory and the host network agent (${vlans.environment.distro.name}, ${vlans.environment.backend}) passed.`);
} catch(error) {
  await fs.mkdir('test-results',{recursive:true});
  await page.screenshot({path:'test-results/ui-failure.png',fullPage:true});
  console.error('Page URL:',page.url());
  console.error('Visible errors:',await page.locator('.notice-error').allTextContents().catch(()=>[]));
  throw error;
} finally {await browser.close();await new Promise(resolve=>mock.close(resolve));}
