/**
 * register-entity-secret.ts — One-time setup: generate and register a Circle entity secret.
 *
 * Run this ONCE when setting up a new Circle account. It:
 *   1. Generates a cryptographically random 32-byte entity secret
 *   2. Registers it with Circle (required before creating any wallets)
 *   3. Saves a recovery file (keep this safe — you need it to recover access)
 *   4. Prints the value to add to your .env
 *
 * Required env vars:
 *   CIRCLE_API_KEY  — from console.circle.com
 *
 * Usage:
 *   node --env-file=.env --import=tsx register-entity-secret.ts
 */

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { registerEntitySecretCiphertext } from "@circle-fin/developer-controlled-wallets";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const OUTPUT_DIR = path.join(__dirname, "output");

async function main() {
  const apiKey = process.env.CIRCLE_API_KEY;
  if (!apiKey) {
    throw new Error("CIRCLE_API_KEY is required");
  }

  fs.mkdirSync(OUTPUT_DIR, { recursive: true });

  // Generate a random 32-byte entity secret.
  const entitySecret = crypto.randomBytes(32).toString("hex");

  console.log("Registering entity secret with Circle...");
  await registerEntitySecretCiphertext({
    apiKey,
    entitySecret,
    recoveryFileDownloadPath: OUTPUT_DIR,
  });

  console.log("");
  console.log("✓ Registered. Recovery file saved to:", OUTPUT_DIR);
  console.log("");
  console.log("Add this to your .env:");
  console.log(`  CIRCLE_ENTITY_SECRET=${entitySecret}`);
  console.log("");
  console.log(
    "Keep the recovery file safe — you need it if you lose the entity secret.",
  );
}

main().catch((err) => {
  console.error("Error:", err.message || err);
  process.exit(1);
});
