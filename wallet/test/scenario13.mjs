// Acceptance scenario 13: a compromised gateway serves a page that shows the
// user one order but asks the wallet to sign another (50 shares instead of
// 1). The wallet must show the real quantity — read from the bytes it would
// sign — and no signature may exist unless the user approves.
//
//   cd wallet && npm install && npx playwright-core install chromium
//   node test/scenario13.mjs
//
// Runs the real extension in Chromium (headless), with no TradeDesk network:
// the page here plays the compromised gateway.
import { chromium } from 'playwright-core';
import http from 'node:http';
import path from 'node:path';
import os from 'node:os';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';

const WALLET = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const N = 0xffffffff00000000ffffffffffffffffbce6faada7179e84f3b9cac2fc632551n;

// The compromised gateway's page: it tells the user "1 share" and asks the
// wallet to sign whatever the test passes in.
const PAGE = `<!doctype html><title>TradeDesk</title><p id="shown">Buy 1 AAPL</p><script>
  window.results = {};
  window.addEventListener('message', ev => {
    const m = ev.data;
    if (ev.source === window && m && m.from === 'tradedesk-wallet' && m.id) window.results[m.id] = m;
  });
  window.askWallet = (id, method, text) => window.postMessage({to: 'tradedesk-wallet', id, method, text}, location.origin);
</script>`;

const server = http.createServer((_, res) => { res.setHeader('Content-Type', 'text/html'); res.end(PAGE); });
await new Promise(r => server.listen(0, '127.0.0.1', r));
const site = `http://127.0.0.1:${server.address().port}/`;

