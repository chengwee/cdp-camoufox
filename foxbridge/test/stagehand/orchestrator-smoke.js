import assert from "node:assert/strict";
import { createServer } from "node:http";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const directory = process.env.ORCHESTRATOR_DIRECTORY;
if (!directory)
  throw new Error("Set ORCHESTRATOR_DIRECTORY to the unchanged orchestrator checkout");
process.env.OPENAI_API_KEY ??= "unused-for-browser-smoke-test";
process.env.NETWORK_CAPTURE_ENABLED = "true";
process.env.NETWORK_CAPTURE_BODIES = "api";

const { createStagehandBrowserAgent } = await import(
  pathToFileURL(resolve(directory, "src/providerRuntime/browserAgent/stagehand.ts")).href
);
const { default: resolveCdpUrl } = await import(
  pathToFileURL(resolve(directory, "scripts/_cdpUrl.ts")).href
);
const server = createServer((request, response) => {
  if (request.url === "/api/data.json") {
    response.setHeader("Content-Type", "application/json");
    const timer = setTimeout(() => {
      response.end(JSON.stringify({ route: "Berlin to Paris", price: 42 }));
    }, 250);
    response.on("close", () => clearTimeout(timer));
  } else {
    response.setHeader("Content-Type", "text/html");
    response.end(
      '<!doctype html><title>Orchestrator fixture</title><h1>Browser adapter proof</h1><button>Confirm booking</button><script>fetch("/api/data.json").then(r=>r.json()).then(d=>document.body.dataset.price=d.price)</script>',
    );
  }
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
let session;
try {
  const cdpUrl = await resolveCdpUrl(process.env.FOXBRIDGE_CDP_URL ?? "http://127.0.0.1:19322");
  session = await createStagehandBrowserAgent().open({
    mode: "headless",
    model: "openai/gpt-5.6-luna",
    cdpUrl,
  });
  const fixtureUrl = `http://127.0.0.1:${server.address().port}/`;
  await session.navigate(fixtureUrl, { timeoutMs: 10000, waitUntil: "load" });
  const capture = await session.capturePage({ mode: "full", timeoutMs: 10000 });
  assert.equal(capture.url, fixtureUrl);
  assert.match(JSON.stringify(capture), /Browser adapter proof/);
  const state = await session.exportStorageState();
  assert.ok(Array.isArray(state.cookies));
  const deadline = Date.now() + 10000;
  let price;
  do {
    price = await session.evaluate("document.body.dataset.price");
    if (price === "42") break;
    await new Promise((resolve) => setTimeout(resolve, 50));
  } while (Date.now() < deadline);
  assert.equal(price, "42", "timed out waiting for the fixture API response");
  const network = await session.exportNetworkLog();
  const api = network.document.log.entries.find((entry) =>
    entry.request.url.endsWith("/api/data.json"),
  );
  assert.ok(api, "recorder did not capture the browser's API request");
  assert.equal(api.response.content.mimeType, "application/json");
  assert.deepEqual(JSON.parse(api.response.content.text), { route: "Berlin to Paris", price: 42 });
  assert.ok(new Date(api.startedDateTime).getFullYear() > 2020);
  console.log(
    "Orchestrator smoke passed: CDP attachment, navigation, capture, storage export, and recorded JSON body.",
  );
} finally {
  await session?.close();
  await session?.close();
  server.closeAllConnections();
  await new Promise((resolve) => server.close(resolve));
}
