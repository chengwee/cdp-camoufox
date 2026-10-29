# Stagehand browser compatibility

This suite exercises the unmodified `@browserbasehq/stagehand@3.4.0` package against a CDP endpoint. It uses local HTTP fixtures and does not need model credentials.

## Run

From `foxbridge/`, start Foxbridge with a browser built from the same CDP Camoufox checkout:

```sh
go build -o .tmp/foxbridge ./cmd/foxbridge
.tmp/foxbridge --binary /path/to/Camoufox.app/Contents/MacOS/camoufox --headless --port 19322 --page-world-eval
```

In another terminal:

```sh
cd test/stagehand
bun install --frozen-lockfile
FOXBRIDGE_CDP_URL=ws://127.0.0.1:19322/devtools/browser/foxbridge \
FOXBRIDGE_PAGE_WORLD_EVAL=1 bun run test
```

For a Chromium reference run, supply Chromium's browser WebSocket URL from `/json/version`. The suite fails if no endpoint is supplied.

`--page-world-eval` is an opt-in setting for Camoufox with the Juggler backend. It lets default-world evaluations access the page's own JavaScript helpers and return their object handles, as Chrome does. Scripts in that world become visible to the website. Named isolated worlds remain separate. Without this flag, Foxbridge preserves Camoufox's default sandbox behavior. Inherited Camoufox configuration is preserved, except `disableWorldIsolation` is set to `true` when this option is enabled. The option is rejected with the BiDi backend.

`FOXBRIDGE_PAGE_WORLD_EVAL=1` enables the page-helper and remote-handle regression check. Set it only when the endpoint provides page-world evaluation, including Chromium.

## Supported milestone

The fixtures cover real snapshot-to-XPath mapping, action-object execution through `act`, native offscreen clicks, typing and selection, locator indexing, isolated worlds, stale and detached nodes, cookies, initialization scripts, same-origin and cross-origin frame locator actions, response recording and body retrieval, page-text extraction, screenshots, and page isolation.

The target browser is the custom CDP Camoufox build based on 156.0.1-beta.36. Stagehand is unchanged. The bridge holds DOM registries as remote objects rather than installing globals in page scripts. Official Camoufox releases lack the native DOM identity extension and fail accessibility requests with an explicit matching-build error.

## Limits

Completed network request metadata is retained for the most recent 1,024 requests across the bridge. Older entries lose response-body access; in-flight requests remain tracked. Failed requests and bodies reported as evicted by Camoufox are removed immediately.

- Same-origin and cross-origin frame snapshots return the selected frame's accessibility tree and resolvable element identities. Each frame is queried separately; a parent snapshot omits child-document descendants.
- These tests do not establish natural-language `act`, `observe`, structured model-based `extract`, or DOM-agent compatibility. Those still require real model-backed validation. The action-object test and the no-model page-text extraction overload do not substitute for those checks.
- A single native accessibility query returns the tree and a temporary batch of actual DOM node handles. Foxbridge registers that batch in one runtime call and releases it, preserving the identities used by its existing DOM APIs. No role/name matching or per-element accessibility scan is used. Native anonymous nodes and inaccessible closed shadow roots do not receive page DOM handles.
- The native tree and node batch are captured together. Navigation during capture fails rather than returning a mixed document. Native requests retain their backend timeout.
- Full orchestrator workflows, advanced network behavior, profile persistence, screenshot masking, visual agents, and Stagehand v4 are outside this milestone's verified scope.

The original Go and Puppeteer suites remain regression gates. Puppeteer accepts the same `FOXBRIDGE_CDP_URL` override.

The deterministic Puppeteer checks can run without external websites:

```sh
FOXBRIDGE_CDP_URL=ws://127.0.0.1:19322/devtools/browser/foxbridge \
node ../puppeteer/test-local.js
```

The older comprehensive Puppeteer suite assumes that `example.com` contains an `<h1>` and that browser contexts and emulation are permitted. The live example page no longer has that heading, and this fork deliberately rejects the latter operations in ownership mode. Those checks are not green; the local fixtures do not claim those capabilities.

## Existing orchestrator adapter

An optional smoke test imports the existing orchestrator adapter directly from a checkout. It navigates only to local fixtures, performs no model inference, and verifies page capture, state export, and the recorded JSON response body. It does not change that checkout or Stagehand.

```sh
ORCHESTRATOR_DIRECTORY=/path/to/adapter-checkout \
FOXBRIDGE_CDP_URL=http://127.0.0.1:19322 bun run test:orchestrator
```

The smoke test requires the adapter checkout's dependencies to be installed and uses Bun to load its TypeScript entry points.
