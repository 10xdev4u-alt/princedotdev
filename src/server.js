import { config } from "./config.js";
import { getDb } from "./db.js";
import { createApp } from "./api.js";

getDb(); // ensure schema exists before serving

const app = createApp();
const server = app.listen(config.port, () => {
  const addr = server.address();
  const actualPort = typeof addr === "object" && addr ? addr.port : config.port;
  console.log(`draftdeck listening on http://localhost:${actualPort}`);
  console.log(`  public base URL: ${config.publicBaseUrl}`);
  console.log(`  data dir: ${config.dataDir}`);
  if (!config.sessionSecret) {
    console.log("  (no SESSION_SECRET set — dashboard sign-in is disabled; set it to enable)");
  }
});
