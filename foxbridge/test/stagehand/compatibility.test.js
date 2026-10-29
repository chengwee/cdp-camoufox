import assert from "node:assert/strict";
import { createServer } from "node:http";
import { after, before, test } from "node:test";
import { Stagehand } from "@browserbasehq/stagehand";

const cdpUrl = process.env.FOXBRIDGE_CDP_URL;
if (!cdpUrl) throw new Error("FOXBRIDGE_CDP_URL must point to the matching CDP Camoufox build");
const markup = `<!doctype html><title>Stagehand compatibility</title>
<script>window.fixturePageHelpers = {locale:'th', setLanguage(language) { this.locale=language; return this.locale; }};</script>
<label for="name">Passenger name</label><input id="name">
<label for="class">Ticket class</label><select id="class"><option value="standard">Standard</option><option value="first">First class</option></select>
<button id="decoy" onclick="document.querySelector('#result').textContent='Wrong button'">Cancel booking</button>
<div style="height:1000px"></div>
<button id="submit" onclick="document.querySelector('#result').textContent='Booked for '+document.querySelector('#name').value">Confirm booking</button>
<p id="result">Not booked</p>
<table><tr><th>Route</th><th>Price</th></tr><tr><td>Berlin to Paris</td><td>42 EUR</td></tr></table>`;

let server;
let crossOriginServer;
let fixtureUrl;
let stagehand;
let page;

before(async () => {
  if (!cdpUrl) return;
  const respond = (request, response) => {
    response.setHeader("Content-Type", "text/html");
    if (request.url === "/data.json") {
      response.setHeader("Content-Type", "application/json");
      response.end(JSON.stringify({ route: "Berlin to Paris", price: 42 }));
    } else if (request.url === "/frame") {
      response.end(
        '<!doctype html><title>Frame</title><input id="frame-name"><button onclick="document.querySelector(\'#frame-result\').textContent=document.querySelector(\'#frame-name\').value">Confirm in frame</button><p id="frame-result">Not submitted</p>',
      );
    } else if (request.url === "/frames") {
      response.end(
        `<!doctype html><title>Frame test</title><p>Parent document</p><iframe id="same" src="/frame"></iframe><iframe id="cross" src="http://127.0.0.1:${crossOriginServer.address().port}/frame"></iframe>`,
      );
    } else if (request.url === "/hidden-frames") {
      response.end(
        '<!doctype html><title>Hidden frame test</title><p>Visible parent</p><iframe id="hidden" style="display:none" src="/frame"></iframe>',
      );
    } else if (request.url === "/sandboxed-frames") {
      response.end(
        `<!doctype html><title>Sandboxed frame test</title><iframe id="sandboxed" sandbox="allow-scripts" src="http://127.0.0.1:${crossOriginServer.address().port}/frame"></iframe>`,
      );
    } else if (request.url?.startsWith("/load-fixture")) {
      response.end(
        '<!doctype html><title>Loading fixture</title><iframe src="/frame"></iframe><img src="/slow-image">',
      );
    } else if (request.url === "/slow-image") {
      response.setHeader("Content-Type", "image/svg+xml");
      setTimeout(
        () => response.end('<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>'),
        700,
      );
    } else {
      response.end(markup);
    }
  };
  crossOriginServer = createServer(respond);
  await new Promise((resolve) => crossOriginServer.listen(0, "127.0.0.1", resolve));
  server = createServer(respond);
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  fixtureUrl = `http://127.0.0.1:${server.address().port}/`;
  stagehand = new Stagehand({
    env: "LOCAL",
    disableAPI: true,
    verbose: 0,
    model: { modelName: "openai/gpt-4.1-mini", apiKey: "unused-for-browser-tests" },
    localBrowserLaunchOptions: { cdpUrl },
  });
  await stagehand.init();
  page = await stagehand.context.newPage();
});

after(async () => {
  await page?.close();
  await stagehand?.close();
  server?.closeAllConnections();
  crossOriginServer?.closeAllConnections();
  if (server) await new Promise((resolve) => server.close(resolve));
  if (crossOriginServer) await new Promise((resolve) => crossOriginServer.close(resolve));
});

