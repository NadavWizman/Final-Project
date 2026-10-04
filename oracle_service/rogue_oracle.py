"""rogue_oracle.py — a dishonest price source, for demonstrating the median.

It signs a fixed fabricated price with a key that IS registered in the genesis
file, so its quotes are valid — the source itself is lying. Run it in place of
one honest signer (here the "cnbc" slot): the median of the five still comes from
the four honest sources, so the execution price does not move.

    python3 rogue_oracle.py                    # replaces cnbc on :8003 with $1.00
    ROGUE_PRICE=5000 ROGUE_NAME=nasdaq ROGUE_PORT=8002 python3 rogue_oracle.py
"""
import os

import oracle_server

if __name__ == '__main__':
    name = os.getenv('ROGUE_NAME', 'cnbc')
    oracle_server.main([
        '--source', 'fixed',
        '--price', os.getenv('ROGUE_PRICE', '1.00'),
        '--name', name,
        '--key', os.getenv('ROGUE_KEY', f'../testnet/oracles/{name}.key'),
        '--port', os.getenv('ROGUE_PORT', '8003'),
    ])
