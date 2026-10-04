// Bridge between a page and the wallet. A page can only ASK:
//   ping      — is the wallet installed?
//   accounts  — the accounts, if this site is already connected (no window)
//   connect   — ask the user, in the wallet's window, to connect this site
//   sign      — ask the user, in the wallet's window, to approve a text
// Answers to connect and sign come from the wallet's window, after the user
// has decided.
//
// Page → wallet:  window.postMessage({to: 'tradedesk-wallet', id, method, text}, origin)
// Wallet → page:  {from: 'tradedesk-wallet', id, result | error}   (and {from, type: 'ready'} on load)

const reply = (id, body) => window.postMessage({ from: 'tradedesk-wallet', id, ...body }, location.origin);

window.addEventListener('message', ev => {
  const m = ev.data;
  if (ev.source !== window || !m || m.to !== 'tradedesk-wallet') return;
  if (m.method === 'ping') return reply(m.id, { result: 'ready' });
  if (!['accounts', 'connect', 'sign'].includes(m.method)) return reply(m.id, { error: 'unknown request' });
  chrome.runtime.sendMessage({ kind: m.method, text: m.text, requestId: m.id }, r => {
    if (!r) return reply(m.id, { error: 'wallet unavailable' });
    if (!r.opened) reply(m.id, r); // answered at once; otherwise the window answers later
  });
});

// the approval window's verdict, delivered by the service worker
chrome.runtime.onMessage.addListener(msg => {
  if (msg && msg.kind === 'verdict') reply(msg.requestId, msg.error ? { error: msg.error } : { result: msg.result });
});

reply(null, { type: 'ready' });
