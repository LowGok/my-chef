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
| `native-identity-installed-android` | `preview`, `app` | Android emulator journeys |

These labels select an explicit matrix lane; a branch, hostname or display name
cannot select the packaged identity. The existing native and reminder label
routes stay Preview-only. Current app identities are
`com.gkldevelopment.mychef.preview` and `dev.mychef.app`. The Android namespace
and activity class remain `com.gkldevelopment.mychef.preview.MainActivity` in
both packages. Instrumentation uses the selected package as its target.
The module explicitly enables [resource value generation](https://developer.android.com/reference/tools/gradle-api/9.1/com/android/build/api/dsl/BuildFeatures)
for the selected display name. The packaged receipt checks that name as well as
the application ID, so a successful compile with a sibling label is rejected.

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

## Prepare Android push for the private app lane

The private Android build can enable the existing Firebase push variant for
`dev.mychef.app`. Supply `-PmychefIdentityLane=app`,
`-PmychefPushPreview=true` and `-PmychefFirebaseResources` pointing at
resources generated from the approved Firebase Android client. The resource
directory needs `values/mychef-firebase.xml` and its `mychef-package.txt`
marker generated for `dev.mychef.app`. Keep these inputs outside Git. Verify
the exact private candidate ingress and keep dependency verification strict.

Build `:app:bundleRelease` for the new lane with this configuration. Verify its
application ID and merged manifest, including the non-exported messaging
service, disabled Firebase automatic registration, and disabled analytics.
The Preview push variant remains debug-only. Builds without the explicit push
setting continue to exclude the Firebase messaging dependency. This variant
does not construct a signed artifact or activate checkout.

## Rehearse upgrade and withdrawal

Build the app and migration binaries from the last schema-28 source and from
the candidate. Run the rehearsal with the four explicit binary paths.

```sh
python3 scripts/test_native_identity_upgrade.py \
  --prior-app "$prior_app" --prior-migrate "$prior_migrate" \
  --candidate-app "$candidate_app" --candidate-migrate "$candidate_migrate"
```

The script starts both binaries on loopback with fake providers and disposable
SQLite files. It seeds a verified account, a Preview session and a saved shopping
item. After deliberately failing the first native identity migration it restarts
the previous binary on the same database and current lifecycle journal. It then
completes the upgrade, checks the Preview session and saved revision again, and
signs in as the same account through the new app route. It exercises the local
operator control, rejects a TCP policy write, withdraws the candidate, and
verifies persisted withdrawal after restart. Use this failure rehearsal
before moving the shared Preview database. After a successful migration, keep
using the compatible binary and pause the new lane to withdraw it.

```sh
go test -race ./internal/http ./internal/auth ./internal/store \
  -run 'NativeIdentity|ReminderCutoff' -count=1
bun test web/src/native-shopping.test.ts web/src/native-app-identity.test.ts
```

The HTTP checks cover two lane-bound sessions for one account, overlapping
shopping edits, the bounded sync window and its cutoff, plus a restarted
handler. The cache fixture checks separate pending edits for the same account.
The compiled builds, fixture sessions and old-binary rehearsal do not prove
installed store co-existence or the new identity's real store, link or push
configuration.

## Activate or withdraw the candidate

Set `MYCHEF_NATIVE_IDENTITY_CONTROL_SOCKET` to an absolute path ending in
`native-identity.sock` on the candidate server with app ingress enabled. The
service user owns the dedicated 0700 directory and the listener sets the socket
to 0600. Run operator commands as that service user, on the host or inside its
private container. Leave the socket outside reverse proxies, Tailscale Serve
and any shared mount. With the setting absent the process opens no operator
listener.

Read the revision before applying an approved change. Send the observed
revision as `expectedRevision`. Pausing can name a separately approved
`syncUntil` UTC Unix timestamp for existing shopping sessions. Omit it for an
immediate cutoff. Read the policy again after a lost or non-success response and
reconcile the observed revision before attempting another change.

```sh
curl --fail --silent --show-error --noproxy '*' --unix-socket "$mychef_control_socket" \
  http://localhost/policy
curl --fail --silent --show-error --noproxy '*' --unix-socket "$mychef_control_socket" \
  --request PUT --json '{"expectedRevision":1,"active":false}' \
  http://localhost/policy
```

The example withdraws only if the observed revision is `1`. Activation uses
`"active":true` with the current revision and no `syncUntil`. Verify the
returned state, signed candidate status and Preview account access after the
change. If the socket path is occupied, verify which process owns it and
reconcile the recorded policy before cleaning up the path.