async function openFixture() {
  await page.goto(fixtureUrl, { waitUntil: "load" });
  await assertEventually(() => page.title(), "Stagehand compatibility");
}

async function assertEventually(read, expected) {
  const deadline = Date.now() + 3000;
  let actual;
  do {
    actual = await read();
    if (actual === expected) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  } while (Date.now() < deadline);
  assert.equal(actual, expected);
}

test(
  "page-world compatibility exposes page helpers and their remote handles",
  { skip: !cdpUrl || process.env.FOXBRIDGE_PAGE_WORLD_EVAL !== "1", timeout: 20000 },
  async () => {
    await openFixture();
    assert.equal(await page.evaluate(() => window.fixturePageHelpers.setLanguage("en")), "en");
    const { result } = await page.sendCDP("Runtime.evaluate", {
      expression: "window.fixturePageHelpers",
      returnByValue: false,
    });
    assert.ok(result.objectId);
    const called = await page.sendCDP("Runtime.callFunctionOn", {
      objectId: result.objectId,
      functionDeclaration: "function() { return this.setLanguage('th'); }",
      returnByValue: true,
    });
    assert.equal(called.result.value, "th");
    await page.sendCDP("Runtime.releaseObject", { objectId: result.objectId });
  },
);

test(
  "a loaded child frame does not finish navigation before the parent loads",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await page.goto(`${fixtureUrl}load-fixture`, { waitUntil: "load" });
    assert.equal(await page.evaluate(() => document.readyState), "complete");
  },
);

test(
  "snapshots map the observed booking control to its real XPath",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const snapshot = await page.snapshot();
    const control = snapshot.formattedTree.match(/\[([^\]]+)\] button: Confirm booking/);
    assert.ok(control, snapshot.formattedTree);
    const selector = snapshot.xpathMap[`0-${control[1]}`] ?? snapshot.xpathMap[control[1]];
    assert.ok(selector, JSON.stringify(snapshot));
    const resolvedId = await page.evaluate(
      (xpath) =>
        document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null)
          .singleNodeValue?.id,
      selector,
    );
    assert.equal(resolvedId, "submit");
  },
);

test(
  "snapshot mapping skips display-none subtrees but keeps referenced names and display-contents controls",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    await page.evaluate(() => {
      document.body.innerHTML =
        '<div style="display:none"><span id="hidden-label">Visible choice</span><button>Hidden choice</button></div><div style="display:contents"><button id="visible-choice" aria-labelledby="hidden-label">Choose</button></div>';
    });
    const snapshot = await page.snapshot();
    assert.ok(!snapshot.formattedTree.includes("Hidden choice"), snapshot.formattedTree);
    const control = snapshot.formattedTree.match(/\[([^\]]+)\] button: Visible choice/);
    assert.ok(control, snapshot.formattedTree);
    const selector = snapshot.xpathMap[`0-${control[1]}`] ?? snapshot.xpathMap[control[1]];
    assert.ok(selector, JSON.stringify(snapshot));
    assert.equal(
      await page.evaluate(
        (xpath) =>
          document.evaluate(xpath, document, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null)
            .singleNodeValue?.id,
        selector,
      ),
      "visible-choice",
    );
  },
);

test(
  "native locator clicks scroll and submit the intended form",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    for (let attempt = 0; attempt < 5; attempt++) {
      await openFixture();
      await page.locator("#name").fill("Camoufox passenger");
      await page.locator("#submit").click();
      await assertEventually(
        () => page.evaluate(() => document.querySelector("#result").textContent),
        "Booked for Camoufox passenger",
      );
    }
  },
);

test(
  "Stagehand acts on a selector produced by its native snapshot",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    await page.locator("#name").fill("Ada");
    const snapshot = await page.snapshot();
    const control = snapshot.formattedTree.match(/\[([^\]]+)\] button: Confirm booking/);
    const key = Object.hasOwn(snapshot.xpathMap, control[1]) ? control[1] : `0-${control[1]}`;
    const result = await stagehand.act(
      {
        selector: `xpath=${snapshot.xpathMap[key]}`,
        description: "Confirm the booking",
        method: "click",
        arguments: [],
      },
      { page },
    );
    assert.equal(result.success, true, result.message);
    await assertEventually(
      () => page.evaluate(() => document.querySelector("#result").textContent),
      "Booked for Ada",
    );
  },
);

