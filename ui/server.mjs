// HTTPS front for the production build: the console is served over TLS only (MED_UI_CERT / MED_UI_KEY).
import { readFileSync } from "node:fs";
import { createServer } from "node:https";
import next from "next";

const port = Number(process.env.MED_UI_PORT || 8090);
const host = process.env.MED_UI_HOST || "127.0.0.1";
const cert = process.env.MED_UI_CERT;
const key = process.env.MED_UI_KEY;
if (!cert || !key) {
  console.error("MED_UI_CERT and MED_UI_KEY are required");
  process.exit(1);
}

const app = next({ dev: false, hostname: host, port });
const handle = app.getRequestHandler();
await app.prepare();
createServer({ cert: readFileSync(cert), key: readFileSync(key), minVersion: "TLSv1.2" }, (req, res) => handle(req, res)).listen(
  port,
  host,
  () => console.log(JSON.stringify({ level: "INFO", component: "med-ui", msg: "listening", url: `https://${host}:${port}` })),
);
