# Verify both native identities

Use the existing **public** `LowGok/my-chef` Native verification workflow for
secret-free checks. Its branch and PR allowlist, repository ID and visibility,
owner-only label trigger, exact head SHA and read-only token are checked before
compilation. It does not sign, contact a store, invite testers or enable checkout.
Do not run these jobs in the private archive or signing repository.

After the owner approves publishing a reviewed candidate for verification, open
its draft PR from `feat/native-app-identity` and set `NATIVE_IDENTITY_PR` to that
exact PR number. The operator reads back the public repository and variable
before adding a label. Labels from contributors, forks or a different PR fail
closed. Keep the PR unmerged until its review and shipping gates pass.

| Owner label | Lanes | Jobs |
| --- | --- | --- |
| `native-identity-compile` | `preview`, `app` | Android, iOS Simulator, unsigned iOS device compile |
| `native-identity-installed` | `preview`, `app` | Android emulator journeys and iOS Simulator session/relaunch |

These labels select an explicit matrix lane; a branch, hostname or display name
cannot select the packaged identity. The existing native and reminder label
routes stay Preview-only. Current app identities are
`com.gkldevelopment.mychef.preview` and `dev.mychef.app`. The Android namespace
and activity class remain `com.gkldevelopment.mychef.preview.MainActivity` in
both packages. Instrumentation uses the selected package as its target.

Compilation inspects the produced Android manifest metadata or Xcode-built
Info.plist, checks package, version and build, then binds `packagedAppId` into the
receipt with the source commit and artifact digest. The installed harness
refuses another lane's receipt before installing anything. It exercises the
fixed lane ingress through a disposable HTTPS proxy and uses matching local
storage selectors. Artifact names include the matrix lane to avoid overwrites.
Only the existing three sanitized JSON receipt paths are uploaded, for one day;
app packages, raw logs, account data and signing/provider files stay excluded.

The disposable app-lane fixture passes `MYCHEF_NATIVE_APP_INGRESS=1` and
`MYCHEF_NATIVE_IDENTITY_FIXTURE=1` alongside the existing explicit loopback E2E
and fake-provider settings. This helper may activate only revision zero and
never reopens a persisted pause on restart. Live services reject the fixture
activation flag. This is not an operator route for the real beta backend.

Local unit checks include the workflow allowlist, package mismatch, wrong-lane
receipt, trusted proxy routing and persisted-pause regressions. They do not
prove that a particular hosted compiler run succeeded. Record public job URLs
for the exact candidate after running the workflow. Simulator or emulator
receipts do not establish store signing, co-installation through the real
stores, live push, sandbox purchases or OS link association. Those remain the
separately approved native identity pilot.