test(
  "box models retain layout dimensions and rotated quads",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    await page.evaluate(() => {
      for (const angle of [90, 180]) {
        const box = document.createElement("div");
        box.id = `rotated-${angle}`;
        box.style.cssText = `width:120px;height:40px;box-sizing:border-box;transform:rotate(${angle}deg)`;
        document.body.append(box);
      }
      const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.id = "svg-box";
      svg.setAttribute("width", "120");
      svg.setAttribute("height", "40");
      svg.style.transform = "rotate(90deg)";
      svg.innerHTML = '<rect width="20" height="10" />';
      document.body.append(svg);
    });
    const { root } = await page.sendCDP("DOM.getDocument", {});
    const dimensions = [];
    for (const angle of [90, 180]) {
      const { nodeId } = await page.sendCDP("DOM.querySelector", {
        nodeId: root.nodeId,
        selector: `#rotated-${angle}`,
      });
      const { model } = await page.sendCDP("DOM.getBoxModel", { nodeId });
      const quad = model.content;
      if (angle === 90) {
        assert.ok(Math.abs(quad[2] - quad[0]) < 0.01 && quad[3] - quad[1] > 119);
      } else {
        assert.ok(quad[2] - quad[0] < -119 && Math.abs(quad[3] - quad[1]) < 0.01);
      }
      dimensions.push([model.width, model.height]);
    }
    assert.deepEqual(dimensions, [
      [120, 40],
      [120, 40],
    ]);
    const { nodeId } = await page.sendCDP("DOM.querySelector", {
      nodeId: root.nodeId,
      selector: "#svg-box",
    });
    const { model } = await page.sendCDP("DOM.getBoxModel", { nodeId });
    assert.deepEqual([model.width, model.height], [120, 40]);
    await page.evaluate(() => document.querySelector("#svg-box").replaceChildren());
    const empty = await page.sendCDP("DOM.getBoxModel", { nodeId });
    assert.deepEqual([empty.model.width, empty.model.height], [120, 40]);
    await page.evaluate(() => {
      document.querySelector("#svg-box").style.cssText =
        "width:160px;height:60px;box-sizing:border-box;padding:3px;border:2px solid;transform:rotate(90deg)";
    });
    const styled = await page.sendCDP("DOM.getBoxModel", { nodeId });
    assert.deepEqual([styled.model.width, styled.model.height], [160, 60]);
    await page.evaluate(() => {
      document.querySelector("#svg-box").style.boxSizing = "content-box";
    });
    const padded = await page.sendCDP("DOM.getBoxModel", { nodeId });
    assert.deepEqual([padded.model.width, padded.model.height], [170, 70]);
  },
);

test(
  "invalid DOM node identifiers fail instead of returning another element",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    await assert.rejects(page.sendCDP("DOM.resolveNode", { backendNodeId: 2147483647 }));
  },
);

test(
  "locator indexing and visibility select the intended controls",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const buttons = page.locator("button");
    assert.equal(await buttons.count(), 2);
    assert.equal(await buttons.first().innerText(), "Cancel booking");
    assert.equal(await buttons.nth(1).innerText(), "Confirm booking");
    assert.equal(await buttons.nth(1).isVisible(), true);
  },
);

test(
  "isolated world globals do not leak into the default world",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const { executionContextId } = await page.sendCDP("Page.createIsolatedWorld", {
      frameId: page.mainFrameId(),
      worldName: "fixture-private-world",
    });
    await page.sendCDP("Runtime.evaluate", {
      contextId: executionContextId,
      expression: "globalThis.fixturePrivateValue = 42",
    });
    const result = await page.sendCDP("Runtime.evaluate", {
      expression: "typeof globalThis.fixturePrivateValue",
      returnByValue: true,
    });
    assert.equal(result.result.value, "undefined");
  },
);

