// Account management: create (shows the recovery code once), restore from a
// recovery code, list, remove. The recovery code is never stored; the seed is
// stored only encrypted under the password chosen here.

import { newRecoveryCode, seedFromCode, keyFromSeed, lockSeed, checkNewPassword } from './core.js';
import { listAccounts, putAccount, removeAccount } from './store.js';

const $ = s => document.querySelector(s);
const view = name => ['list', 'new', 'restore'].forEach(v => $(`#${v}-view`).classList.toggle('hidden', v !== name));
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
let code = null;

async function render() {
  const list = await listAccounts();
  $('#accounts').innerHTML = list.length
    ? list.map(a => `<div class="account"><div><b>${esc(a.label)}</b>${a.vault ? '' : ' <span class="err">· no password: remove and restore it</span>'}<br><small>${a.address}</small></div>
        <button class="secondary" data-remove="${a.address}">Remove</button></div>`).join('')
    : '<p class="note">No account yet.</p>';
  document.querySelectorAll('[data-remove]').forEach(b => b.onclick = async () => {
    if (confirm('Remove this account from the wallet? Without its recovery code it cannot be restored.')) {
      await removeAccount(b.dataset.remove);
      showList();
    }
  });
  const { allowed = {} } = await chrome.storage.local.get('allowed');
  const sites = Object.keys(allowed);
  $('#sites').innerHTML = sites.length
    ? sites.map(o => `<div class="account"><small>${esc(o)}</small><button class="secondary" data-site="${esc(o)}">Disconnect</button></div>`).join('')
    : '<p class="note">None.</p>';
  document.querySelectorAll('[data-site]').forEach(b => b.onclick = async () => {
    const { allowed = {} } = await chrome.storage.local.get('allowed');
    delete allowed[b.dataset.site];
    await chrome.storage.local.set({ allowed });
    render();
  });
}

// back to the account list after an action (the first load only fills the
// lists, so a quick click is never undone by it)
const showList = async () => { await render(); view('list'); };

// Only the public key and the encrypted seed are stored — no key object.
async function save(seed, label, password) {
  const k = await keyFromSeed(seed);
  const vault = await lockSeed(seed, password, k.address);
  seed.fill(0);
  await putAccount({ address: k.address, label: label || k.address.slice(0, 8), publicRaw: k.publicRaw, vault });
}

const showError = (id, e) => { $(id).textContent = e.message; $(id).classList.remove('hidden'); };

$('#new').onclick = async () => {
  code = await newRecoveryCode();
  $('#code').textContent = code;
  $('#new-label').value = $('#new-password').value = $('#new-password2').value = '';
  $('#new-error').classList.add('hidden');
  view('new');
};
$('#new-cancel').onclick = () => { code = null; showList(); };
$('#new-save').onclick = async () => {
  try {
    checkNewPassword($('#new-password').value, $('#new-password2').value);
    $('#new-save').disabled = true;
    await save(await seedFromCode(code), $('#new-label').value.trim(), $('#new-password').value);
    code = null;
    showList();
  } catch (e) {
    showError('#new-error', e);
  } finally {
    $('#new-save').disabled = false;
  }
};
$('#restore').onclick = () => {
  $('#restore-code').value = $('#restore-password').value = $('#restore-password2').value = '';
  $('#restore-error').classList.add('hidden');
  view('restore');
};
$('#restore-cancel').onclick = showList;
$('#restore-save').onclick = async () => {
  try {
    checkNewPassword($('#restore-password').value, $('#restore-password2').value);
    $('#restore-save').disabled = true;
    await save(await seedFromCode($('#restore-code').value), $('#restore-label').value.trim(), $('#restore-password').value);
    showList();
  } catch (e) {
    showError('#restore-error', e);
  } finally {
    $('#restore-save').disabled = false;
  }
};

render();
