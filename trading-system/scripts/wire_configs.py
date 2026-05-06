#!/usr/bin/env python3
"""Fill in the REPLACE_WITH_* placeholders in node configs and genesis.

Run *after* scripts/gen_keys.py. Takes keys/bundle.json and rewrites
node/configs/node-{1,2,3}.json and node/configs/genesis.json in-place.
"""
from __future__ import annotations

import base64
import json
from pathlib import Path

from cryptography.hazmat.primitives import serialization


ROOT = Path(__file__).resolve().parent.parent
KEYS = ROOT / "keys"
CONFIGS = ROOT / "node" / "configs"


def pem_to_der_b64(pem: str) -> str:
    pub = serialization.load_pem_public_key(pem.encode())
    der = pub.public_bytes(
        encoding=serialization.Encoding.DER,
        format=serialization.PublicFormat.SubjectPublicKeyInfo,
    )
    return base64.standard_b64encode(der).decode()


def main() -> None:
    bundle = json.loads((KEYS / "bundle.json").read_text())

    # 1. Node configs — fill in authorities' pub_key_pem.
    auth_pems = {a["node_id"]: a["pub_key_pem"] for a in bundle["authorities"]}
    for n in ("node-1", "node-2", "node-3"):
        path = CONFIGS / f"{n}.json"
        cfg = json.loads(path.read_text())
        for a in cfg["authorities"]:
            a["pub_key_pem"] = auth_pems[a["node_id"]]
        path.write_text(json.dumps(cfg, indent=2) + "\n")
        print(f"wrote {path}")

    # 2. Genesis — fill in user DER pub keys (base64-encoded for the []byte field).
    genesis_path = CONFIGS / "genesis.json"
    genesis = json.loads(genesis_path.read_text())
    genesis.pop("_note", None)
    for acc in genesis["accounts"]:
        user = bundle["users"].get(acc["user_id"])
        if user is None:
            print(f"WARN: no key for user {acc['user_id']}, leaving placeholder")
            continue
        acc["pub_key_der"] = pem_to_der_b64(user["pub_pem"])
    genesis_path.write_text(json.dumps(genesis, indent=2) + "\n")
    print(f"wrote {genesis_path}")


if __name__ == "__main__":
    main()