test(
  "sandboxed cross-origin frames support expression and function evaluation",
  { timeout: 20000 },
  async () => {
    await page.goto(`${fixtureUrl}sandboxed-frames`, { waitUntil: "load" });
    const frame = await page.frameLocator("#sandboxed").resolveFrame();
    const { executionContextId } = await page.sendCDP("Page.createIsolatedWorld", {
      frameId: frame.frameId,
      worldName: "sandboxed-frame-world",
    });
    const evaluated = await page.sendCDP("Runtime.evaluate", {
      contextId: executionContextId,
      expression: "document.title",
      returnByValue: true,
    });
    assert.equal(evaluated.result.value, "Frame");
    const called = await page.sendCDP("Runtime.callFunctionOn", {
      executionContextId,
      functionDeclaration: "function() { return document.querySelector('#frame-name').tagName; }",
      arguments: [],
      returnByValue: true,
    });
    assert.equal(called.result.value, "INPUT");
    for (const [expression, subtype] of [
      ["({plain: true})", undefined],
      ["new RegExp('frame')", "regexp"],
      [
        "(() => { const original = RegExp; Object.defineProperty(globalThis, 'RegExp', {get() { return original; }, configurable: true}); return new original('accessor'); })()",
        "regexp",
      ],
      ["new Date(0)", "date"],
      ["new Map()", "map"],
      ["new Set()", "set"],
      ["new WeakMap()", "weakmap"],
      ["new WeakSet()", "weakset"],
      ["new TypeError('frame')", "error"],
      ["new Uint8Array(2)", "typedarray"],
    ]) {
      const object = await page.sendCDP("Runtime.evaluate", {
        contextId: executionContextId,
        expression,
        returnByValue: false,
        awaitPromise: false,
      });
      assert.equal(object.result.subtype, subtype, expression);
      await page.sendCDP("Runtime.releaseObject", { objectId: object.result.objectId });
    }
    const snapshot = await page.snapshot();
    assert.ok(snapshot.formattedTree.includes("Confirm in frame"), snapshot.formattedTree);
    await page.evaluate(
      () =>
        new Promise((resolve) => {
          const child = document.querySelector("#sandboxed");
          child.addEventListener("load", resolve, { once: true });
          child.src = "/frame";
        }),
    );
    await assert.rejects(
      page.sendCDP("Runtime.evaluate", {
        contextId: executionContextId,
        expression: "document.title",
        returnByValue: true,
      }),
    );
    const replacement = await page.sendCDP("Page.createIsolatedWorld", {
      frameId: frame.frameId,
      worldName: "sandboxed-frame-world",
    });
    const navigated = await page.sendCDP("Runtime.evaluate", {
      contextId: replacement.executionContextId,
      expression: "location.port",
      returnByValue: true,
    });
    assert.equal(navigated.result.value, new URL(fixtureUrl).port);
  },
);

test(
  "detached elements cannot be resolved through a previous node ID",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const { root } = await page.sendCDP("DOM.getDocument");
    const { nodeId } = await page.sendCDP("DOM.querySelector", {
      nodeId: root.nodeId,
      selector: "#submit",
    });
    await page.evaluate(() => document.querySelector("#submit").remove());
    await assert.rejects(page.sendCDP("DOM.resolveNode", { nodeId }));
  },
);

test(
  "typing, native selection and XPath locators preserve the selected control",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    await page.locator('xpath=//input[@id="name"]').type("Ada");
    assert.equal(await page.locator("#name").inputValue(), "Ada");
    await page.locator("#class").selectOption("first");
    assert.equal(await page.evaluate(() => document.querySelector("#class").value), "first");
  },
);

test(
  "navigation invalidates old node identifiers without affecting the new document",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const { root } = await page.sendCDP("DOM.getDocument", { depth: -1 });
    const { nodeId } = await page.sendCDP("DOM.querySelector", {
      nodeId: root.nodeId,
      selector: "#submit",
    });
    await page.goto(`${fixtureUrl}?next=1`, { waitUntil: "load" });
    await assertEventually(() => page.title(), "Stagehand compatibility");
    await assert.rejects(page.sendCDP("DOM.resolveNode", { nodeId }));
    assert.equal(await page.locator("#name").inputValue(), "");
  },
);

