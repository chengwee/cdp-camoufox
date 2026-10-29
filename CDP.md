# CDP Camoufox

This fork keeps Camoufox's upstream layout and maintains the Foxbridge CDP adapter under `foxbridge/`. Browser protocol changes and bridge changes can be reviewed and tested in one repository. The initial upstream base is `v156.0.1-beta.36`, commit `7a4ac0d4dca2dfdeb277f521824a26efb0b6a7b6`.

## Build and run

Build the browser on Linux using Camoufox's existing build system. See `AGENTS.md` and the upstream README for host dependencies and cross-compilation:

```sh
make dir
make bootstrap
make build
```

Build the adapter from its module directory:

```sh
cd foxbridge
go build -o foxbridge ./cmd/foxbridge
./foxbridge --binary /absolute/path/to/custom/camoufox --headless --port 9222
```

Use the browser built from this checkout. Official Camoufox releases do not expose the DOM identity extension; accessibility queries fail explicitly rather than using a slow compatibility scan. Add `--page-world-eval` when clients need Chrome-like access to page-owned JavaScript. This option makes that automation code visible to the page; named isolated worlds remain separate.

## Native accessibility

`Accessibility.getFullAXTree` accepts optional `frameId` and `executionContextId` parameters in Juggler. With an execution context belonging to the selected frame, it returns the tree and a temporary array handle containing the actual DOM nodes. Tree entries link to that array through `nodeIndex` and carry browser-owned `backendNodeId` values. The bridge registers the array in its DOM registry in one call, preserving its public CDP identifiers, then releases the array handle.

The browser captures the tree and handles together. It exports ordinary document nodes and open shadow-root nodes, not privileged anonymous nodes. Parent snapshots omit child documents; frame snapshots are requested separately. A frame without a native accessibility document, such as a hidden iframe, returns an empty result. Removing an element or navigating invalidates the corresponding bridge reference. Context metadata is scoped to the owning page.

The extension is additive for existing Juggler clients: requests without `executionContextId` retain the original response behavior. Stagehand and other CDP clients do not need source changes. The tested subset and remaining limits are documented in `foxbridge/test/stagehand/README.md`; this is not a claim of complete Chrome DevTools Protocol parity.

## Upstream updates

Keep `main` as the clean upstream mirror and use `cdp` for integration. Preserve upstream paths, licenses, and notices. Put new bridge-specific files under `foxbridge/` and prefix custom workflows with `cdp-`.

To prepare an update for review:

```sh
git fetch upstream --tags
git switch cdp
git switch -c update-camoufox
git merge --no-ff v156.0.1-beta.36
```

Replace the example tag with the release being adopted. Resolve conflicts in the update branch, update `foxbridge/camoufox-version.json`, build the custom browser, and run both browser and adapter checks before merging. Do not rebase shared history or force-push upstream changes over `cdp`.

The manually triggered **Prepare Camoufox upstream update** workflow accepts a release tag or commit and opens an update pull request. Already integrated revisions are left alone; existing update branches are reused without overwriting them. Merge conflicts stop the workflow without pushing an incomplete update. The workflow explicitly dispatches the full test pipeline because pull requests created with GitHub's automation token do not trigger it automatically. GitHub Actions must be allowed to create pull requests for this workflow to open them.

## Verification

From `foxbridge/`, run `go test -race ./...`, `go vet ./...`, and the Stagehand and local Puppeteer checks documented in `test/stagehand/README.md`. Browser changes also require upstream's applicable automation guards and Linux build pipeline. Testing repacked Juggler JavaScript inside an isolated release app is useful local evidence, but does not replace a full source build.

The root Foxbridge workflow runs Go and documentation checks. The existing full browser pipeline also runs Stagehand against its prepared browser artifact, and both Foxbridge jobs are required by its final gate.
