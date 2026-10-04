// Account management: create (shows the recovery code once), restore from a
// recovery code, list, remove. The recovery code is never stored.

import { newRecoveryCode, seedFromCode, keyFromSeed } from './core.js';
import { listAccounts, putAccount, removeAccount } from './store.js';

const $ = s => document.querySelector(s);
const view = name => ['list', 'new', 'restore'].forEach(v => $(`#${v}-view`).classList.toggle('hidden', v !== name));
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
let code = null;

async function render() {
  const list = await listAccounts();
  $('#accounts').innerHTML = list.length
    ? list.map(a => `<div class="account"><div><b>${esc(a.label)}</b><br><small>${a.address}</small></div>
        <button class="secondary" data-remove="${a.address}">Remove</button></div>`).join('')
    : '<p class="note">No account yet.</p>';
  document.querySelectorAll('[data-remove]').forEach(b => b.onclick = async () => {
    if (confirm('Remove this account from the wallet? Without its recovery code it cannot be restored.')) {
      await removeAccount(b.dataset.remove);
      render();
    }
  });
  view('list');
}

async function save(seed, label) {
  const k = await keyFromSeed(seed);
  await putAccount({ address: k.address, label: label || k.address.slice(0, 8), publicRaw: k.publicRaw, privateKey: k.privateKey });
}

$('#new').onclick = async () => {
  code = await newRecoveryCode();
  $('#code').textContent = code;
  $('#new-label').value = '';
  view('new');
};
$('#new-cancel').onclick = () => { code = null; render(); };
$('#new-save').onclick = async () => {
  await save(await seedFromCode(code), $('#new-label').value.trim());
  code = null;
  render();
};
$('#restore').onclick = () => { $('#restore-code').value = ''; $('#restore-error').classList.add('hidden'); view('restore'); };
$('#restore-cancel').onclick = render;
$('#restore-save').onclick = async () => {
  try {
    await save(await seedFromCode($('#restore-code').value), $('#restore-label').value.trim());
    render();
  } catch (e) {
    $('#restore-error').textContent = e.message;
    $('#restore-error').classList.remove('hidden');
  }
};

render();
