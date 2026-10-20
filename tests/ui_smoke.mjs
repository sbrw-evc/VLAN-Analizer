import assert from 'node:assert/strict';
import http from 'node:http';
import fs from 'node:fs/promises';
import { chromium } from 'playwright';

const origin = process.env.APP_URL || 'http://127.0.0.1:8000';
const verify = process.env.MODE === 'verify';
const mock = http.createServer((req, res) => {
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify([{name:'Gatus portal',url:'http://127.0.0.1:8080/gatus'}]));
});
await new Promise(resolve => mock.listen(8766, '127.0.0.1', resolve));
const browser = await chromium.launch();
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
try {
  await waitStatus(s => verify ? s.ready : s.required && s.managed_available);
  await page.goto(origin);
  if (!verify) {
    await page.locator('#setup').waitFor({state:'visible'});
    assert.equal(await page.locator('[name=bao_mode]').inputValue(),'managed');
    await page.locator('#setup-next').click();
    await page.locator('[data-step="1"]').waitFor({state:'visible'});
    await page.locator('#setup-next').click();
    await page.locator('[data-step="2"]').waitFor({state:'visible'});
    await page.locator('#setup-next').click();
    await page.locator('[data-step="3"]').waitFor({state:'visible'});
    await page.locator('[name=admin_password]').fill('Integration-Admin-Password-2026');
    await page.locator('[name=admin_confirm]').fill('Integration-Admin-Password-2026');
    await page.locator('#setup-next').click();
    await page.locator('[data-step="4"]').waitFor({state:'visible'});
    await page.locator('#setup-next').click();
  }
  await page.locator('#login').waitFor({state:'visible'});
  await page.locator('#login-form [name=username]').fill('admin');
  await page.locator('#login-form [name=password]').fill('Integration-Admin-Password-2026');
  await page.locator('#login-form button').click();
  await page.locator('#shell').waitFor({state:'visible'});
  await page.locator('[data-tab=settings]').click();
  await page.waitForFunction(()=>document.querySelector('#monitoring-form [name=interval]').value !== '');
  if (!verify) {
    await page.locator('#monitoring-form [name=concurrency]').fill('2');
    await page.locator('#monitoring-form [name=browser]').uncheck();
    await page.locator('#monitoring-form button').click();
    await page.locator('#monitoring-form [data-result]').filter({hasText:'saved'}).waitFor();
    for (const [field,value] of Object.entries({enabled:true,url:'http://127.0.0.1:8766',token:'ci-gatus-token'})) {
      const el=page.locator(`#gatus-form [name=${field}]`); if(typeof value==='boolean')await el.setChecked(value);else await el.fill(value);
    }
    await page.locator('#gatus-form button.primary').click();
    await page.locator('#gatus-form [data-result]').filter({hasText:'Saved'}).waitFor();
    const kuma={enabled:true,host:'127.0.0.1',port:'3307',database:'kuma',user:'vlan_reader',password:'integration-kuma-password',tls:false,instance:'ci'};
    for(const [field,value] of Object.entries(kuma)) {
      const el=page.locator(`#kuma-form [name=${field}]`);if(typeof value==='boolean')await el.setChecked(value);else await el.fill(value);
    }
    await page.locator('#kuma-form button.primary').click();
    await page.locator('#kuma-form [data-result]').filter({hasText:'Saved'}).waitFor();
    await api('/api/gatus/sync','POST');
    await api('/api/kuma/sync','POST');
    await page.locator('[data-tab=overview]').click();
    await page.locator('#add').click();
    await page.locator('#site-form [name=name]').fill('Manual portal');
    await page.locator('#site-form [name=url]').fill('http://127.0.0.1:8080/manual');
    await page.locator('#site-form [name=enabled]').uncheck();
    await page.locator('#site-form button.primary').click();
    await page.locator('#site-dialog').waitFor({state:'hidden'});
    await page.locator('#matrix').filter({hasText:'Manual portal'}).waitFor();
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
  await page.locator('[data-tab=networks]').click();
  await page.locator('#network-cards article').first().waitFor();
  assert.deepEqual(errors,[]);
  console.log(verify?'Restart persistence and automatic OpenBao unseal passed.':'Browser wizard, login, monitoring, manual/Gatus/Kuma imports and inventory passed.');
} catch(error) {
  await fs.mkdir('test-results',{recursive:true});
  await page.screenshot({path:'test-results/ui-failure.png',fullPage:true});
  console.error('Setup message:',await page.locator('#setup-message').textContent());
  console.error('Login error:',await page.locator('#login-error').textContent());
  throw error;
} finally {await browser.close();await new Promise(resolve=>mock.close(resolve));}
