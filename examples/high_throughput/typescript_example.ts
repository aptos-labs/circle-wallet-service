#!/usr/bin/env npx tsx
/**
 * High-throughput usage example for the Circle Wallet Service.
 *
 * The server processes transactions FIFO per sender but in parallel across
 * senders. Using M wallets gives ~M× throughput.
 *
 * Usage:
 *   export API_KEY=your-bearer-token
 *   export WALLETS='[{"wallet_id":"w1","address":"0xabc..."},{"wallet_id":"w2","address":"0xdef..."}]'
 *   npx tsx examples/high_throughput/typescript_example.ts
 *
 * Requirements: Node 18+ (native fetch)
 */

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

interface Wallet {
  wallet_id: string;
  address: string;
}

interface Config {
  baseUrl: string;
  apiKey: string;
  wallets: Wallet[];
  txnCount: number;
}

function loadConfig(): Config {
  const baseUrl = process.env.API_BASE_URL ?? "http://localhost:8080";
  const apiKey = process.env.API_KEY ?? "";
  if (!apiKey) throw new Error("API_KEY is required");

  const raw = process.env.WALLETS ?? "";
  if (!raw) throw new Error("WALLETS is required (JSON array of {wallet_id, address})");
  const wallets: Wallet[] = JSON.parse(raw);
  if (!wallets.length) throw new Error("WALLETS must contain at least one wallet");

  const txnCount = parseInt(process.env.TXN_COUNT ?? "20", 10);
  return { baseUrl, apiKey, wallets, txnCount };
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

async function apiPost(cfg: Config, path: string, body: unknown): Promise<{ status: number; data: any }> {
  const resp = await fetch(`${cfg.baseUrl}${path}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${cfg.apiKey}`,
    },
    body: JSON.stringify(body),
  });
  const data = await resp.json().catch(() => null);
  return { status: resp.status, data };
}

async function apiGet(cfg: Config, path: string): Promise<{ status: number; data: any }> {
  const resp = await fetch(`${cfg.baseUrl}${path}`, {
    headers: { Authorization: `Bearer ${cfg.apiKey}` },
  });
  const data = await resp.json().catch(() => null);
  return { status: resp.status, data };
}

// ---------------------------------------------------------------------------
// Submission with concurrency limiter
// ---------------------------------------------------------------------------

interface SubmitResult {
  transactionId: string;
  status: string;
  idempotencyKey: string;
}

async function withSemaphore<T>(sem: { count: number; max: number; queue: (() => void)[] }, fn: () => Promise<T>): Promise<T> {
  if (sem.count >= sem.max) {
    await new Promise<void>((resolve) => sem.queue.push(resolve));
  }
  sem.count++;
  try {
    return await fn();
  } finally {
    sem.count--;
    sem.queue.shift()?.();
  }
}

async function submitAll(cfg: Config): Promise<SubmitResult[]> {
  const sem = { count: 0, max: 50, queue: [] as (() => void)[] };
  let submitted = 0;

  const tasks = Array.from({ length: cfg.txnCount }, (_, i) => {
    const wallet = cfg.wallets[i % cfg.wallets.length];
    const idempotencyKey = crypto.randomUUID();

    return withSemaphore(sem, async (): Promise<SubmitResult | null> => {
      const { status, data } = await apiPost(cfg, "/v1/execute", {
        wallet_id: wallet.wallet_id,
        address: wallet.address,
        function_id: "0x1::aptos_account::transfer",
        arguments: [wallet.address, "1"],
        idempotency_key: idempotencyKey,
      });

      if (status === 429) {
        console.log(`[submit ${i}] 429 — back off and retry`);
        return null;
      }
      if (status !== 202) {
        console.log(`[submit ${i}] unexpected ${status}: ${JSON.stringify(data)}`);
        return null;
      }

      submitted++;
      if (submitted % 50 === 0 || submitted === cfg.txnCount) {
        console.log(`submitted ${submitted} / ${cfg.txnCount}`);
      }

      return {
        transactionId: data.transaction_id,
        status: data.status,
        idempotencyKey,
      };
    });
  });

  const results = await Promise.all(tasks);
  return results.filter((r): r is SubmitResult => r !== null);
}

