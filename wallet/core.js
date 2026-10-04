// TradeDesk Wallet — the cryptographic core. Runs only inside the extension
// (and in its unit tests); nothing here comes from the TradeDesk site.
//
// - A key is derived from a 128-bit seed. The seed is shown once to the user
//   as a recovery code; the private key is imported into WebCrypto as
//   NON-EXTRACTABLE, so no script — not even the wallet's own — can read it
//   back. Restoring = typing the recovery code on another browser.
// - The wallet signs only a message it can fully read: canonical JSON with
//   known fields only. What the approval window shows is parsed from the
//   exact bytes that are signed.
// - Signatures are ECDSA P-256 / SHA-256, r‖s, in the low-S form the chain
//   accepts.

const enc = new TextEncoder();

// ── P-256 ─────────────────────────────────────────────────────────────
export const P256 = {
  p: 0xffffffff00000001000000000000000000000000ffffffffffffffffffffffffn,
  n: 0xffffffff00000000ffffffffffffffffbce6faada7179e84f3b9cac2fc632551n,
  b: 0x5ac635d8aa3a93e7b3ebbd55769886bc651d06b0cc53b0f63bce3c3e27d2604bn,
  gx: 0x6b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c296n,
  gy: 0x4fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5n,
};

const mod = (a, m) => ((a % m) + m) % m;

function inv(a, m) {
  let [r0, r1, s0, s1] = [mod(a, m), m, 1n, 0n];
  while (r1 !== 0n) {
    const q = r0 / r1;
    [r0, r1] = [r1, r0 - q * r1];
    [s0, s1] = [s1, s0 - q * s1];
  }
  return mod(s0, m);
}

// affine point addition on y² = x³ − 3x + b; null is the point at infinity
function add(P, Q) {
  const { p } = P256;
  if (!P) return Q;
  if (!Q) return P;
  let l;
  if (P[0] === Q[0]) {
    if (mod(P[1] + Q[1], p) === 0n) return null;
    l = mod((3n * P[0] * P[0] - 3n) * inv(2n * P[1], p), p);
  } else {
    l = mod((Q[1] - P[1]) * inv(Q[0] - P[0], p), p);
  }
  const x = mod(l * l - P[0] - Q[0], p);
  return [x, mod(l * (P[0] - x) - P[1], p)];
}

export function pointMul(k, P = [P256.gx, P256.gy]) {
  let R = null;
  for (let Q = P; k > 0n; k >>= 1n, Q = add(Q, Q)) {
    if (k & 1n) R = add(R, Q);
  }
  return R;
}

// ── encodings ─────────────────────────────────────────────────────────
export const toHex = b => [...new Uint8Array(b)].map(x => x.toString(16).padStart(2, '0')).join('');
const fromHex = h => Uint8Array.from(h.match(/../g), x => parseInt(x, 16));
const bigToBytes = (v, len = 32) => fromHex(v.toString(16).padStart(len * 2, '0'));
const bytesToBig = b => BigInt('0x' + (toHex(b) || '0'));
export const b64 = b => btoa(String.fromCharCode(...new Uint8Array(b)));
const b64url = b => b64(b).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

// Crockford base32: no I, L, O, U, so codes are easy to copy by hand.
const B32 = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';

function base32(bytes) {
  let bits = 0, value = 0, out = '';
  for (const byte of bytes) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += B32[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += B32[(value << (5 - bits)) & 31];
  return out;
}

function unbase32(text, nBytes) {
  let bits = 0, value = 0;
  const out = [];
  for (const ch of text) {
    const v = B32.indexOf(ch);
    if (v < 0) throw new Error(`"${ch}" is not part of a recovery code`);
    value = (value << 5) | v;
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 255);
      bits -= 8;
    }
  }
  if (out.length < nBytes) throw new Error('the recovery code is too short');
  return Uint8Array.from(out.slice(0, nBytes));
}

// ── recovery codes ────────────────────────────────────────────────────
// 16 random bytes + a 4-byte checksum (the start of SHA-256) = 160 bits =
// exactly 32 characters, written in 8 groups of 4. A typo is caught with
// probability 1 − 2⁻³², and there are no spare bits, so every seed has one code.
export async function newRecoveryCode() {
  return recoveryCode(crypto.getRandomValues(new Uint8Array(16)));
}

