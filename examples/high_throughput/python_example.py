#!/usr/bin/env python3
"""
High-throughput usage example for the Circle Wallet Service.

The server processes transactions FIFO per sender but in parallel across
senders. Using M wallets gives ~M× throughput.

Usage:
    export API_KEY=your-bearer-token
    export WALLETS='[{"wallet_id":"w1","address":"0xabc..."},{"wallet_id":"w2","address":"0xdef..."}]'
    pip install aiohttp
    python examples/high_throughput/python_example.py

Requirements: aiohttp (pip install aiohttp)
"""

import asyncio
import json
import os
import sys
import time
import uuid
from dataclasses import dataclass, field

try:
    import aiohttp
except ImportError:
    sys.exit("aiohttp is required: pip install aiohttp")

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

@dataclass
class Wallet:
    wallet_id: str
    address: str

@dataclass
class Config:
    base_url: str
    api_key: str
    wallets: list[Wallet]
    txn_count: int


def load_config() -> Config:
    base_url = os.getenv("API_BASE_URL", "http://localhost:8080")
    api_key = os.getenv("API_KEY", "")
    if not api_key:
        sys.exit("API_KEY is required")

    raw = os.getenv("WALLETS", "")
    if not raw:
        sys.exit("WALLETS is required (JSON array of {wallet_id, address})")
    wallets = [Wallet(**w) for w in json.loads(raw)]
    if not wallets:
        sys.exit("WALLETS must contain at least one wallet")

    txn_count = int(os.getenv("TXN_COUNT", "20"))
    return Config(base_url=base_url, api_key=api_key, wallets=wallets, txn_count=txn_count)

# ---------------------------------------------------------------------------
# Submission
# ---------------------------------------------------------------------------

@dataclass
class SubmitResult:
    transaction_id: str
    status: str
    idempotency_key: str = ""

SEMAPHORE = asyncio.Semaphore(50)


async def submit_one(
    session: aiohttp.ClientSession,
    cfg: Config,
    idx: int,
    wallet: Wallet,
    counter: dict,
) -> SubmitResult | None:
    idemp_key = str(uuid.uuid4())
    body = {
        "wallet_id": wallet.wallet_id,
        "address": wallet.address,
        "function_id": "0x1::aptos_account::transfer",
        "arguments": [wallet.address, "1"],
        "idempotency_key": idemp_key,
    }

    async with SEMAPHORE:
        async with session.post(f"{cfg.base_url}/v1/execute", json=body) as resp:
            if resp.status == 429:
                retry_after = float(resp.headers.get("Retry-After", "1"))
                print(f"[submit {idx}] 429 — backing off {retry_after}s")
                await asyncio.sleep(retry_after)
                return None

            if resp.status != 202:
                text = await resp.text()
                print(f"[submit {idx}] unexpected {resp.status}: {text}")
                return None

            data = await resp.json()
            counter["n"] += 1
            if counter["n"] % 50 == 0 or counter["n"] == cfg.txn_count:
                print(f"submitted {counter['n']} / {cfg.txn_count}")
            return SubmitResult(
                transaction_id=data["transaction_id"],
                status=data["status"],
                idempotency_key=idemp_key,
            )


async def submit_all(session: aiohttp.ClientSession, cfg: Config) -> list[SubmitResult]:
    counter: dict = {"n": 0}
    tasks = [
        submit_one(session, cfg, i, cfg.wallets[i % len(cfg.wallets)], counter)
        for i in range(cfg.txn_count)
    ]
    results = await asyncio.gather(*tasks)
    return [r for r in results if r is not None]

# ---------------------------------------------------------------------------
# Polling with exponential backoff
# ---------------------------------------------------------------------------

