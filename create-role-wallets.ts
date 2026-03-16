/**
 * create-role-wallets.ts — Create Circle dev-controlled wallets for all Contract Integration roles.
 *
 * Creates one wallet per role (master_minter, minter, denylister, metadata_updater)
 * in a single wallet set, then prints the env vars to add to your .env file.
 *
 * Note: The owner role requires a local Aptos key for contract deployment (the Aptos
 * CLI needs a private key to publish packages). Create the owner key separately with:
 *   aptos key generate --output-file owner.key
 *
 * Required env vars:
 *   CIRCLE_API_KEY         — from console.circle.com
 *   CIRCLE_ENTITY_SECRET   — 32-byte hex secret (from entity secret registration)
 *
 * Usage:
 *   node --env-file=.env --import=tsx create-role-wallets.ts
 */

import {
  initiateDeveloperControlledWalletsClient,
} from "@circle-fin/developer-controlled-wallets";

const ROLES = ["master_minter", "minter", "denylister", "metadata_updater"] as const;
type Role = typeof ROLES[number];

const ENV_VAR: Record<Role, { walletId: string; address: string; publicKey: string }> = {
  master_minter:    { walletId: "MASTER_MINTER_WALLET_ID",    address: "MASTER_MINTER_ADDRESS",    publicKey: "MASTER_MINTER_PUBLIC_KEY" },
  minter:           { walletId: "MINTER_WALLET_ID",           address: "MINTER_ADDRESS",           publicKey: "MINTER_PUBLIC_KEY" },
  denylister:       { walletId: "DENYLISTER_WALLET_ID",       address: "DENYLISTER_ADDRESS",        publicKey: "DENYLISTER_PUBLIC_KEY" },
  metadata_updater: { walletId: "METADATA_UPDATER_WALLET_ID", address: "METADATA_UPDATER_ADDRESS", publicKey: "METADATA_UPDATER_PUBLIC_KEY" },
};

async function main() {
  const apiKey = process.env.CIRCLE_API_KEY;
  const entitySecret = process.env.CIRCLE_ENTITY_SECRET;
  if (!apiKey)     throw new Error("CIRCLE_API_KEY is required");
  if (!entitySecret) throw new Error("CIRCLE_ENTITY_SECRET is required");

  const client = initiateDeveloperControlledWalletsClient({ apiKey, entitySecret });

  // Create a wallet set for all roles
  console.log("Creating wallet set...");
  const walletSet = (await client.createWalletSet({ name: "Contract Integration Testnet Roles" })).data?.walletSet;
  if (!walletSet?.id) throw new Error("Wallet set creation failed");
  console.log("Wallet Set ID:", walletSet.id);

  // Create one wallet per role
  console.log("\nCreating wallets for all roles...");
  const results: Record<string, { walletId: string; address: string; publicKey: string }> = {};

  for (const role of ROLES) {
    process.stdout.write(`  ${role}... `);
    const wallet = (
      await client.createWallets({
        walletSetId: walletSet.id,
        blockchains: ["APTOS-TESTNET"],
        count: 1,
        accountType: "EOA",
      })
    ).data?.wallets?.[0];

    if (!wallet) throw new Error(`Wallet creation failed for ${role}`);

    results[role] = {
      walletId: wallet.id,
      address: wallet.address,
      publicKey: wallet.initialPublicKey ?? "",
    };
    console.log(`✓ ${wallet.address}`);
  }

  // Print env var block
  console.log("\n# ── Add these to your .env ──────────────────────────────────");
  console.log(`SIGNER_PROVIDER=circle`);
  console.log(`CIRCLE_API_KEY=${apiKey}`);
  console.log(`# Set CIRCLE_ENTITY_SECRET_CIPHERTEXT from your entity secret registration`);
  console.log();
  for (const role of ROLES) {
    const r = results[role];
    const v = ENV_VAR[role as Role];
    console.log(`${v.walletId}=${r.walletId}`);
    console.log(`${v.address}=${r.address}`);
    if (r.publicKey) {
      console.log(`# ${v.publicKey}=${r.publicKey}  # auto-fetched at startup; set manually to skip the API call`);
    }
    console.log();
  }
  console.log("# ─────────────────────────────────────────────────────────────");
  console.log("\nDone! Fund each wallet address with testnet APT before starting the server.");
  console.log("Testnet faucet: https://aptos.dev/en/network/faucet");
}

main().catch((err) => {
  console.error("Error:", err.message || err);
  process.exit(1);
});
