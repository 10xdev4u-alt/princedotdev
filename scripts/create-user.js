#!/usr/bin/env node
// Creates an account (optionally with an email so a team owner can add them)
// and prints a fresh API key. Run: npm run user:create -- --name "Maya" --email maya@team.dev
import { parseArgs } from "node:util";
import { getDb, createAccount, createApiKey } from "../src/db.js";

const { values } = parseArgs({
  options: {
    name: { type: "string" },
    email: { type: "string" }
  }
});

getDb();

const name = values.name || "New User";
const accountId = createAccount(name, values.email || null);
const { id, token } = createApiKey(accountId, "bootstrap");

console.log(`Account created: ${name} (${accountId})${values.email ? ` · ${values.email}` : ""}`);
console.log(`API key id: ${id}`);
console.log(`API key (shown once): ${token}`);
console.log("");
console.log("Save it on any machine/agent with:");
console.log(`  npx draftdeck auth set ${token}`);
