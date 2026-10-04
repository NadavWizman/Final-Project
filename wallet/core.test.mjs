// Unit tests of the wallet core:  node --test wallet/
import test from 'node:test';
import assert from 'node:assert/strict';
import { P256, pointMul, newRecoveryCode, recoveryCode, seedFromCode, keyFromSeed, signText, describe, toHex } from './core.js';

const b64urlToBig = s => BigInt('0x' + Buffer.from(s, 'base64url').toString('hex'));

test('point multiplication agrees with WebCrypto', async () => {
  for (let i = 0; i < 5; i++) {
    const kp = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign']);
    const jwk = await crypto.subtle.exportKey('jwk', kp.privateKey);
    const [x, y] = pointMul(b64urlToBig(jwk.d));
    assert.equal(x, b64urlToBig(jwk.x));
    assert.equal(y, b64urlToBig(jwk.y));
  }
});

test('recovery code: round trip, readable variants, typos caught', async () => {
  const code = await newRecoveryCode();
  assert.match(code, /^([0-9A-HJKMNP-TV-Z]{4}-){7}[0-9A-HJKMNP-TV-Z]{4}$/);
  const seed = await seedFromCode(code);
  assert.equal(await recoveryCode(seed), code);
  assert.deepEqual(await seedFromCode(code.toLowerCase().replace(/-/g, ' ')), seed);
  const typo = (code[0] === '0' ? '1' : '0') + code.slice(1);
  await assert.rejects(seedFromCode(typo), /typo/);
  await assert.rejects(seedFromCode(code.slice(0, 20)), /32 characters/);
});

test('the same code gives the same account, and the key cannot be exported', async () => {
  const seed = await seedFromCode(await newRecoveryCode());
  const a = await keyFromSeed(seed), b = await keyFromSeed(seed);
  assert.equal(a.address, b.address);
  assert.match(a.address, /^[0-9a-f]{40}$/);
  assert.equal(a.privateKey.extractable, false);
  await assert.rejects(crypto.subtle.exportKey('jwk', a.privateKey));
  await assert.rejects(crypto.subtle.exportKey('pkcs8', a.privateKey));
});

test('signatures verify with the account key and are always low-S', async () => {
  const k = await keyFromSeed(crypto.getRandomValues(new Uint8Array(16)));
  const pub = await crypto.subtle.importKey('raw', k.publicRaw, { name: 'ECDSA', namedCurve: 'P-256' }, false, ['verify']);
  for (let i = 0; i < 100; i++) {
    const text = `{"n":"${i}"}`;
    const sig = Buffer.from(await signText(k.privateKey, text), 'base64');
    assert.equal(sig.length, 64);
    assert.ok(BigInt('0x' + toHex(sig.subarray(32))) <= P256.n / 2n, 'high S');
    assert.ok(await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, pub, sig, new TextEncoder().encode(text)));
  }
});

const base = { chain: 'tradedesk-local', from: 'ab'.repeat(20), nonce: '7' };
const order = o => JSON.stringify({ type: 'order', order: o, ...base });

test('describe shows the details read from the signed bytes', () => {
  const d = describe(order({ kind: 'CFD', side: 'BUY', ticker: 'AAPL', qty: '50', leverage: '10', sl: '90' }));
  assert.equal(d.title, 'Buy · CFD (leveraged)');
  assert.deepEqual(Object.fromEntries(d.rows), {
    Ticker: 'AAPL', Side: 'BUY', Quantity: '50', Price: 'market (median of the signed quotes)', Leverage: '10×', 'Stop-loss': '$90',
  });
  assert.equal(describe(JSON.stringify({ type: 'register', username: 'alice', ...base })).rows[0][1], 'alice');
  assert.equal(describe(JSON.stringify({ type: 'level_cancel', level_id: '12', ...base })).rows[0][1], '#12');
  const close = Object.fromEntries(describe(order({ kind: 'OPT_CLOSE', side: 'SELL', ticker: 'AAPL', qty: '5', position: '9' })).rows);
  assert.equal(close.Contracts, '5 (the whole position)');
});

test('the wallet refuses what it cannot read unambiguously', () => {
  const good = order({ kind: 'STOCK', side: 'BUY', ticker: 'AAPL', qty: '1' });
  const bad = {
    'duplicate key (JSON.parse would keep the last)': good.replace('"qty":"1"', '"qty":"1","qty":"50"'),
    'extra whitespace': good.replace(',', ', '),
    'upper-case key': good.replace('"qty"', '"QTY"'),
    'unknown field': order({ kind: 'STOCK', side: 'BUY', ticker: 'AAPL', qty: '1', note: 'x' }),
    'number instead of text': good.replace('"qty":"1"', '"qty":1'),
    'unknown kind': order({ kind: 'SWAP', side: 'BUY', ticker: 'AAPL', qty: '1' }),
    'unknown type': JSON.stringify({ type: 'deposit', ...base }),
    'missing nonce': JSON.stringify({ type: 'order', order: { kind: 'STOCK', side: 'BUY', ticker: 'AAPL', qty: '1' }, chain: 'x', from: 'y' }),
    'not JSON': 'sign me',
    'leverage on a stock order': order({ kind: 'STOCK', side: 'BUY', ticker: 'AAPL', qty: '1', leverage: '50' }),
    'close without a position': order({ kind: 'CFD_CLOSE', side: 'SELL', ticker: 'AAPL', qty: '1' }),
    'CFD without leverage': order({ kind: 'CFD', side: 'BUY', ticker: 'AAPL', qty: '1' }),
    'username on an order': JSON.stringify({ type: 'order', username: 'x', order: { kind: 'STOCK', side: 'BUY', ticker: 'AAPL', qty: '1' }, ...base }),
  };
  for (const [name, text] of Object.entries(bad)) {
    assert.throws(() => describe(text), undefined, name);
  }
});