test(
  "cookies and new-document scripts apply to subsequent navigation",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await stagehand.context.addCookies([
      { name: "session", value: "fixture-session", domain: "127.0.0.1", path: "/" },
    ]);
    await page.addInitScript(() => {
      window.fixtureInitialized = true;
    });
    await openFixture();
    assert.equal(await page.evaluate(() => window.fixtureInitialized), true);
    assert.equal(
      await page.evaluate(() => document.cookie.includes("session=fixture-session")),
      true,
    );
    const cookies = await stagehand.context.cookies();
    assert.equal(cookies.find((cookie) => cookie.name === "session")?.value, "fixture-session");
  },
);

test(
  "same-origin and cross-origin frame locators use their own document and coordinates",
  { skip: !cdpUrl, timeout: 30000 },
  async () => {
    await page.goto(`${fixtureUrl}frames`, { waitUntil: "load" });
    await assertEventually(() => page.title(), "Frame test");
    for (const frameId of ["same", "cross"]) {
      const frame = page.frameLocator(`#${frameId}`);
      await frame.locator("#frame-name").fill(frameId);
      await frame.locator("button").click();
      await assertEventually(() => frame.locator("#frame-result").textContent(), frameId);
    }
    assert.equal(
      await page.evaluate(() => document.querySelector("p").textContent),
      "Parent document",
    );
    assert.equal(page.url(), `${fixtureUrl}frames`);
  },
);

test(
  "same-origin and cross-origin frame snapshots resolve their own controls",
  { timeout: 20000 },
  async () => {
    await page.goto(`${fixtureUrl}frames`, { waitUntil: "load" });
    for (const frameId of ["same", "cross"]) {
      const childFrame = await page.frameLocator(`#${frameId}`).resolveFrame();
      const tree = await page.sendCDP("Accessibility.getFullAXTree", {
        frameId: childFrame.frameId,
      });
      assert.ok(!tree.nodes.some((node) => node.name?.value === "Parent document"));
      const control = tree.nodes.find(
        (node) => node.role?.value === "button" && node.name?.value === "Confirm in frame",
      );
      assert.ok(control?.backendDOMNodeId, JSON.stringify(tree));
      const { object } = await page.sendCDP("DOM.resolveNode", {
        backendNodeId: control.backendDOMNodeId,
      });
      const { result } = await page.sendCDP("Runtime.callFunctionOn", {
        objectId: object.objectId,
        functionDeclaration: "function() { return this.ownerDocument.title; }",
        returnByValue: true,
      });
      assert.equal(result.value, "Frame");
      await page.sendCDP("Runtime.releaseObject", { objectId: object.objectId });
    }
  },
);

test("hidden frames return an empty accessibility result", { timeout: 20000 }, async () => {
  await page.goto(`${fixtureUrl}hidden-frames`, { waitUntil: "load" });
  const frame = await page.frameLocator("#hidden").resolveFrame();
  const tree = await page.sendCDP("Accessibility.getFullAXTree", { frameId: frame.frameId });
  assert.deepEqual(tree.nodes, []);
  const snapshot = await page.snapshot();
  assert.ok(snapshot.formattedTree.includes("Visible parent"), snapshot.formattedTree);
});

test(
  "native snapshots keep element IDs and invalidate replaced controls",
  { timeout: 20000 },
  async () => {
    await openFixture();
    const read = async () =>
      (await page.sendCDP("Accessibility.getFullAXTree", {})).nodes.find(
        (node) => node.name?.value === "Confirm booking" && node.role?.value === "button",
      ).backendDOMNodeId;
    const original = await read();
    assert.equal(await read(), original);
    await page.evaluate(() => {
      const button = document.querySelector("#submit");
      button.replaceWith(button.cloneNode(true));
    });
    assert.notEqual(await read(), original);
    await assert.rejects(page.sendCDP("DOM.resolveNode", { backendNodeId: original }));
  },
);

