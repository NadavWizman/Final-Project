// Accounts live in the extension's own IndexedDB (the extension's origin,
// which no web page can open). A record holds the address, a label, the raw
// public key and the NON-EXTRACTABLE CryptoKey — never the private key bytes
// and never the recovery code.

const DB = 'tradedesk-wallet', STORE = 'accounts';

function open() {
  return new Promise((resolve, reject) => {
    const r = indexedDB.open(DB, 1);
    r.onupgradeneeded = () => r.result.createObjectStore(STORE, { keyPath: 'address' });
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}

async function run(mode, fn) {
  const db = await open();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, mode);
    const req = fn(tx.objectStore(STORE));
    tx.oncomplete = () => resolve(req && req.result);
    tx.onerror = () => reject(tx.error);
  });
}

export const listAccounts = () => run('readonly', s => s.getAll());
export const getAccount = address => run('readonly', s => s.get(address));
export const putAccount = rec => run('readwrite', s => s.put(rec));
export const removeAccount = address => run('readwrite', s => s.delete(address));
