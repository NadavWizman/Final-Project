// Bridge between a TradeDesk page and the wallet. The page can only ASK:
// "which accounts are there?" and "please sign this text". The answer to a
// signing request comes from the wallet's approval window, after the user
// has read the details there and approved.
//
// Page → wallet:  window.postMessage({to: 'tradedesk-wallet', id, method: 'accounts' | 'sign', text}, origin)
// Wallet → page:  {from: 'tradedesk-wallet', id, result | error}   (and {from, type: 'ready'} on load)

const reply = (id, body) => window.postMessage({ from: 'tradedesk-wallet', id, ...body }, location.origin);

window.addEventListener('message', ev => {
  const m = ev.data;
  if (ev.source !== window || !m || m.to !== 'tradedesk-wallet') return;
  if (m.method === 'ping') return reply(m.id, { result: 'ready' });
  if (m.method === 'accounts') {
    chrome.runtime.sendMessage({ kind: 'accounts' }, r => reply(m.id, r || { error: 'wallet unavailable' }));
  } else if (m.method === 'sign') {
    chrome.runtime.sendMessage({ kind: 'sign', text: String(m.text), requestId: m.id }, r => {
      if (!r || r.error) reply(m.id, { error: (r && r.error) || 'wallet unavailable' });
      // otherwise the approval window answers later, through onMessage below
    });
  }
});

// the approval window's verdict
chrome.runtime.onMessage.addListener(msg => {
  if (msg && msg.kind === 'verdict') reply(msg.requestId, msg.error ? { error: msg.error } : { result: msg.result });
});

reply(null, { type: 'ready' });
