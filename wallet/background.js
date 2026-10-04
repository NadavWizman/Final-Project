// Service worker: receives requests from the content script of a page and
// opens the wallet's own approval window. It never signs by itself: a
// signature exists only after the user approves in that window.
//
// A site must first be connected (a one-time approval per origin, which can
// be revoked in the wallet); until then it can neither see the accounts nor
// ask for a signature.

import { listAccounts } from './store.js';

const busy = new Map(); // tabId → true while an approval window is open for it

async function connected(origin) {
  const { allowed = {} } = await chrome.storage.local.get('allowed');
  return Boolean(allowed[origin]);
}

const accountsList = async () => (await listAccounts()).map(a => ({ address: a.address, label: a.label }));

// The answer goes only to the document that asked: if the tab has meanwhile
// navigated elsewhere, nobody receives it.
function deliver(pending, body) {
  const target = pending.documentId ? { documentId: pending.documentId } : { frameId: 0 };
  return chrome.tabs.sendMessage(pending.tabId, { kind: 'verdict', requestId: pending.requestId, ...body }, target)
    .catch(() => {});
}

// One request at a time per tab: a page cannot stack approval windows. The
// tab is marked busy synchronously, before any await, so concurrent requests
// cannot slip past the check.
async function openApproval(kind, text, req, sender) {
  const tabId = sender.tab.id;
  if (busy.get(tabId)) return { error: 'another request from this tab is waiting in the wallet' };
  busy.set(tabId, true);
  try {
    const id = crypto.randomUUID();
    const pending = { id, kind, text, origin: sender.origin, tabId, documentId: sender.documentId, requestId: req.requestId };
    await chrome.storage.session.set({ ['req:' + id]: pending });
    const win = await chrome.windows.create({ url: `approve.html?id=${id}`, type: 'popup', width: 420, height: 640 });
    await chrome.storage.session.set({ ['win:' + win.id]: id });
    return { opened: true };
  } catch (e) {
    busy.delete(tabId);
    throw e;
  }
}

const APPROVE_PAGE = chrome.runtime.getURL('approve.html');

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  // the wallet's own approval window reports its answer (content scripts
  // share the extension id, so the sender is told apart by its page URL)
  if (sender.url && sender.url.startsWith(APPROVE_PAGE)) {
    if (msg && msg.kind === 'answer') {
      answered(msg).then(() => reply({ ok: true }), () => reply({ ok: false }));
      return true;
    }
    return false;
  }
  // a page, through the content script
  if (!sender.tab || !sender.origin || sender.origin.startsWith('chrome-extension:')) return false;
  (async () => {
    switch (msg.kind) {
      case 'accounts': // silent: only for a connected site
        return (await connected(sender.origin)) ? { result: await accountsList() } : { error: 'not connected' };
      case 'connect':
        if (await connected(sender.origin)) return { result: await accountsList() };
        return openApproval('connect', '', msg, sender);
      case 'sign':
        if (!(await connected(sender.origin))) return { error: 'this site is not connected to the wallet' };
        return openApproval('sign', String(msg.text).slice(0, 4096), msg, sender);
      default:
        return { error: 'unknown request' };
    }
  })().then(reply, e => reply({ error: String(e.message || e) }));
  return true;
});

async function answered(msg) {
  const pending = (await chrome.storage.session.get('req:' + msg.id))['req:' + msg.id];
  if (!pending || pending.answered) return;
  pending.answered = true;
  await chrome.storage.session.set({ ['req:' + msg.id]: pending });
  if (pending.kind === 'connect' && msg.approved) {
    const { allowed = {} } = await chrome.storage.local.get('allowed');
    allowed[pending.origin] = Date.now();
    await chrome.storage.local.set({ allowed });
    await deliver(pending, { result: await accountsList() });
  } else {
    await deliver(pending, msg.body);
  }
}

// When the approval window closes — answered or not — the tab is free again;
// closing it without answering is a rejection.
chrome.windows.onRemoved.addListener(async windowId => {
  const key = 'win:' + windowId;
  const id = (await chrome.storage.session.get(key))[key];
  if (!id) return;
  const pending = (await chrome.storage.session.get('req:' + id))['req:' + id];
  await chrome.storage.session.remove([key, 'req:' + id]);
  if (!pending) return;
  busy.delete(pending.tabId);
  if (!pending.answered) deliver(pending, { error: 'rejected in the wallet' });
});

chrome.tabs.onRemoved.addListener(tabId => busy.delete(tabId));
