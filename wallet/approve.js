// The approval window. It belongs to the wallet (an extension page): the
// site cannot script it or draw in it. For a signing request it parses the
// exact text, shows what that text does, and signs only when the user
// presses Approve. For a connection request it asks whether this site may
// see the wallet's accounts and ask for signatures.

import { describe, signText, b64, keyFromSeed, unlockSeed } from './core.js';
import { getAccount, listAccounts } from './store.js';

const id = new URLSearchParams(location.search).get('id');
const $ = s => document.querySelector(s);
let pending, done = false;

function esc(v) {
  return String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// The service worker delivers the answer to the page and frees the tab.
async function answer(msg) {
  if (done) return;
  done = true;
  await chrome.runtime.sendMessage({ kind: 'answer', id, ...msg });
  window.close();
}

// Approve becomes clickable only after a moment, so a window that opens
// under the cursor cannot catch a click meant for the page.
function armApprove(onClick) {
  $('#approve').onclick = onClick;
  setTimeout(() => { $('#approve').disabled = false; }, 800);
}

async function showConnect() {
  const list = await listAccounts();
  $('#title').textContent = 'Connect this site?';
  $('#rows').innerHTML = list.map(a => `<tr><th>${esc(a.label)}</th><td>${esc(a.address.slice(0, 18))}…</td></tr>`).join('')
    || '<tr><td>The wallet has no account yet.</td></tr>';
  $('#account').textContent = `${list.length} account${list.length === 1 ? '' : 's'}`;
  $('#meta').textContent = '';
  $('#note').textContent = 'The site will see these addresses and may ask for signatures. Each signature still needs '
    + 'your approval here. You can disconnect the site in the wallet.';
  $('#raw-box').hidden = true;
  $('#approve').textContent = 'Connect';
  armApprove(() => answer({ approved: true }));
}

async function showSign() {
  let d, account;
  try {
    d = describe(pending.text);
    account = await getAccount(d.from);
    if (!account) throw new Error(`account ${d.from.slice(0, 10)}… is not in this wallet`);
    if (!account.vault) throw new Error('this account has no password yet (made by an older wallet): remove it and restore it from its recovery code');
  } catch (e) {
    $('#body').innerHTML = `<p class="err">The wallet will not sign this request: ${esc(e.message)}</p>`;
    $('#raw').textContent = pending.text;
    return;
  }
  $('#title').textContent = d.title;
  $('#rows').innerHTML = d.rows.map(([k, v]) => `<tr><th>${esc(k)}</th><td>${esc(v)}</td></tr>`).join('');
  $('#account').textContent = `${account.label} · ${account.address.slice(0, 10)}…`;
  $('#meta').textContent = `network ${d.chain} · transaction #${d.nonce}`;
  $('#raw').textContent = pending.text;
  // The password unlocks the seed for this one signature: the key is derived,
  // used once and dropped with the seed. A wrong password signs nothing, and
  // the next try waits a moment.
  $('#unlock').hidden = false;
  $('#approve').textContent = 'Unlock and sign';
  $('#password').onkeydown = e => { if (e.key === 'Enter' && !$('#approve').disabled) $('#approve').click(); };
  $('#password').focus();
  armApprove(async () => {
    const err = $('#pw-error');
    if (!$('#password').value) { err.textContent = 'Enter the wallet password.'; err.hidden = false; return; }
    $('#approve').disabled = true;
    err.hidden = true;
    let seed;
    try {
      seed = await unlockSeed(account.vault, $('#password').value, account.address);
    } catch (e) {
      err.textContent = e.message === 'wrong password' ? 'Wrong password — nothing was signed.' : e.message;
      err.hidden = false;
      $('#password').value = '';
      setTimeout(() => { $('#approve').disabled = false; $('#password').focus(); }, 1500);
      return;
    }
    $('#password').value = '';
    const key = await keyFromSeed(seed);
    seed.fill(0);
    if (key.address !== account.address) throw new Error('the unlocked key is not this account');
    const sig = await signText(key.privateKey, pending.text);
    await answer({ body: { result: { sig, pubkey: b64(account.publicRaw) } } });
  });
}

async function show() {
  pending = (await chrome.storage.session.get('req:' + id))['req:' + id];
  if (!pending) {
    $('#body').innerHTML = '<p class="err">This request is no longer waiting.</p>';
    return;
  }
  $('#origin').textContent = pending.origin;
  if (pending.kind === 'connect') await showConnect(); else await showSign();
}

$('#reject').onclick = () => answer({ approved: false, body: { error: 'rejected in the wallet' } });
// closing the window without answering: the service worker sends the rejection
show();