async def poll_one(
    session: aiohttp.ClientSession,
    cfg: Config,
    tx_id: str,
    confirmed: dict,
    failed: dict,
) -> None:
    backoff = 0.2
    max_backoff = 5.0

    while True:
        async with SEMAPHORE:
            async with session.get(f"{cfg.base_url}/v1/transactions/{tx_id}") as resp:
                if resp.status == 429:
                    await asyncio.sleep(backoff)
                    backoff = min(backoff * 2, max_backoff)
                    continue
                data = await resp.json()

        status = data.get("status", "")
        if status == "confirmed":
            confirmed["n"] += 1
            return
        if status in ("failed", "expired"):
            failed["n"] += 1
            print(f"[poll {tx_id[:8]}] terminal: {status}")
            return

        await asyncio.sleep(backoff)
        backoff = min(backoff * 2, max_backoff)


async def poll_all(session: aiohttp.ClientSession, cfg: Config, txns: list[SubmitResult]) -> tuple[int, int]:
    confirmed: dict = {"n": 0}
    failed: dict = {"n": 0}
    await asyncio.gather(*(poll_one(session, cfg, tx.transaction_id, confirmed, failed) for tx in txns))
    return confirmed["n"], failed["n"]

# ---------------------------------------------------------------------------
# Idempotency demo
# ---------------------------------------------------------------------------

async def demo_idempotency(session: aiohttp.ClientSession, cfg: Config) -> None:
    w = cfg.wallets[0]
    idemp_key = f"demo-idemp-{uuid.uuid4()}"
    body = {
        "wallet_id": w.wallet_id,
        "address": w.address,
        "function_id": "0x1::aptos_account::transfer",
        "arguments": [w.address, "1"],
        "idempotency_key": idemp_key,
    }
    print("--- idempotency demo: submitting same key twice ---")

    async with session.post(f"{cfg.base_url}/v1/execute", json=body) as r1:
        print(f"  1st call: {r1.status} {await r1.text()}")
    async with session.post(f"{cfg.base_url}/v1/execute", json=body) as r2:
        print(f"  2nd call: {r2.status} {await r2.text()} (same transaction_id)")

# ---------------------------------------------------------------------------
# Webhook listener (uncomment to use instead of polling)
# ---------------------------------------------------------------------------
#
# To receive completions via webhook instead of polling, run a small server:
#
#   pip install fastapi uvicorn
#
#   # webhook_server.py
#   from fastapi import FastAPI, Request
#   app = FastAPI()
#
#   @app.post("/webhook")
#   async def webhook(request: Request):
#       body = await request.json()
#       print(f"[webhook] {body}")
#       return {"ok": True}
#
#   # uvicorn webhook_server:app --port 9090
#
# Then pass "webhook_url": "http://your-host:9090/webhook" in the execute
# request. The server will POST the final transaction status there instead
# of requiring you to poll.

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

async def main() -> None:
    cfg = load_config()
    print(f"Circle Wallet Service — high-throughput example (Python)")
    print(f"base_url={cfg.base_url}  wallets={len(cfg.wallets)}  txn_count={cfg.txn_count}")

    headers = {"Authorization": f"Bearer {cfg.api_key}"}
    async with aiohttp.ClientSession(headers=headers) as session:
        await demo_idempotency(session, cfg)

        t0 = time.monotonic()

        print("\n--- submitting ---")
        txns = await submit_all(session, cfg)
        submit_elapsed = time.monotonic() - t0
        print(f"submission: {len(txns)} txns in {submit_elapsed:.2f}s "
              f"({len(txns)/max(submit_elapsed,0.001):.1f} txn/s)")

        if not txns:
            sys.exit("no transactions submitted")

        print("\n--- polling ---")
        poll_start = time.monotonic()
        confirmed, failed = await poll_all(session, cfg, txns)
        poll_elapsed = time.monotonic() - poll_start

        total = time.monotonic() - t0
        print(f"\npolling: {confirmed} confirmed, {failed} failed/expired in {poll_elapsed:.2f}s")
        print("=========================================")
        print(f"wallets:        {len(cfg.wallets)}")
        print(f"transactions:   {len(txns)}")
        print(f"total time:     {total:.2f}s")
        if total > 0:
            print(f"throughput:     {len(txns)/total:.1f} txn/s (submit → confirm)")
        print(f"expected scale: ~{min(len(cfg.wallets), len(txns))}× throughput with {len(cfg.wallets)} wallets")
        print("=========================================")


if __name__ == "__main__":
    asyncio.run(main())
