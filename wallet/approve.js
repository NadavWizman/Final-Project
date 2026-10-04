// The approval window. It belongs to the wallet (an extension page): the
// TradeDesk site cannot script it or draw in it. It parses the exact text it
// was asked to sign, shows what that text does, and signs only when the user
// presses Approve.

import { describe, signText, b64 } from './core.js';
import { getAccount } from './store.js';

const id = new URLSearchParams(location.search).get('id');
const $ = s => document.querySelector(s);
let pending, answered = false;

function esc(v) {
  return String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

async function answer(body) {
  if (answered || !pending) return;
  answered = true;
  await chrome.storage.session.remove(['req:' + id, 'busy:' + pending.tabId]);
  try {
    await chrome.tabs.sendMessage(pending.tabId, { kind: 'verdict', requestId: pending.requestId, ...body }, { frameId: 0 });
  } catch { /* the page is gone */ }
  window.close();
}

async function show() {
  pending = (await chrome.storage.session.get('req:' + id))['req:' + id];
  if (!pending) {
    $('#body').innerHTML = '<p class="err">This request is no longer waiting.</p>';
    return;
  }
  $('#origin').textContent = pending.origin;
  let d, account;
  try {
    d = describe(pending.text);
    account = await getAccount(d.from);
    if (!account) throw new Error(`account ${d.from.slice(0, 10)}… is not in this wallet`);
  } catch (e) {
    $('#body').innerHTML = `<p class="err">The wallet will not sign this request: ${esc(e.message)}</p>`;
    $('#raw').textContent = pending.text;
    $('#approve').disabled = true;
    return;
  }
  $('#title').textContent = d.title;
  $('#rows').innerHTML = d.rows.map(([k, v]) => `<tr><th>${esc(k)}</th><td>${esc(v)}</td></tr>`).join('');
  $('#account').textContent = `${account.label} · ${account.address.slice(0, 10)}…`;
  $('#meta').textContent = `network ${d.chain} · transaction #${d.nonce}`;
  $('#raw').textContent = pending.text;
  $('#approve').disabled = false;
  $('#approve').onclick = async () => {
    $('#approve').disabled = true;
    const sig = await signText(account.privateKey, pending.text);
    await answer({ result: { sig, pubkey: b64(account.publicRaw) } });
  };
}

$('#reject').onclick = () => answer({ error: 'rejected in the wallet' });
// closing the window without answering: the service worker sends the rejection
show();