export async function recoveryCode(seed) {
  const sum = new Uint8Array(await crypto.subtle.digest('SHA-256', seed));
  return base32([...seed, ...sum.slice(0, 4)]).match(/.{4}/g).join('-');
}

export async function seedFromCode(code) {
  const clean = String(code).toUpperCase().replace(/[\s-]/g, '')
    .replace(/O/g, '0').replace(/[IL]/g, '1');
  if (clean.length !== 32) throw new Error('a recovery code has 32 characters (8 groups of 4)');
  const raw = unbase32(clean, 20);
  const seed = raw.slice(0, 16);
  const sum = new Uint8Array(await crypto.subtle.digest('SHA-256', seed));
  if (sum.slice(0, 4).some((b, i) => b !== raw[16 + i])) throw new Error('the recovery code has a typo (checksum does not match)');
  return seed;
}

// ── keys ──────────────────────────────────────────────────────────────
// The P-256 private scalar is d = HKDF-SHA256(seed) mod (n − 1) + 1, and the
// public point is computed here, so the private key can be imported straight
// into WebCrypto as non-extractable — it is never exported, not even once.
export async function keyFromSeed(seed) {
  const ikm = await crypto.subtle.importKey('raw', seed, 'HKDF', false, ['deriveBits']);
  const bits = await crypto.subtle.deriveBits(
    { name: 'HKDF', hash: 'SHA-256', salt: enc.encode('tradedesk-wallet'), info: enc.encode('p256-signing-key') },
    ikm, 384);
  const d = bytesToBig(new Uint8Array(bits)) % (P256.n - 1n) + 1n;
  const [x, y] = pointMul(d);
  const jwk = { kty: 'EC', crv: 'P-256', d: b64url(bigToBytes(d)), x: b64url(bigToBytes(x)), y: b64url(bigToBytes(y)), ext: false };
  const privateKey = await crypto.subtle.importKey('jwk', jwk, { name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
  const publicRaw = Uint8Array.from([4, ...bigToBytes(x), ...bigToBytes(y)]);
  return { privateKey, publicRaw, address: await addressOf(publicRaw) };
}

// The chain's account address: the first 20 bytes of SHA-256(public key).
export async function addressOf(publicRaw) {
  return toHex(await crypto.subtle.digest('SHA-256', publicRaw)).slice(0, 40);
}

// Signs text with a non-extractable key; returns base64 r‖s in low-S form.
export async function signText(privateKey, text) {
  const sig = new Uint8Array(await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, privateKey, enc.encode(text)));
  const s = bytesToBig(sig.slice(32));
  if (s > P256.n / 2n) sig.set(bigToBytes(P256.n - s), 32);
  return b64(sig);
}

// ── reading what is signed ────────────────────────────────────────────
// The wallet signs only canonical JSON (exactly what JSON.stringify gives
// for it: no duplicate keys, no extra spaces) made of the chain's known
// fields with string values. Anything else is refused, so the details shown
// to the user are the only reading of the signed bytes.
const FIELDS = {
  msg: { register: ['username'], order: ['order'], level_add: ['level'], level_cancel: ['level_id'] },
  level: ['kind', 'ticker', 'cfd', 'price', 'qty'],
};
// The fields each order kind may carry — the same rules the chain enforces,
// so every field of a signable order is shown and acted on.
const ORDER_FIELDS = {
  STOCK: ['kind', 'side', 'ticker', 'qty', 'limit', 'sl', 'sl_qty', 'tp', 'tp_qty'],
  CFD: ['kind', 'side', 'ticker', 'qty', 'limit', 'leverage', 'sl', 'sl_qty', 'tp', 'tp_qty'],
  CFD_CLOSE: ['kind', 'side', 'ticker', 'qty', 'position'],
  OPTION: ['kind', 'side', 'ticker', 'qty', 'option_type', 'strike', 'expiry'],
  OPT_CLOSE: ['kind', 'side', 'ticker', 'qty', 'position'],
  OPT_EXER: ['kind', 'side', 'ticker', 'qty', 'position'],
};
const KIND_LABEL = {
  STOCK: 'Stock', CFD: 'CFD (leveraged)', CFD_CLOSE: 'Close CFD position',
  OPTION: 'Buy option', OPT_CLOSE: 'Sell option back', OPT_EXER: 'Exercise option',
};

function checkObject(obj, allowed, where) {
  if (typeof obj !== 'object' || obj === null || Array.isArray(obj)) throw new Error(`${where} must be an object`);
  for (const [k, v] of Object.entries(obj)) {
    if (!allowed.includes(k)) throw new Error(`unknown field "${k}" in ${where}`);
    if (typeof v !== 'string' && !(k === 'order' || k === 'level')) throw new Error(`field "${k}" must be text`);
  }
}

// describe returns {type, from, chain, nonce, title, rows: [[label, value]]}
// or throws if the wallet must not sign the text.
export function describe(text) {
  let m;
  try { m = JSON.parse(text); } catch { throw new Error('not a TradeDesk transaction (not JSON)'); }
  if (JSON.stringify(m) !== text) throw new Error('not in canonical form (duplicate keys or extra characters)');
  if (typeof m !== 'object' || m === null || !FIELDS.msg[m.type]) throw new Error(`unknown transaction type "${m && m.type}"`);
  checkObject(m, ['type', 'chain', 'from', 'nonce', ...FIELDS.msg[m.type]], 'the transaction');
  for (const k of ['type', 'chain', 'from', 'nonce', ...FIELDS.msg[m.type]]) {
    if (m[k] === undefined || m[k] === '') throw new Error(`missing "${k}"`);
  }
  const rows = [];
  let title;
  switch (m.type) {
    case 'register':
      title = 'Create account';
      rows.push(['Username', m.username]);
      break;
    case 'order': {
      const o = m.order;
      if (!o || !ORDER_FIELDS[o.kind]) throw new Error(`unknown order kind "${o && o.kind}"`);
      checkObject(o, ORDER_FIELDS[o.kind], `a ${o.kind} order`);
      if (o.side !== 'BUY' && o.side !== 'SELL') throw new Error('side must be BUY or SELL');
      const closing = ['CFD_CLOSE', 'OPT_CLOSE', 'OPT_EXER'].includes(o.kind);
      const required = ['ticker', 'qty', ...(closing ? ['position'] : []), ...(o.kind === 'CFD' ? ['leverage'] : []),
        ...(o.kind === 'OPTION' ? ['option_type', 'strike', 'expiry'] : [])];
      for (const k of required) if (!o[k]) throw new Error(`missing "${k}" in a ${o.kind} order`);
      if (closing) {
        title = KIND_LABEL[o.kind];
        rows.push(['Ticker', o.ticker], ['Position', '#' + o.position]);
        rows.push(o.kind === 'CFD_CLOSE' ? ['Quantity', o.qty] : ['Contracts', o.qty + ' (the whole position)']);
        rows.push(['Price', 'market (median of the signed quotes)']);
        break;
      }
      title = `${o.side === 'BUY' ? 'Buy' : 'Sell'} · ${KIND_LABEL[o.kind]}`;
      rows.push(['Ticker', o.ticker], ['Side', o.side], [o.kind === 'OPTION' ? 'Contracts' : 'Quantity', o.qty]);
      if (o.limit) rows.push(['Limit price', '$' + o.limit]);
      else if (o.kind !== 'OPTION') rows.push(['Price', 'market (median of the signed quotes)']);
      if (o.leverage) rows.push(['Leverage', o.leverage + '×']);
      if (o.kind === 'OPTION') rows.push(['Option', `${o.option_type} strike $${o.strike} expiring ${o.expiry}`]);
      if (o.sl) rows.push(['Stop-loss', `$${o.sl}` + (o.sl_qty ? ` for ${o.sl_qty}` : '')]);
      if (o.tp) rows.push(['Take-profit', `$${o.tp}` + (o.tp_qty ? ` for ${o.tp_qty}` : '')]);
      break;
    }
    case 'level_add': {
      const l = m.level;
      checkObject(l, FIELDS.level, 'the level');
      if (l.kind !== 'SL' && l.kind !== 'TP') throw new Error('level kind must be SL or TP');
      title = `Add ${l.kind === 'SL' ? 'stop-loss' : 'take-profit'}`;
      rows.push([l.cfd ? 'CFD position' : 'Ticker', l.cfd ? '#' + l.cfd : l.ticker], ['Price', '$' + l.price], ['Quantity', l.qty]);
      break;
    }
    case 'level_cancel':
      title = 'Cancel a stop-loss / take-profit';
      rows.push(['Level', '#' + m.level_id]);
      break;
  }
  return { type: m.type, from: m.from, chain: m.chain, nonce: m.nonce, title, rows };
}
