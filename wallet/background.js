// Service worker: receives requests from the content script of a TradeDesk
// page and opens the wallet's own approval window. It never signs by itself:
// a signature exists only after the user approves in that window.

import { listAccounts } from './store.js';

chrome.runtime.onMessage.addListener((req, sender, reply) => {
  if (!sender.tab) return false; // only from pages, through the content script
  if (req.kind === 'accounts') {
    listAccounts().then(list => reply({ result: list.map(a => ({ address: a.address, label: a.label })) }),
      e => reply({ error: String(e) }));
    return true;
  }
  if (req.kind === 'sign') {
    openApproval(req, sender).then(reply, e => reply({ error: String(e.message || e) }));
    return true;
  }
  reply({ error: 'unknown request' });
  return false;
});

// One request at a time per tab: a page cannot flood the user with windows.
async function openApproval(req, sender) {
  const tabId = sender.tab.id, busyKey = 'busy:' + tabId;
  const busy = (await chrome.storage.session.get(busyKey))[busyKey];
  if (busy && Date.now() - busy < 5 * 60 * 1000) return { error: 'another request from this tab is waiting in the wallet' };
  const id = crypto.randomUUID();
  const pending = { id, text: String(req.text).slice(0, 4096), origin: sender.origin || new URL(sender.url).origin,
    tabId, requestId: req.requestId };
  await chrome.storage.session.set({ ['req:' + id]: pending, [busyKey]: Date.now() });
  const win = await chrome.windows.create({ url: `approve.html?id=${id}`, type: 'popup', width: 420, height: 640 });
  await chrome.storage.session.set({ ['win:' + win.id]: id });
  return { opened: true };
}

// Closing the approval window without answering is a rejection.
chrome.windows.onRemoved.addListener(async windowId => {
  const key = 'win:' + windowId;
  const id = (await chrome.storage.session.get(key))[key];
  if (!id) return;
  const pending = (await chrome.storage.session.get('req:' + id))['req:' + id];
  await chrome.storage.session.remove([key, 'req:' + id]);
  if (!pending) return; // already answered
  await chrome.storage.session.remove('busy:' + pending.tabId);
  chrome.tabs.sendMessage(pending.tabId, { kind: 'verdict', requestId: pending.requestId, error: 'rejected in the wallet' },
    { frameId: 0 }).catch(() => {});
});