const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'tradedesk-wallet-'));
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: true,
  args: [`--disable-extensions-except=${WALLET}`, `--load-extension=${WALLET}`],
});
const results = [];
const check = (name, ok, note = '') => { results.push(ok); console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${note ? '  — ' + note : ''}`); };

try {
  let [sw] = ctx.serviceWorkers();
  if (!sw) sw = await ctx.waitForEvent('serviceworker');
  const ext = new URL(sw.url()).host;

  // 1. create an account in the wallet
  const popup = await ctx.newPage();
  await popup.goto(`chrome-extension://${ext}/popup.html`);
  await popup.click('#new');
  const code = await popup.textContent('#code');
  await popup.fill('#new-label', 'alice');
  await popup.click('#new-save');
  await popup.waitForSelector('.account small');
  const address = await popup.textContent('.account small');
  check('Account created in the wallet, recovery code shown once', /^([0-9A-Z]{4}-){7}[0-9A-Z]{4}$/.test(code), address);

  // the key is non-extractable: not even the wallet's own pages can export it
  const exported = await popup.evaluate(async () => {
    const db = await new Promise(r => { const q = indexedDB.open('tradedesk-wallet', 1); q.onsuccess = () => r(q.result); });
    const rec = await new Promise(r => { const q = db.transaction('accounts').objectStore('accounts').getAll(); q.onsuccess = () => r(q.result[0]); });
    try { await crypto.subtle.exportKey('jwk', rec.privateKey); return 'EXPORTED'; } catch (e) { return `${rec.privateKey.extractable}:${e.name}`; }
  });
  check('Private key is non-extractable', exported.startsWith('false:'), exported);

  const page = await ctx.newPage();
  await page.goto(site);
  const call = (id, method, text) => page.evaluate(([i, m, t]) => window.askWallet(i, m, t), [id, method, text]);
  const waitResult = id => page.waitForFunction(i => window.results[i], id).then(() => page.evaluate(i => window.results[i], id));

  // 1b. a site that is not connected sees nothing and cannot ask for a signature
  await call('acc0', 'accounts');
  const acc0 = await waitResult('acc0');
  await call('sig0', 'sign', '{}');
  const sig0 = await waitResult('sig0');
  check('Unconnected site gets no accounts and cannot ask to sign',
    acc0.error === 'not connected' && /not connected/.test(sig0.error), `${acc0.error} / ${sig0.error}`);

  // 1c. connecting needs the user's approval in the wallet
  const [cwin] = await Promise.all([ctx.waitForEvent('page'), call('conn', 'connect')]);
  await cwin.waitForLoadState();
  await cwin.waitForFunction(() => document.querySelector('#title')?.textContent);
  const armedAtOnce = !(await cwin.isDisabled('#approve'));
  await cwin.waitForFunction(() => !document.querySelector('#approve').disabled);
  await cwin.click('#approve');
  const conn = await waitResult('conn');
  check('Connecting the site needs approval; Approve is not clickable at once',
    !armedAtOnce && conn.result?.[0]?.address === address, JSON.stringify(conn.result));
  const order = qty => JSON.stringify({ type: 'order', order: { kind: 'STOCK', side: 'BUY', ticker: 'AAPL', qty },
    chain: 'tradedesk-local', from: address, nonce: '1' });
  const approvalFor = async (id, text) => {
    const [win] = await Promise.all([ctx.waitForEvent('page'), page.evaluate(([i, t]) => window.askWallet(i, 'sign', t), [id, text])]);
    await win.waitForLoadState();
    await win.waitForFunction(() => document.querySelector('.err') || document.querySelector('#title')?.textContent);
    await win.waitForFunction(() => !document.querySelector('#approve').disabled || document.querySelector('.err'));
    return win;
  };
  const result = id => page.evaluate(i => window.results[i] || null, id);

  // 2. the attack: the page shows 1 share and asks for 50
  const win = await approvalFor('attack', order('50'));
  const rows = Object.fromEntries(await win.$$eval('#rows tr', trs => trs.map(tr => [tr.cells[0].textContent, tr.cells[1].textContent])));
  check('Page shows the user 1 share', (await page.textContent('#shown')) === 'Buy 1 AAPL');
  check('Wallet shows the real quantity from the signed bytes', rows.Quantity === '50' && rows.Ticker === 'AAPL', JSON.stringify(rows));
  check('Wallet shows which site is asking', (await win.textContent('#origin')) === site.slice(0, -1));
  await page.waitForTimeout(1500);
  check('No signature exists while the user has not approved', (await result('attack')) === null);
  await win.click('#reject');
  await page.waitForFunction(() => window.results.attack);
  const rejected = await result('attack');
  check('Rejecting gives the page no signature', !rejected.result && /rejected/.test(rejected.error), rejected.error);

  // 2b. requests fired together open one window; the others are refused
  const opened = [];
  const onPage = p => opened.push(p);
  ctx.on('page', onPage);
  await page.evaluate(t => { for (const i of ['c1', 'c2', 'c3']) window.askWallet(i, 'sign', t); }, order('50'));
  await page.waitForFunction(() => window.results.c2 && window.results.c3);
  await page.waitForTimeout(500);
  ctx.off('page', onPage);
  const refused = await page.evaluate(() => [window.results.c2.error, window.results.c3.error]);
  check('Concurrent requests open a single approval window', opened.length === 1 && refused.every(e => /waiting/.test(e)),
    `${opened.length} window(s): ${refused.join(' / ')}`);
  await opened[0].waitForLoadState();
  await opened[0].click('#reject');
  await waitResult('c1');

  // 3. closing the window is a rejection too
  const win2 = await approvalFor('closed', order('50'));
  await win2.close();
  await page.waitForFunction(() => window.results.closed);
  check('Closing the approval window gives no signature', !(await result('closed')).result);

  // 4. a message the wallet cannot read unambiguously is never signable
  const dup = order('1').replace('"qty":"1"', '"qty":"1","qty":"50"');
  const win3 = await approvalFor('dup', dup);
  check('Duplicate-key message is refused, Approve disabled',
    (await win3.isDisabled('#approve')) && /canonical/.test(await win3.textContent('#body')));
  await win3.click('#reject');
  await page.waitForFunction(() => window.results.dup);

  // 5. the honest path: approve, and the signature verifies under the account key
  const text = order('1');
  const win4 = await approvalFor('ok', text);
  await win4.click('#approve');
  await page.waitForFunction(() => window.results.ok);
  const ok = (await result('ok')).result;
  const sig = Buffer.from(ok.sig, 'base64');
  const pubRaw = Buffer.from(ok.pubkey, 'base64');
  const pub = await crypto.subtle.importKey('raw', pubRaw, { name: 'ECDSA', namedCurve: 'P-256' }, false, ['verify']);
  const valid = await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, pub, sig, new TextEncoder().encode(text));
  const lowS = BigInt('0x' + sig.subarray(32).toString('hex')) <= N / 2n;
  const addr = Buffer.from(await crypto.subtle.digest('SHA-256', pubRaw)).toString('hex').slice(0, 40);
  check('Approved signature verifies, low-S, under this account', valid && lowS && addr === address);

  // 6. restoring from the recovery code gives the same account
  popup.once('dialog', d => d.accept());   // "Remove this account?"
  await popup.click('.account button');
  await popup.waitForSelector('#accounts .note');
  await popup.click('#restore');
  await popup.fill('#restore-code', code.toLowerCase());
  await popup.fill('#restore-label', 'alice again');
  await popup.click('#restore-save');
  await popup.waitForSelector('.account small');
  check('Recovery code restores the same address', (await popup.textContent('.account small')) === address);
} catch (e) {
  check('scenario ran', false, e.stack);
} finally {
  await ctx.close();
  server.close();
}
const passed = results.filter(Boolean).length;
console.log(`\n${passed}/${results.length} checks passed`);
process.exit(passed === results.length ? 0 : 1);
