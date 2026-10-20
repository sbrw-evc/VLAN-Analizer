// End-to-end check of VLAN management through the browser and the real host network agent.
// CI points netagent (NETAGENT_HOST_PID) at a process in a disposable network namespace that holds a bridge named
// trunk0, so real 802.1Q devices are created in the kernel without touching the runner's own network.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import { chromium } from 'playwright';

const origin = process.env.APP_URL || 'http://127.0.0.1:8000';
const trunk = process.env.TRUNK || 'trunk0';
const browser = await chromium.launch(process.env.CHROMIUM_PATH ? {executablePath: process.env.CHROMIUM_PATH} : {});
const page = await browser.newPage();
const errors = [];
page.on('pageerror', error => errors.push(error.message));
page.setDefaultTimeout(45000);
const api = (path) => page.evaluate(async (p) => { const r = await fetch(p); return r.json(); }, path);
const vlans = () => api('/api/network/vlans');
const managed = async (name) => (await vlans()).interfaces.find(i => i.name === name);
async function until(check, what) {
  for (let i = 0; i < 40; i++) { if (await check()) return; await page.waitForTimeout(500); }
  throw new Error(`timed out waiting for ${what}`);
}
async function menu(name, item) {
  await page.getByRole('button', {name: `Actions for ${name}`}).click();
  await page.getByRole('menuitem', {name: item}).click();
}

try {
  await page.goto(origin + '/signin');
  await page.locator('[data-testid=signin-form] [name=username]').fill('admin');
  await page.locator('[data-testid=signin-form] [name=password]').fill('Integration-Admin-Password-2026');
  await page.locator('[data-testid=signin-form] button[type=submit]').click();
  await page.locator('.app-shell').waitFor();
  await page.goto(origin + '/vlans');
  await page.getByTestId('host-environment').waitFor();
  const before = await vlans();
  assert.equal(before.environment.error, undefined, before.environment.error);
  assert.equal(before.environment.backend, 'runtime', 'the test namespace must hide the runner network services');
  assert(before.trunks.some(t => t.name === trunk), `${trunk} is not offered as a trunk`);

  // Static VLAN through the dialog.
  await page.getByTestId('create-vlan').click();
  const dialog = page.getByTestId('vlan-dialog');
  await dialog.locator('[name=parent]').selectOption(trunk);
  await dialog.locator('[name=vlans]').fill('20');
  await dialog.getByRole('radio', {name: 'Static'}).click();
  await dialog.locator('[name=address]').fill('10.20.0.2/24');
  await dialog.locator('[name=gateway]').fill('10.20.0.1');
  await dialog.locator('[name=mtu]').fill('1400');
  await dialog.locator('[name=dns]').fill('10.20.0.53');
  await dialog.locator('[name=monitor]').setChecked(false);
  await page.locator('button[form=vlan-form][type=submit]').click();
  await dialog.waitFor({state: 'detached'});
  await page.getByTestId('managed-vlans').filter({hasText: `${trunk}.20`}).waitFor();
  let item = await managed(`${trunk}.20`);
  assert(item.present && item.live.up, 'VLAN device missing or down');
  assert.equal(item.live.vlan, 20);
  assert.equal(item.live.parent, trunk);
  assert.equal(item.live.mtu, 1400);
  assert.deepEqual(item.live.addresses, ['10.20.0.2/24']);
  assert.equal(item.metric, 2020);
  const scan = await api('/api/settings/monitoring');
  assert.deepEqual(scan.dns_servers[`${trunk}.20`], ['10.20.0.53']);

  // A batch without addresses; the second ID is rejected as a duplicate while the others are created.
  const batch = await page.evaluate(async (parent) => {
    const r = await fetch('/api/network/vlans', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({parent, vlans: '20, 30-31', naming: 'vlan', mode: 'none', monitor: false})});
    return {status: r.status, body: await r.json()};
  }, trunk);
  assert.equal(batch.status, 201);
  assert.deepEqual(batch.body.created.map(c => c.name).sort(), ['vlan30', 'vlan31']);
  assert.equal(batch.body.failed.length, 1);

  // Edit, down and up through the actions menu.
  await page.reload();
  await menu(`${trunk}.20`, 'Edit addressing');
  await page.getByRole('dialog').locator('input[type=number]').first().fill('1300');
  await page.getByRole('button', {name: 'Save and apply'}).click();
  await until(async () => (await managed(`${trunk}.20`))?.live.mtu === 1300, 'MTU 1300');
  item = await managed(`${trunk}.20`);
  assert.deepEqual(item.live.addresses, ['10.20.0.2/24'], 'address lost on update');
  await menu(`${trunk}.20`, 'Bring down');
  await until(async () => (await managed(`${trunk}.20`))?.live.up === false, 'interface down');
  await menu(`${trunk}.20`, 'Bring up');
  await until(async () => (await managed(`${trunk}.20`))?.live.up === true, 'interface up');

  // Delete everything through the confirmation dialog.
  for (const name of [`${trunk}.20`, 'vlan30', 'vlan31']) {
    await menu(name, 'Delete interface');
    await page.getByRole('dialog').getByRole('button', {name: 'Delete interface'}).click();
    await until(async () => !(await managed(name)), `${name} deleted`);
  }
  const after = await vlans();
  assert.equal(after.interfaces.length, 0);
  assert(!after.trunks.find(t => t.name === trunk).vlans.length, 'VLAN devices left on the trunk');
  assert.equal((await api('/api/settings/monitoring')).dns_servers[`${trunk}.20`], undefined);
  assert.deepEqual(errors, []);
  console.log(`VLAN create, batch, edit, down/up and delete passed on ${trunk} (${after.environment.distro.name}, ${after.environment.backend}).`);
} catch (error) {
  await fs.mkdir('test-results', {recursive: true});
  await page.screenshot({path: 'test-results/vlan-failure.png', fullPage: true});
  console.error('Visible errors:', await page.locator('.notice-error').allTextContents().catch(() => []));
  throw error;
} finally {
  await browser.close();
}
