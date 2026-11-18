# Native app link artifacts

MyChef prepares three website routes for `dev.mychef.app`. The renderer writes a local artifact with a digest for review. Website publication uses a separate owner approval, verified provider readbacks and the website owner’s deployment path.

| Route | Content |
| --- | --- |
| `/.well-known/apple-app-site-association` | One Apple application identifier and the exact `/app/open` path |
| `/.well-known/assetlinks.json` | The new Android package and every applicable Play app-signing fingerprint; Android 15+ rules also restrict the path |
| `/app/open` | A neutral branded page linking to the existing `/app/` browser route |

The API origin, account proofs and Preview associations keep their existing routes. The prepared handler returns control to the existing website router for other paths. The fallback contains no script, automatic redirect, account data or private origin. Its style has a CSP hash and its response uses `no-referrer`.

## Preparation and rehearsal

A paused artifact can be prepared before the new store records exist. Put this non-secret input in a local JSON file:

```json
{"enabled": false, "appId": "dev.mychef.app"}
```

Run the repository’s pinned Bun:

```sh
bun scripts/native-app-links.ts render .test-artifacts/native-links-input.json .test-artifacts/native-links
bun scripts/native-app-links.ts verify .test-artifacts/native-links
bun test scripts/native-app-links.test.ts
```

The output directory must be new. Verification reconstructs the approved shape and compares every byte; changed files, symlinks and additional manifest fields fail. The sidecar `manifest.json` stays in operator staging. Install only the three route bodies, with the content types and response headers in the manifest. The controlled HTTPS test uses a disposable certificate, verifies it explicitly, checks 200 responses without redirects, and keeps a representative existing browser route working.

Enabled input adds `appleApplicationIdentifier` and `playSigning`. The Apple value comes from the actual Application Identifier Prefix and the new bundle ID. Read it from the verified signing/profile facts; a remembered Team ID is insufficient. `playSigning` contains `source: "play-app-signing"`, `complete: true`, the full `fingerprints` array and `fingerprintSetSha256`, calculated over the JSON serialization of the sorted array. Each fingerprint is 32 uppercase hex bytes separated by colons. Preserve all applicable signers, including hybrid signing variants. Upload and debug certificates are excluded.

These fields describe the renderer’s input contract. Authenticated readback and owner authorization are separate delivery evidence. A locally recomputed fingerprint digest does not establish either one.

## Website owner handoff

Before publication, present the exact site/account, operation, three paths, artifact digest, verified Apple identifier, current complete Play signing set and readback evidence. Verify the selected artifact and current scope again immediately before the approved write. Read back HTTPS status, certificate, response headers, body digests and absence of redirects afterward. Keep the private API and proof endpoints outside this publication.

Paused output empties the two association declarations and retains the browser page. Installed devices can retain older association data. The app’s private authorization policy provides the paused state, and the final push gate stops new submissions; the unassociated `/app/` browser action avoids reopening the native handoff. Preserve the old Preview and pending local changes through the rollback window.

Local renderer and HTTPS results are recorded in the native identity feature. Signed iOS and Play-installed association behavior, supported source preferences, co-installation and signing-version boundaries retain their own unfinished checks until observed.

## Provider references

- [Apple associated domains](https://developer.apple.com/documentation/xcode/supporting-associated-domains): application identifier prefix, entitlement and HTTPS association endpoint.
- [Apple applinks](https://developer.apple.com/documentation/bundleresources/applinks): exact path components.
- [Android website associations](https://developer.android.com/training/app-links/configure-assetlinks): Play signing fingerprints, static manifest scope and dynamic rules.
