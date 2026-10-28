# Stagehand browser compatibility

This suite exercises the unmodified `@browserbasehq/stagehand@3.4.0` package against a CDP endpoint. It uses local HTTP fixtures and does not need model credentials.

## Run

Start Foxbridge with a compatible Camoufox binary:

```sh
go build -o .tmp/foxbridge ./cmd/foxbridge
.tmp/foxbridge --binary /path/to/Camoufox.app/Contents/MacOS/camoufox --headless --port 19322 --page-world-eval
```

In another terminal:

```sh
cd test/stagehand
bun install --frozen-lockfile
FOXBRIDGE_CDP_URL=ws://127.0.0.1:19322/devtools/browser/foxbridge \
FOXBRIDGE_PAGE_WORLD_EVAL=1 \
FOXBRIDGE_EXPECT_FRAME_AX_UNSUPPORTED=1 bun run test
```

`FOXBRIDGE_EXPECT_FRAME_AX_UNSUPPORTED=1` enables a negative test for the documented child-frame accessibility limitation. For a Chromium reference run, omit it and supply Chromium's browser WebSocket URL from `/json/version`. The suite skips browser checks if no endpoint is supplied; skipped checks are not a compatibility pass.

`--page-world-eval` is an opt-in setting for Camoufox with the Juggler backend. It lets default-world evaluations access the page's own JavaScript helpers and return their object handles, as Chrome does. ATLAS needs this for Thai Rail language switching. Scripts in that world become visible to the website. Named isolated worlds remain separate. Without this flag, Foxbridge preserves Camoufox's default sandbox behavior. Inherited Camoufox configuration is preserved, except `disableWorldIsolation` is set to `true` when this option is enabled. The option is rejected with the BiDi backend.

`FOXBRIDGE_PAGE_WORLD_EVAL=1` enables the page-helper and remote-handle regression check. Set it only when the endpoint provides page-world evaluation, including Chromium.

## Supported milestone

The fixtures cover real snapshot-to-XPath mapping, action-object execution through `act`, native offscreen clicks, typing and selection, locator indexing, isolated worlds, stale and detached nodes, cookies, initialization scripts, same-origin and cross-origin frame locator actions, response recording and body retrieval, page-text extraction, screenshots, and page isolation.

The target browser is Camoufox 156.0.1-beta.36 with Juggler. Camoufox and Stagehand do not need source patches for these checks. The bridge holds DOM registries as remote objects rather than installing globals in page scripts.

## Limits

Completed network request metadata is retained for the most recent 1,024 requests across the bridge. Older entries lose response-body access; in-flight requests remain tracked. Failed requests and bodies reported as evicted by Camoufox are removed immediately.

- Child-frame accessibility snapshots are not supported by this Camoufox protocol. A frame-scoped accessibility query returns an explicit error; it must not return the parent's tree. Main-frame accessibility snapshots omit iframe descendants. Frame locator actions are a separate capability and can work without frame observation.
- These tests do not establish natural-language `act`, `observe`, structured model-based `extract`, or DOM-agent compatibility. Those still require real model-backed validation. The action-object test and the no-model page-text extraction overload do not substitute for those checks.
- Accessibility identity mapping uses Camoufox's native `foundObject` marker. It skips CSS `display:none` subtrees and pipelines batches of up to 32 native tree requests rather than guessing identities from labels. Hidden text can still contribute to accessible names through the browser's native tree. Each remaining candidate requires its own native tree, so large pages remain more expensive than Chrome's native accessibility query.
- Snapshots reject concurrent DOM changes or accessibility-state changes rather than combining identities and values from different page states. A changed snapshot is retried from scratch, with at most three attempts. Foxbridge stops starting retries after five seconds; individual native requests retain their backend timeout. Pages that keep changing can still return a changed-document error.
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
ORCHESTRATOR_DIRECTORY=/path/to/agentic-provider-orchestrator \
FOXBRIDGE_CDP_URL=http://127.0.0.1:19322 bun run test:orchestrator
```

The reference orchestrator commit is `1e7bdf37261f19cefcaf75136cc5bfeffe6aa93a`. The smoke test requires its dependencies to be installed and uses Bun to load its TypeScript entry points.