// ---------------------------------------------------------------------------
// Polling with exponential backoff
// ---------------------------------------------------------------------------

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function pollOne(
  cfg: Config,
  txId: string,
  stats: { confirmed: number; failed: number },
  sem: { count: number; max: number; queue: (() => void)[] },
): Promise<void> {
  let backoff = 200;
  const maxBackoff = 5000;

  while (true) {
    const { data } = await withSemaphore(sem, () => apiGet(cfg, `/v1/transactions/${txId}`));
    const status = data?.status;

    if (status === "confirmed") {
      stats.confirmed++;
      return;
    }
    if (status === "failed" || status === "expired") {
      stats.failed++;
      console.log(`[poll ${txId.slice(0, 8)}] terminal: ${status}`);
      return;
    }

    const jitter = Math.random() * backoff * 0.25;
    await sleep(backoff + jitter);
    backoff = Math.min(backoff * 2, maxBackoff);
  }
}

async function pollAll(cfg: Config, txns: SubmitResult[]): Promise<{ confirmed: number; failed: number }> {
  const sem = { count: 0, max: 50, queue: [] as (() => void)[] };
  const stats = { confirmed: 0, failed: 0 };
  await Promise.all(txns.map((tx) => pollOne(cfg, tx.transactionId, stats, sem)));
  return stats;
}

// ---------------------------------------------------------------------------
// Idempotency demo
// ---------------------------------------------------------------------------

async function demoIdempotency(cfg: Config): Promise<void> {
  const wallet = cfg.wallets[0];
  const idempotencyKey = `demo-idemp-${crypto.randomUUID()}`;
  const body = {
    wallet_id: wallet.wallet_id,
    address: wallet.address,
    function_id: "0x1::aptos_account::transfer",
    arguments: [wallet.address, "1"],
    idempotency_key: idempotencyKey,
  };

  console.log("--- idempotency demo: submitting same key twice ---");

  const r1 = await apiPost(cfg, "/v1/execute", body);
  console.log(`  1st call: ${r1.status} ${JSON.stringify(r1.data)}`);

  const r2 = await apiPost(cfg, "/v1/execute", body);
  console.log(`  2nd call: ${r2.status} ${JSON.stringify(r2.data)} (same transaction_id)`);
}

// ---------------------------------------------------------------------------
// Webhook listener (Express snippet — uncomment to use instead of polling)
// ---------------------------------------------------------------------------
//
// import express from "express";
//
// const app = express();
// app.use(express.json());
//
// app.post("/webhook", (req, res) => {
//   console.log("[webhook]", req.body);
//   res.sendStatus(200);
// });
//
// app.listen(9090, () => console.log("webhook listener on :9090/webhook"));
//
// Then pass "webhook_url": "http://your-host:9090/webhook" in the execute
// request. The server will POST the final transaction status there instead
// of requiring you to poll.

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main(): Promise<void> {
  const cfg = loadConfig();
  console.log(`Circle Wallet Service — high-throughput example (TypeScript)`);
  console.log(`base_url=${cfg.baseUrl}  wallets=${cfg.wallets.length}  txn_count=${cfg.txnCount}`);

  await demoIdempotency(cfg);

  const t0 = performance.now();

  console.log("\n--- submitting ---");
  const txns = await submitAll(cfg);
  const submitMs = performance.now() - t0;
  console.log(
    `submission: ${txns.length} txns in ${(submitMs / 1000).toFixed(2)}s ` +
      `(${(txns.length / (submitMs / 1000)).toFixed(1)} txn/s)`,
  );

  if (!txns.length) throw new Error("no transactions submitted");

  console.log("\n--- polling ---");
  const pollStart = performance.now();
  const { confirmed, failed } = await pollAll(cfg, txns);
  const pollMs = performance.now() - pollStart;

  const totalMs = performance.now() - t0;
  console.log(`\npolling: ${confirmed} confirmed, ${failed} failed/expired in ${(pollMs / 1000).toFixed(2)}s`);
  console.log("=========================================");
  console.log(`wallets:        ${cfg.wallets.length}`);
  console.log(`transactions:   ${txns.length}`);
  console.log(`total time:     ${(totalMs / 1000).toFixed(2)}s`);
  if (totalMs > 0) {
    console.log(`throughput:     ${(txns.length / (totalMs / 1000)).toFixed(1)} txn/s (submit → confirm)`);
  }
  console.log(`expected scale: ~${Math.min(cfg.wallets.length, txns.length)}× throughput with ${cfg.wallets.length} wallets`);
  console.log("=========================================");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
