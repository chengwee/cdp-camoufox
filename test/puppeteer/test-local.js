const assert = require("node:assert/strict");
const { createServer } = require("node:http");
const puppeteer = require("puppeteer-core");

async function main() {
  const server = createServer((_request, response) => {
    response.setHeader("Content-Type", "text/html");
    response.end(
      '<!doctype html><title>Local regression</title><h1>Local heading</h1><a href="/one">One</a><a href="/two">Two</a>',
    );
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  let browser;
  let page;
  try {
    browser = await puppeteer.connect({
      browserWSEndpoint:
        process.env.FOXBRIDGE_CDP_URL || "ws://127.0.0.1:9222/devtools/browser/foxbridge",
      defaultViewport: null,
    });
    page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${server.address().port}/`, {
      waitUntil: "load",
      timeout: 15000,
    });
    assert.equal(await page.title(), "Local regression");
    assert.match(await page.content(), /<h1>Local heading<\/h1>/);
    assert.equal(await page.$eval("h1", (node) => node.textContent), "Local heading");
    assert.deepEqual(await page.$$eval("a", (nodes) => nodes.map((node) => node.textContent)), [
      "One",
      "Two",
    ]);
    const heading = await page.$("h1");
    assert.equal(await heading.evaluate((node) => node.textContent), "Local heading");
    const links = await page.$$("a");
    assert.equal(links.length, 2);
    console.log("Local Puppeteer regression checks passed.");
  } finally {
    await page?.close();
    await browser?.disconnect();
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