test(
  "CDP network observers receive response bodies for browser fetches",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const session = page.mainFrame().session;
    const responses = [];
    const onResponse = (event) => {
      if (event.response.url.endsWith("/data.json")) responses.push(event);
    };
    session.on("Network.responseReceived", onResponse);
    try {
      await session.send("Network.enable", {
        maxTotalBufferSize: 1000000,
        maxResourceBufferSize: 100000,
      });
      const data = await page.evaluate(async () => (await fetch("/data.json")).json());
      assert.deepEqual(data, { route: "Berlin to Paris", price: 42 });
      await assertEventually(async () => responses.length, 1);
      assert.equal(responses[0].response.mimeType, "application/json");
      assert.equal(responses[0].type, "Fetch");
      assert.ok(responses[0].timestamp > 0);
      const body = await session.send("Network.getResponseBody", {
        requestId: responses[0].requestId,
      });
      assert.equal(body.base64Encoded, false);
      const text = body.base64Encoded ? Buffer.from(body.body, "base64").toString() : body.body;
      assert.deepEqual(JSON.parse(text), { route: "Berlin to Paris", price: 42 });
    } finally {
      session.off("Network.responseReceived", onResponse);
    }
  },
);

test(
  "page-text extraction and screenshots contain actual browser content",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const extracted = await stagehand.extract({ page });
    assert.match(extracted.pageText, /Berlin to Paris/);
    assert.match(extracted.pageText, /42 EUR/);
    const image = await page.screenshot();
    assert.equal(image.subarray(0, 8).toString("hex"), "89504e470d0a1a0a");
    assert.ok(image.length > 1000);
  },
);

test(
  "network recordings preserve the original JSON request body",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const session = page.mainFrame().session;
    const requests = [];
    const onRequest = (event) => {
      if (event.request.method === "POST") requests.push(event.request);
    };
    session.on("Network.requestWillBeSent", onRequest);
    const body = '{"passenger":"Ada","price":42}';
    try {
      await session.send("Network.enable");
      await page.evaluate(
        async (body) =>
          (
            await fetch("/data.json", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body,
            })
          ).json(),
        body,
      );
      await assertEventually(async () => requests.length, 1);
      assert.equal(requests[0].postData, body);
      assert.equal(requests[0].hasPostData, true);
    } finally {
      session.off("Network.requestWillBeSent", onRequest);
    }
  },
);

test(
  "another page cannot resolve a node owned by the first page",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    const { root } = await page.sendCDP("DOM.getDocument");
    const otherPage = await stagehand.context.newPage();
    try {
      await otherPage.goto(fixtureUrl, { waitUntil: "load" });
      await assert.rejects(
        otherPage.sendCDP("DOM.resolveNode", { backendNodeId: root.backendNodeId }),
      );
      assert.equal(await page.title(), "Stagehand compatibility");
      await otherPage.locator("#name").fill("Other passenger");
      await page.locator("#name").fill("Original passenger");
      assert.equal(await otherPage.locator("#name").inputValue(), "Other passenger");
      assert.equal(await page.locator("#name").inputValue(), "Original passenger");
    } finally {
      await otherPage.close();
    }
  },
);

test(
  "identical accessible names keep distinct identities after DOM replacement",
  { skip: !cdpUrl, timeout: 20000 },
  async () => {
    await openFixture();
    await page.evaluate(() => {
      document.body.innerHTML =
        '<button id="first" onclick="document.querySelector(\'#result\').textContent=\'Wrong button\'">Confirm booking</button><button id="second" onclick="document.querySelector(\'#result\').textContent=\'Correct button\'">Confirm booking</button><p id="result">Not booked</p>';
    });
    const snapshot = await page.snapshot();
    const controls = [...snapshot.formattedTree.matchAll(/\[([^\]]+)\] button: Confirm booking/g)];
    assert.equal(controls.length, 2, snapshot.formattedTree);
    const selectors = controls.map(
      (control) => snapshot.xpathMap[control[1]] ?? snapshot.xpathMap[`0-${control[1]}`],
    );
    assert.ok(selectors.every(Boolean), JSON.stringify(snapshot));
    assert.notEqual(selectors[0], selectors[1]);
    const result = await stagehand.act(
      {
        selector: `xpath=${selectors[1]}`,
        description: "Choose the second matching button",
        method: "click",
        arguments: [],
      },
      { page },
    );
    assert.equal(result.success, true, result.message);
    await assertEventually(
      () => page.evaluate(() => document.querySelector("#result").textContent),
      "Correct button",
    );
  },
);
