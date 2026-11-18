---
status: in-progress
branch: feat/native-app-identity
status_description: "Implementing both native identities; existing subscription proof and exact live-operation approvals remain separate."
issue: https://github.com/LowGok/my-chef/issues/42
goal: "Approved testers can install the same MyChef app identity that will be used for release while their existing MyChef accounts and the current Preview remain usable."
covers:
  - .github/workflows/
  - mobile/
  - scripts/
  - cmd/mychef/
  - internal/auth/
  - internal/http/
  - internal/notifications/
  - internal/billing/
  - internal/store/
  - migrations/
  - web/src/
  - api/openapi.yaml
  - .dockerignore
  - docs/reference/
  - README.md
---

# Plan: MyChef native app identity

Prepare `dev.mychef.app` for iOS and Android and use those same store records for a private beta and the eventual release. Keep the current Preview as a separate app during the transition.

## Context

### Owner summary

The owner chose `dev.mychef.app` for both platforms, for private beta and the eventual release. The approved main-domain link uses a browser fallback for same-site Safari navigation. That choice avoids setting up another temporary identity before release. Apple locks the bundle ID after the first upload, and Google Play treats a different package as a different app. The new identity installs beside the Preview and needs a fresh sign-in. The pilot requires a usable Preview build for the entire test and rollback window.

People keep the same MyChef account and server data. They should let pending shopping changes sync in the old app before starting with the new one. The new install starts with its own session, shopping cache and notification permission. The current Preview, its tester access and its subscription tests stay in place until there is a verified alternative. The old installation is also the fallback if a pilot fails.

First prove both identities in public CI with test fixtures, then verify private signed builds against the new store records using synthetic accounts. After approval, add one public app link with a web fallback and publish its non-secret association files. The API stays private. Prove live push on the signed private builds and test purchase and restore with the new identity. TestFlight uses production APNs and App Attest with sandbox purchases. Android checkout tests require an approved license tester and a test payment method. Tests on the old identity and Xcode or Android fixtures cover different things and cannot stand in for this proof.

The first test is internal on both stores. An external pilot would use the same app identity but needs its own review and access plan. The spec does not authorize creating store records, changing app access, publishing a website or build, moving testers, or opening paid checkout. Before each of those steps we need the exact target and separate authorization. Public store release, the recipe pipeline, and analytics and automated emails stay outside this work.

### Background

- The [identity issue](https://github.com/LowGok/my-chef/issues/42) calls for a move from `com.gkldevelopment.mychef.preview`. The owner selected `dev.mychef.app` for both platforms in the specification discussion.
- At public main `9a4079667ddc2bdfe32ae6455ec60d0d68b4eb98`, the Android package and Java namespace are defined in `mobile/android/app/build.gradle:23-26`; resource names are in `mobile/android/app/src/main/res/values/strings.xml:5-6`. The iOS build settings use the same ID in `mobile/ios/App/App.xcodeproj/project.pbxproj:328,350`. Capacitor generation in `scripts/mobile-build.ts:15-41` uses that ID with a separately configured private API origin. `scripts/mobile-delivery.ts:5` also pins the ID for delivery.
- The build consumers also include the iOS verification ID in `scripts/mobile-compile.ts:278` and the Android probe target in `scripts/native_android_probe.py:11,23-27`. The runtime package ID, Java namespace and probe IDs have different jobs. Keep existing iOS Keychain service names isolated by the app's signing identity; they are local storage keys, not store identifiers (`mobile/ios/App/App/MyChefNativePlugin.swift:19`, `mobile/ios/App/App/ProtectedShopping.swift:14`).
- The Android app disables backups (`mobile/android/app/src/main/AndroidManifest.xml:5`) and encrypts the shopping payload using an account ID and instance from its local app state (`mobile/android/app/src/main/java/com/gkldevelopment/mychef/preview/ProtectedShopping.java:34-67`). Native sessions are separate server bindings (`internal/auth/native.go:74-114,141-217`).
- Push reads settings for Apple and Google from private runtime configuration (`cmd/mychef/notifications.go:19-65`) and sets the APNs topic on the request (`internal/notifications/provider.go:257`). The native verification matrix builds both platforms (`.github/workflows/native-compile.yml:14-58`). As checked on 18 November 2026, [weekly reminders](https://github.com/LowGok/my-chef/pull/11) had merged and the [native billing PR](https://github.com/LowGok/my-chef/pull/37) was open and draft at `8904a7a`. Billing and App Attest checks on the new identity depend on the reviewed billing contract and existing sandbox results; this work does not substitute for them.
- Apple and Google identify uploaded builds by their app IDs. Firebase cannot rename a registered Android package. See the provider sources below. This is a new app installation and record on each store. Changing a display name or the backend URL alone does not move the app identity.

## Scope

Cover both native build identities, store and tester isolation, signing, private backend targeting, website link association, push, and sandbox billing compatibility. Reuse the current Apple and Play accounts and the existing approved Firebase project where suitable. Keep code publication separate from provider setup; private operational values and actual tester details stay outside Git.

Excluded: replacing the current Preview in place, bulk tester invites, exposing the private backend or its database, copying app-local secrets or unsynced files, production billing or App Store/Play release, public distribution links, provider or DNS changes for marketing or email, analytics, and the recipe pipeline. No account identities or data move to a different database. Additive session and provider metadata changes must preserve compatibility with existing builds.

## Design

### Candidate identity and access

| Lane | iOS bundle ID / Android application ID | Distribution |
| --- | --- | --- |
| Existing MyChef Preview | `com.gkldevelopment.mychef.preview` | Existing groups, tracks, builds, API routing and tests |
| MyChef private beta and future release identity | `dev.mychef.app` | Private TestFlight and Play test tracks only during this work; no automatic distribution |

Select the lane explicitly in the build. Validate the selected app ID against Capacitor, Gradle (application ID, namespace/class resolution, flavor definitions, resources and intents), Xcode (all configurations, entitlements, schemes and URL types), native test selectors, and the artifact manifest. Validate the API origin separately. Fail on missing or inconsistent values before signing, uploading or running a live test. Do not infer store or backend targets from the branch, hostname, or display name. The legacy lane stays available from the existing source and can still tag which artifact it produced. Keep current #37 tests and approved distributions on the old lane.

Verify the exact Apple team and Play developer account, ID availability, permissions and whether the target records already exist. An approval to create a record names the verified account, operation, platform, bundle/package, intended record name and Apple SKU where applicable. Read back the new record after it exists. A later step approves only prerequisites that can exist at that stage: group creation binds the parent record, group type, name, automatic distribution off and initial empty roster; read back its new ID. The first Play upload may enroll or establish Play App Signing; approve the record, expected signing mode and upload certificate, signed bundle digest and inactive draft target before that step. Read back the generated signing fingerprints before sharing any links or activating a test release. Build upload or assignment must bind the exact operation, immutable record and eligible artifact, plus the current signing fingerprints, group or track and roster where relevant. Compare current values before mutation; a stale approval or mismatch stops the operation.

Reuse the existing Apple and Play accounts. On Apple, verify the new explicit App ID and provisioning profile with the relevant capabilities. On Google, verify the complete applicable set of Play App Signing fingerprints separately from upload signing and add an Android registration to the approved Firebase project; keep its config outside Git. Initial signed proof uses TestFlight Internal Only and Google Play internal testing with named testers and automatic distribution off on TestFlight. Keep the current groups, tracks and tester roles. External TestFlight is out of scope here: it needs a separately approved distribution-eligible artifact, synthetic reviewer access or an accepted demo route, Apple beta review and manual invitation. The private backend remains private.

Public GitHub Actions runs secret-free variants for both identities, including Android, iOS simulator and unsigned iOS device compile. Use the existing approved protected signing path separately. Only the scoped service identities and operator approvals for the selected environment can perform store and provider changes. Record those approvals, artifact hashes and provider readbacks privately, without secrets or tester addresses in the repository.

### Account continuity and links

First use disposable accounts on a private backend that matches the candidate contract. Before any approved move of existing testers, confirm the new lane can use their same authorized private account backend, not a copy that could diverge. Keep the current route and API behavior available while both apps run. A fresh installation signs in normally and loads saved recipes, favorites, plans, synced shopping changes and server-side entitlement from the current account. Do not change the account ID, import a session or share keys or caches across installs. Tell the tester how to confirm shopping edits have synced in the old app; if offline or pending, keep the old installation and wait before switching. Use existing server write and shopping sync semantics for overlapping changes; no import or second account is created.

The app-opening link is `https://mychef.dev/app/open`, with no query or fragment. A tap from another app opens Meals after normal sign-in when platform link handling is enabled. Address-bar entry and user-chosen browser handling stay on the web. With the main host, same-site Safari navigation also stays on the web. In a browser it shows a neutral page with a link to the existing `/app/` page. The handoff carries no account or device data. On 18 November 2026, `curl` returned HTML 200 for `/app/` and 404 for the proposed path and both `/.well-known` association paths. Prepare the fallback and association files with the website owner and seek approval for these exact paths. No API, verification endpoint, or private origin is published.

Bind the Apple App ID through an exact-path AASA rule and the Android package through Digital Asset Links using every applicable Play app-signing SHA-256 fingerprint from authenticated readback, including all hybrid signatures if used. Upload certificates and debug fingerprints are excluded from the public association. The Android manifest restricts the exact host and path on old and new Android; Android 14 and earlier ignore dynamic rules. Use a narrow native handler that validates HTTPS and exact URL shape. On an allowed cold launch open Meals after sign-in. On a warm launch use the normal navigation and unsaved-edit guard in `web/src/App.tsx:190-208`; never replace or interrupt an active private proof flow. Preserve the private proof validator in `web/src/native-runtime.ts:20-41,85-90`. Do not claim account proof paths, other public URLs or any legacy route. Android may still dispatch path matches with an unsupported query or fragment; reject those in the app and offer the browser fallback. If the OS caches an association and still launches the withdrawn candidate, show a neutral paused state and a browser action to the unassociated `/app/` route. Keep handoff disabled without current candidate authorization, including on a cold offline launch.

### Provider separation and retries

Current native sessions bind only the platform (`internal/auth/native.go:102-108`); the registration accepts only a token and OS permission (`internal/http/notifications.go:68-94`, `web/src/reminder-device.ts`). Map the server-validated private listener or trusted ingress route to a fixed lane. Keep ingress routing private and strip or reject client-controlled routing headers. Save the lane on the family during native start and carry it through sign-in replacement, refresh and binding. Every native protected request using a bearer must validate its family's lane against the trusted ingress and current lane policy before reaching a handler. The lane selects routing and grants no user or store privileges. Existing families and registrations default only to legacy and keep the old request format and behavior. New installations must sign in and opt in separately.

Bind token uniqueness/replacement and queued delivery to the immutable lane and environment alongside the existing account and session checks. A retry, cancellation, or provider rejection affects only the original lane. Reject a known token claimed across lanes without changing the original registration. For an unknown token that cannot be checked until send time, constrain APNs to the exact topic and require FCM's package restriction, even on the same project. Never deliver to the wrong app or treat an invalid token as a reason to try another route. TestFlight receipt must use the signed production APNs entitlement and topic. The live pilot uses production App Attest for the new App ID, with StoreKit sandbox transactions. Treat all three settings separately. Keep the per-opted-in-install policy and allow turning reminders off on the old install. Before moving real testers, prove both private ingresses use a shared authoritative account database with separate provider routing; otherwise stay in disposable isolation.

Start real store tests only after the reviewed [billing feature](https://github.com/LowGok/my-chef/pull/37) passes its own sandbox gates. Validate store, app identity, plan and environment before accepting a transaction, callback or restore. Apple uses sandbox billing behind exact-identity App Attest; Google requires an eligible base plan on the new record and the approved license tester as the actual Play purchasing account. Any required plan activation needs separate approval. Ordinary accounts may not launch checkout before, during or after the test. The isolated route enables sandbox checkout for the approved synthetic account for the test window only. Verify Play shows the approved test payment method and no real charge before confirming, and reject non-test receipts or wrong-app events. Close the sandbox route after proof. If an eligible test plan cannot be set up within those limits, stop and leave the Play proof pending. Legacy store purchases do not transfer across store records. Existing server-granted Premium stays on the account.

Serialize provider writes per lane and reconcile uncertain results by provider resource and approved scope before retrying. Never edit a published migration; rehearse additive schema upgrades from the previous version with candidate features off, preserving accounts, sessions, registrations and queued work. Failure before the migration commits leaves the old state usable. After rollout, do not start an incompatible older binary on a newer schema or restore a stale backup over writes. Withdraw only the candidate: stop new assignments, close candidate sign-in and privileged routes, and put the handoff into its neutral fallback state even if installed apps retain association data. Retain unsynced device state. An approved sync window has an exact deadline and permits only the existing authenticated families to reach the specific shopping sync endpoints. Existing access cannot escape the policy through the legacy ingress. Do not grant new families during withdrawal; refresh of an existing sync family, if needed, must retain the same restriction and may not outlive the deadline.

Stop push claims, submissions and retries on the candidate lane at the acknowledged cutoff. Serialize the cutoff with the last submission gate: a claimed job that has not crossed that gate is cancelled, and an already submitted request may receive a result later. Do not resubmit ambiguous results or revive them after re-enabling. A previously accepted notification may still arrive before its provider expiry; an old tap opens the neutral state without reopening protected data. For billing, check lane state again in the same transaction as entitlement writes. Already committed sandbox outcomes stay in the isolated environment; attempts in flight at closure that have not committed and replayed events cannot create grants. Leave a redacted reconciliation record for already sent or acknowledged store actions and finish only under the authorized sandbox scope. Verify the legacy private backend, track and unexpired build remain usable throughout the rollback window. Stop the pilot if that cannot be assured. Do not delete store records or try to recycle reserved IDs.

### Inputs before implementation and live proof

| When | Verify or obtain | Owner involvement |
| --- | --- | --- |
| Before provider setup | Verified Apple app-management and Play Organization access, identifier availability, Firebase app-registration access, selected store records and website path owner | Authenticate only if existing access fails; approve specific record, profile and provider changes after seeing the plan |
| Before signed test | Approved protected signing route, profile and package signing custody, private origins, exact group and licence scope, explicit product and offer identifiers, test-checkout scope and callback credentials | Approve the exact build upload, group and track; hand off any needed credentials through a verified private flow |
| Before a tester move | All criterion evidence, readback of the exact approved recipients and access, site association and rollback drill | Approve the public fallback page and association files and any internal tester move separately; external distribution and final store release are separate work |
| During verification | CI, simulator and device relay first; supported Apple device for real App Attest and sandbox checkout, licensed Play target for real billing, signed device for actual push where a fixture cannot prove it | Ask for one combined check only for device behavior unavailable through the approved test rigs |

## Acceptance Criteria

- [ ] AC-01: Both native platforms package as `dev.mychef.app` for the new private lane; the original Preview lane can still be built independently, and inconsistent identities or endpoints are rejected before delivery.
- [ ] AC-02: Authorized testers can install the signed candidate beside the legacy app through the exact approved internal groups or tracks; automatic distribution stays off and no other group, track or public audience receives it.
- [ ] AC-03: Existing accounts sign in on the new app and see the same server-owned data and Premium grants. Credentials are bound to their lane through sign-in and refresh. The two installations use separate device-local state, and pending offline shopping changes are preserved until they sync from the original installation.
- [ ] AC-04: The approved link opens Meals in the intended native app from supported sources when platform settings allow. With no candidate, a disabled link or a browser-preferred source, the link uses the browser fallback. Other links, the current website and legacy app routes retain their behavior.
- [ ] AC-05: New installations receive a real opt-in push on each platform, through the matching app identity, without delivering to unrelated or opted-out installations. Users can still manage reminders on the legacy app.
- [ ] AC-06: Actual Apple and Google sandbox purchase and restore work on their respective new signed store apps; a wrong app or environment cannot attach a purchase to the new lane, and live checkout remains off.
- [ ] AC-07: A partially failed candidate handover leaves legacy distribution usable. Retrying or withdrawing the candidate does not duplicate store resources, expand access, lose synced account data or let issued credentials continue protected operations past the cutoff. Previously accepted push delivery cannot reopen the candidate.

## Test Plan

| Criterion | Proof |
| --- | --- |
| AC-01 | Run Delivery and the public native matrix (Android, iOS simulator, unsigned iOS device) for both lanes. Inspect packaged IDs, intents, entitlements and origin fixtures against the lane manifest. Intentionally mismatch the measured app ID, lane or allowed origin and confirm the packaging or delivery guard refuses it. |
| AC-02 | Compare signed artifact IDs, entitlements and fingerprints with authenticated store reads. Install each new build on an approved target with the old one still installed. Read back exact groups, rosters, track, build and automatic distribution; confirm other groups and tracks have no candidate and an unlisted identity has no access. Fixtures check each create-stage readback and reject wrong team, record, group, track, artifact or stale approval before a provider mutation. |
| AC-03 | On both platforms, seed a synthetic existing account with a saved recipe, favorite, week, shopping entries and server Premium. Create an offline shopping edit in the old app, launch the new app signed out, reconnect and sync the original edit, then sign in and check the same ID and data. Make overlapping writes and relaunch both; revoke one session and verify the sibling session and private cache behavior. Confirm neither install receives the other's local state. Replay bootstrap, sign-in replacement, refreshed and unrefreshed access against the wrong ingress and confirm the original remains active. |
| AC-04 | Validate the HTTPS files and actual signing IDs. Test cold open to Meals through sign-in, browser fallback without the candidate and paused fallback with a cached association and both apps installed. Verify Play-installed builds across Android 14/15 path behavior and Android 16/17 signing behavior; also verify a signed iOS target. Cover both install orders, approved and browser-preferred sources, a warm app with unsaved edits, unrelated and malformed links, queries/fragments, and active account proofs. Use a controlled host first; repeat after the approved website update. |
| AC-05 | Upgrade old sessions, registrations and queued jobs in fixtures; check old request formats, cross-lane refresh/registration, token rotation, opt-out and replay. On signed tests read back entitlements and routes; record sender acceptance, receipt and tap separately with opted-out and legacy controls. Verify a mismatched APNs topic and same-project wrong FCM package do not deliver or disturb legacy registration. |
| AC-06 | Use fake providers for mismatch and callback replay in CI. On isolated signed targets prove a TestFlight sandbox purchase and restore with production App Attest, and Play licensed purchase and restore with a test payment method. Verify scope, server acknowledgement, entitlement, rejection of wrong app/environment, and denial for ordinary accounts before opening the store. Read back closed sandbox routing after the test. |
| AC-07 | In disposable fixtures interrupt a provider write and reconcile it without duplication; also fail a schema upgrade and verify unchanged legacy behavior. Rehearse withdrawal with both apps installed and stale association data. Hold a push at the final send gate, another at provider acceptance, and a billing callback before its grant transaction; withdraw and confirm no new submission, retry or grant. Simulate late receipt and tap. Check a previously issued session against the wrong ingress and denied routes before, during and after the allowed sync window. Confirm legacy group and track are unchanged, the old build is unexpired and can open the correct account, and already synced changes remain present. Check that unsynced local edits stay in their originating install. |

## Tasks

- [x] T1 `scripts/native-identity.ts`, `scripts/native-identity.test.ts`, `scripts/mobile-build.ts` - fail lane/ID mismatch tests first; define explicit preview/app identities and bind the selection into prepared config and asset manifests without widening private endpoints (AC-01).
      Evidence: The tracked regression first failed because the identity module was absent. Ten direct packaging tests and the existing delivery/hosted tests then passed; TypeScript checked both identity manifests. Altering either lane or package fails even after recomputing the manifest hash, and both lanes reject public or insecure API origins.
- [x] T2 `mobile/android/`, `mobile/ios/`, `scripts/mobile-hosted.ts`, `scripts/mobile-compile.ts`, `scripts/native_android_probe.py` - fail package/probe selection tests first; package both app IDs using the matching build inputs while keeping compile namespaces and storage isolation intentional, then verify packaged identity and capabilities (AC-01, AC-03).
      Evidence: The build-plan test first failed for the missing selected-lane Android flag. The dual-platform command, package, generated-config and actual JVM mismatch guards now pass with the existing native boundary tests. Python probe selection and the existing probe verification suite pass for both IDs. iOS compilation and signed packaged-artifact proofs remain in T10/T13 and AC-01 remains unchecked.
- [x] T3 `internal/store/`, `migrations/`, `internal/auth/` - fail legacy upgrade and session-lane tests first; add transactional lane metadata, backfill only the legacy identity and retain it through sign-in, binding and refresh with explicit compatible migration and rollback checks (AC-03, AC-07).
      Evidence: The tracked tests first failed for missing family metadata and the lane migration. Explicit schema 29 now preserves existing families as Preview, rejects foreign lane values, and rolls back injected failure before a successful retry. Native access, refresh and family replacement tests pass; a wrong-ingress attempt cannot consume, revoke or adopt the original lane's authority. Actual old-binary and co-installation rehearsals remain in T12.
- [x] T4 `internal/http/`, `internal/config/`, `cmd/mychef/` - fail cross-ingress and issued-token withdrawal tests first; derive the lane from server configuration, check every native request and enforce candidate pause plus bounded existing-family shopping sync while legacy behavior stays available (AC-03, AC-07).
      Evidence: Missing-ingress/policy and actual JVM route tests were observed failing first. Native, login and shopping regressions, focused policy/migration tests, race checks, 17 Python verification tests, generated API checks, TypeScript and the full Bun suite pass; the pinned linter reports zero issues. Schema 30 persists pause and revision-bound sync, with rollback/retry tested on disposable data. Withdrawal waits for admitted HTTP work; expired or anonymous families cannot refresh into shopping authority. Preview remains usable. Manual documentation impact review updated README and the generated API contract; no mechanical coverage declaration exists. Signed and live provider proofs remain unchecked.
- [x] T5 `internal/store/notification_settings.go`, `internal/store/notification_dispatch.go`, `internal/notifications/`, `cmd/mychef/notifications.go`, `api/openapi.yaml` - fail token replacement and provider-route mismatch checks first; bind installations and retries to the server lane/environment, enforce APNs topic and FCM package restriction, then prove old client compatibility and new independent opt-in (AC-05).
      Evidence: The new registration/provider and runtime-selection contracts first failed for missing route metadata. Focused tests and all existing reminder/provider regressions pass against fake transports, with zero pinned-linter issues. Schema 31 preserves legacy ciphertext and queued work through injected failure and retry; candidate registration cannot steal a known Preview token, and delivery/retry metadata is immutable. APNs topic and FCM restricted package follow the selected sender, with no sibling fallback. API and README were manually reviewed and updated; live push and signed-device proof remain in T13 and AC-05 remains unchecked.
- [x] T6 `internal/store/notification_dispatch.go`, `internal/notifications/` - fail claimed-send and accepted-send withdrawal races first; serialize final dispatch admission with lane pause, retain uncertain outcomes without replay and make late taps safe without promising provider cancellation (AC-05, AC-07).
      Evidence: Missing final-gate contracts were observed failing, followed by a real cross-lane reminder-open failure. Claimed, submitted/late-result, retry and native tap regressions now pass with fake providers; race checks and the pinned linter pass. A real loopback HTTP/2 test confirms release after request write and before a delayed reply. Pause suppresses unsubmitted copies and late temporary retries, while accepted/uncertain outcomes stay terminal across re-enable. Candidate references cannot open through Preview; matching-lane controls pass. README was manually updated; actual provider receipt and withdrawn-install UI remain separately unverified.
- [x] T7 `web/src/native-runtime.ts`, `web/src/App.tsx`, `mobile/`, `scripts/native-identity.test.ts` - fail cold/warm app-opening and malformed URL tests first; add the exact main-domain route to Meals with sign-in, existing unsaved-edit and active-proof guards, static Android path restriction and a safe paused/browser fallback (AC-04).
      Evidence: Exact-link/runtime/JVM/entitlement and storage-renewal contracts failed first, then passed (35 focused unit/native contracts). Three candidate Chromium journeys pass for cold authorization, Meals navigation, private-proof preservation, unsaved-editor cancellation and warm draft retention; all five Preview runtime and two offline-shopping journeys also pass. Focused native Go policy tests, API generation, type-check/build and repository lint pass. A full repository run exposed one older migration table allowlist; adding only native_identity_policy preserves all original data/schema comparisons, and that test now passes. The broad rerun remains a T14 verification item until observed complete. Actual signed association handling remains in T13; AC-04 is unchecked.
- [ ] T8 `scripts/`, `docs/reference/` - fail association and browser fallback fixture checks first; generate only the approved path, complete provider signing fingerprint set and narrow AASA/Digital Asset Links files, with a website-owner handoff and controlled-host rehearsal before any separately approved publication (AC-04).
- [ ] T9 `scripts/mobile-delivery.ts`, `scripts/mobile-delivery.test.ts`, `scripts/` - fail wrong-target and uncertain-create retry tests first; require stage-specific immutable provider scopes, exact artifact and roster approval before writes, and idempotent authenticated readback with no implicit distribution (AC-02, AC-07).
- [ ] T10 `.github/workflows/native-compile.yml`, `scripts/` - fail CI matrix and receipt scope checks first; prepare secret-free Android, iOS simulator and unsigned iOS device verification for both identities, with exact packaged-identity receipts and private signing kept separate (AC-01, AC-02).
- [ ] T11 `internal/billing/`, `internal/http/`, `scripts/` - after the subscription dependency supplies its reviewed contract, fail wrong-lane purchase/callback and withdrawal-commit tests first; implement exact-app/environment sandbox routing and same-transaction pause checks without opening normal checkout (AC-06, AC-07).
- [ ] T12 `scripts/`, `docs/reference/` - run the committed disposable account/co-installation and migration/withdrawal rehearsals for both identities, including independent caches, current lifecycle journal and old-build expiry readiness; preserve failing transcripts and close all automated findings (AC-03, AC-07).
- [ ] T13 `docs/internal/plans/native-app-identifiers.md` - after exact provider/store and build approvals, run and record signed private installation, co-installation, live push and actual sandbox purchase/restore proofs on each new store record, including all supported link/signing boundaries and readback of the approved roster (AC-02, AC-04, AC-05, AC-06).
- [ ] T14 `docs/internal/plans/native-app-identifiers.md` - run the applicable repository checks and independent exact-commit implementation review, preserve any unmet store/device criteria as unfinished, and request shipping only when its actual gates are satisfied (AC-01, AC-02, AC-03, AC-04, AC-05, AC-06, AC-07).

## Open Questions

None. The owner approved the native identity draft and implementation on 18 November 2026, following the recommendation to use the existing main-domain app-opening link.

## Review Log

| Date | Review | Kind | Commit | Model | Isolation | Outcome |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-11-18 | R-1 | spec | `49876561b72921b80e9082eb02add453fb99a39b` | unknown | procedural | Five blocking and three should-fix findings |
| 2026-11-18 | R-2 | spec | `c0be3665a7791408413d56b9f15b5c86f7ccd165` | unknown | procedural | Five blocking findings, one should-fix finding and one nit |
| 2026-11-18 | R-3 | spec | `ec340e953b411d26a969c2326543b86ff4df570d` | unknown | procedural | No findings |
| 2026-11-18 | R-3 | spec | `ec340e953b411d26a969c2326543b86ff4df570d` | n/a | n/a | accepted by owner |

### R-1

FINDING: F-01
KIND: factual
SEVERITY: blocking
WHERE: Owner summary:32; Candidate identity and access:61; Provider separation and retries:73,75; Inputs before implementation and live proof:84,86; AC-05, AC-06; Test Plan AC-05, AC-06; Sources
EVIDENCE: The owner summary calls for real sandbox pushes, but Apple's [APNs entitlement documentation](https://developer.apple.com/documentation/bundleresources/entitlements/aps-environment.md) identifies production as the default for prerelease distribution. [App Attest](https://developer.apple.com/documentation/devicecheck/preparing-to-use-the-app-attest-service.md) always uses production after TestFlight distribution, while [StoreKit purchases in TestFlight](https://developer.apple.com/help/app-store-connect/test-a-beta-version/testing-subscriptions-and-in-app-purchases-in-testflight) run in sandbox. The plan does not state what combination must pass on the signed build.
PROPOSAL: Identify the expected APNs, App Attest and StoreKit environments separately, and require readback of the signed entitlements and the corresponding provider and verifier routes. Scope the live Apple calls to the new identity and approved test installations without enabling production commerce.

FINDING: F-02
KIND: factual
SEVERITY: blocking
WHERE: Owner summary:32,34; Scope:46,48; Candidate identity and access:61; Provider separation and retries:75; Inputs before implementation and live proof:83,84,86; AC-06; Test Plan AC-06; Sources
EVIDENCE: AC-06 requires a real Play subscription purchase while line 75 excludes catalogue activation without qualification. [Play Console subscription setup](https://support.google.com/googleplay/android-developer/answer/140504) requires an active base plan for purchasing, and [Play Billing testing guidance](https://developer.android.com/google/play/billing/test) says ordinary track testers can incur charges. The plan does not give an eligible no-charge path under those limits, or prove that merely having the test build cannot open a paid flow.
PROPOSAL: Define the minimum separately approved Play catalogue setup, the exact license tester and purchasing account, and how paid checkout and production grant acceptance stay off before, during and after the test. If the required activation is not authorized, leave live Play proof pending and record the constraint for the owner.

FINDING: F-03
KIND: factual
SEVERITY: blocking
WHERE: Owner summary:30,32; Scope:46; Candidate identity and access:61; Provider separation and retries:73,77; Inputs before implementation and live proof:83-86; AC-05, AC-07; Test Plan AC-05, AC-07
EVIDENCE: The exact-identity rule does not define how a shared backend distinguishes an old client or existing registration from the new identity. `internal/auth/native.go:102-108` binds only platform, `internal/http/notifications.go:68-94` accepts only token and permission from an existing native client, and `migrations/025_weekly_reminders.sql:14-22` has no app identity or environment. `internal/store/notification_settings.go:288-297` can replace another family's token registration. A second Firebase app in the same project does not fix this: `internal/notifications/provider.go:234-240` sends to the project and token without the package restriction documented in the [FCM REST API](https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages).
PROPOSAL: Specify how the server binds a registration and its retries to a verified app and environment, handles existing clients and rows during upgrade and rollback, and rejects attempts to use another lane's token, including on a shared FCM project. Prove that misplaced tokens cannot deliver or disturb the legacy registration, and keep the pilot on the disposable backend until that passes.

FINDING: F-04
KIND: factual
SEVERITY: blocking
WHERE: Owner summary:32; Scope:46,48; Candidate identity and access:59; Account continuity and links:67,69; Inputs before implementation and live proof:83,85; AC-04; Test Plan AC-04; Sources
EVIDENCE: The plan approves only website association files on a public host and cites the existing private account link boundary. `web/src/native-runtime.ts:20-40,85-90` accepts only four exact proof routes on the configured private API origin and rejects other URLs. AC-04 could pass by observing the app launch and then show an invalid-link error. Also, Android supports API 24 (`mobile/android/variables.gradle:2`), but [Android's App Links rules](https://developer.android.com/training/app-links/add-applinks) say Android 14 and earlier ignore path rules in `assetlinks.json`; checking a file and one modern device does not establish that unrelated paths stay in the browser.
PROPOSAL: Specify the host, permitted path classes, the in-app destination and the useful browser fallback, including whether sensitive account proof URLs stay on their existing private route. Require a bounded Android manifest filter and proof of correct destination and browser fallback on supported older and current Android, with both install orders where handlers could collide.

FINDING: F-05
KIND: factual
SEVERITY: blocking
WHERE: Owner summary:28,30; Candidate identity and access:54-61; Account continuity and links:69; Provider separation and retries:77; Inputs before implementation and live proof:85; AC-04, AC-07; Test Plan AC-04, AC-07
EVIDENCE: Stopping assignments and changing association files does not say what happens to an already installed candidate. [Apple](https://developer.apple.com/help/app-store-connect/test-a-beta-version/add-internal-testers) says a tester can retain access after a group removal, and [Play](https://support.google.com/googleplay/android-developer/answer/9845334) says a paused track leaves the app installed. [Apple's association cache](https://developer.apple.com/documentation/technotes/tn3155-debugging-universal-links) can outlast the rollback, and [Android 14 and earlier](https://developer.android.com/training/app-links/verify-applinks) typically re-read association changes only on install or update. Apple also [expires TestFlight builds after 90 days](https://developer.apple.com/help/app-store-connect/reference/app-uploads/app-build-statuses), so a legacy group readback alone cannot prove a usable fallback.
PROPOSAL: Define the outcome for already installed candidates, cached handoffs and delayed provider work during withdrawal, including how an affected tester reaches a safe fallback without deleting unsynced data. Rehearse with both builds installed and stale association data, and verify that the legacy build remains usable for the pilot window before moving testers.

FINDING: F-06
KIND: factual
SEVERITY: should-fix
WHERE: Owner summary:32,34; Scope:46,48; Candidate identity and access:54-63; Inputs before implementation and live proof:83-85; AC-02; Test Plan AC-02; Sources
EVIDENCE: The existing delivery contract checks `internalOnly === true` and zero external groups in `scripts/mobile-delivery.ts:114-135`. [Apple](https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers/) excludes Internal Only builds from external groups, and [TestFlight review](https://developer.apple.com/help/app-store-connect/test-a-beta-version/provide-test-information/) requires additional information for the review. The design names an external path but does not say how reviewers or external testers would reach the private backend, or which exact evidence is required before any submission or notification.
PROPOSAL: State that internal testing satisfies this spec and defer the external path unless a separate approval defines a distinct externally eligible artifact, review access through a synthetic environment or accepted demo route, and manual invitation with automatic notification off. Preserve the legacy Internal Only delivery policy.

FINDING: F-07
KIND: factual
SEVERITY: should-fix
WHERE: Owner summary:34; Candidate identity and access:59,61,63; Provider separation and retries:77; Inputs before implementation and live proof:83-85; AC-01, AC-02, AC-07; Test Plan AC-01, AC-02, AC-07
EVIDENCE: The draft asks for exact signing fingerprints and group and track verification before preparing any provider change, although the new records and groups do not exist and [Play](https://support.google.com/googleplay/android-developer/answer/9859152) can generate the app-signing key when setting up the first release. The baseline approval in `scripts/mobile-delivery.ts:19-27,82-90,114-135` binds source and artifact data but has no approved group, store record or track to compare against; the group ID is only checked for presence. The proposed test plan can catch a wrong target after upload or distribution without proving that it was refused before mutation.
PROPOSAL: Separate planned identifiers and permissions from provider IDs and fingerprints that become observable after creation. Bind each later mutation to the read-back target, roster, operation and artifact, then test that a swapped group, track, record or stale approval is rejected before it can mutate provider state.

FINDING: F-08
KIND: factual
SEVERITY: should-fix
WHERE: Frontmatter covers; Background:38-41; Candidate identity and access:59; Account continuity and links:67,69; Provider separation and retries:73; Test Plan AC-01, AC-03, AC-04, AC-05
EVIDENCE: [Issue #42](https://github.com/LowGok/my-chef/issues/42) asks for an inventory of identifier-dependent settings. The current references omit live dependencies such as the fixed iOS verification identity in `scripts/mobile-compile.ts:278`, the Android test target in `scripts/native_android_probe.py:11,23-27`, and the iOS Keychain service names in `mobile/ios/App/App/MyChefNativePlugin.swift:19` and `mobile/ios/App/App/ProtectedShopping.swift:14`. The `covers` list also omits `web/src/native-runtime.ts`, `web/src/reminder-device.ts` and `migrations/025_weekly_reminders.sql`, which can change the routing guarantees under review.
PROPOSAL: Record the remaining identifier-dependent paths and distinguish OS application identity from compile namespace, probe IDs and local storage keys. Add the relevant client and schema paths to `covers`, preserving published migration files and documenting any compatible schema change separately from account migration.

### R-2

FINDING: F-01
KIND: judgement
SEVERITY: blocking
WHERE: Owner summary:36,38; Scope:51,53; Account continuity and links:76,78; Inputs before implementation and live proof:94,96; AC-04; Test Plan AC-04; Open Questions:127
EVIDENCE: The revision makes a new public page at `mychef.dev/app/open` and a link to Meals mandatory. The owner chose the native identifier and retained the Preview; the dispatch gave no owner decision about adding this public route to the website. The pinned `conventions/feature-documents.md:137-160` requires owner decisions for trade-offs settled in a spec. A later approval to publish the page does not settle whether the feature should require it.
PROPOSAL: Raise a short Open Question for the owner to select the public handoff and fallback or defer public association and scope proof to the private test. Record the selected route and website work dependency before requesting spec acceptance.

FINDING: F-02
KIND: factual
SEVERITY: blocking
WHERE: Owner summary:36; Account continuity and links:76,78; AC-04; Test Plan AC-04; Sources
EVIDENCE: The revision requires the proposed public URL to open the new app when the app is installed, without saying where the link must be opened from. [Apple's universal link diagnostics](https://developer.apple.com/documentation/technotes/tn3155-debugging-universal-links) say that Safari stays in the browser on same-domain navigation or address-bar entry, and that users can choose browser handling. A cold-open test from another app can pass while the same link on the MyChef site never opens the app.
PROPOSAL: Specify the supported link sources and device settings, and say which cases should remain in the browser. Verify the actual launch source and same-domain fallback alongside the cross-app cold open and warm navigation, while preserving active account proofs and the existing unsaved-edit guard in `web/src/App.tsx:190-208`.

FINDING: F-03
KIND: factual
SEVERITY: blocking
WHERE: Candidate identity and access:66,68; Account continuity and links:78; Inputs before implementation and live proof:94-96; AC-02, AC-04; Test Plan AC-02, AC-04; Sources
EVIDENCE: The revised link design calls for one Play app-signing certificate, and the signed test can use Android 15 as its newer target. [Play App Signing](https://support.google.com/googleplay/android-developer/answer/9842756?hl=en-GB) says new apps default to hybrid signing, distinguishes the three signing fingerprints for pre-17 and 17+ devices, and directs developers to add the applicable fingerprints to `assetlinks.json`. The specified test can pass on Android 14 and 15 while the Play-installed app on Android 17 fails association.
PROPOSAL: Require the complete applicable set of Play app-signing fingerprints from authenticated readback, separate from the upload certificate. Prove link handling on Play-installed builds across both the Android 14/15 path rule boundary and the Android 16/17 signing boundary, or record an owner-approved limitation.

FINDING: F-04
KIND: factual
SEVERITY: blocking
WHERE: Scope:53; Account continuity and links:74,78; Provider separation and retries:82,84,88; AC-03, AC-05, AC-06, AC-07; Test Plan AC-03, AC-05, AC-06, AC-07
EVIDENCE: The revised lane rule covers creation, refresh and reminder changes, but does not define what happens to previously issued access credentials on a different or paused ingress. `internal/http/native.go:37-55` authorizes bearer requests independently of refresh; `internal/http/native.go:141-158` also replaces the family after sign-in. The specified refresh and registration tests could pass while sign-in loses the lane or a still-valid access family keeps reaching ordinary or billing routes beyond the candidate's permitted sync window.
PROPOSAL: Define which trusted ingress value creates the lane, how the lane survives family replacement, and where existing bearer requests are checked against the lane and withdrawal state. Prove that an already-issued candidate family cannot use the legacy ingress or reach disabled routes, that any approved sync allowance expires, and that the legacy family still operates.

FINDING: F-05
KIND: factual
SEVERITY: blocking
WHERE: Owner summary:32,34,36; Provider separation and retries:84,86,88; AC-05, AC-07; Test Plan AC-05, AC-07; Sources
EVIDENCE: The revised withdrawal scenario says candidate push stops after routing is disabled, but `internal/store/notification_dispatch.go:350-382` can already have claimed a send and be waiting on the provider or writing a retry after the change. [APNs](https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns) and [FCM](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan) can deliver previously accepted messages later. The revision also does not say what happens when a verified billing callback is already in progress when its route closes.
PROPOSAL: Define the cutover for claimed, accepted and retried push work, the safe behavior of a late tap, and the outcome for in-progress or replayed billing callbacks. Exercise withdrawal with work already in flight and distinguish stopping new submissions and grants from device deliveries that the provider already accepted.

FINDING: F-06
KIND: factual
SEVERITY: should-fix
WHERE: Candidate identity and access:66,68; Inputs before implementation and live proof:94,95; Test Plan AC-02, AC-07
EVIDENCE: The revision separates creation of the top-level record from later writes, then requires signing fingerprints, a group ID or track, and a roster for each later write. A group creation can only return its own group ID after it succeeds, and [Play](https://support.google.com/googleplay/android-developer/answer/9842756?hl=en) documents signing enrollment during initial app setup and upload. The current wording can make the first preparation operation depend on its own result.
PROPOSAL: Define the preconditions and expected readback separately for each creation-stage mutation, including creating a group and the first Play upload. Require provider-generated identifiers and fingerprints on downstream operations once they exist and before making a build available.

FINDING: F-07
KIND: factual
SEVERITY: nit
WHERE: Review Log:131-134
EVIDENCE: The blank line between the delimiter and the R-1 row separates the row from the table. At `c0be3665a7791408413d56b9f15b5c86f7ccd165` the pinned `vh-adlc fresh` reports `spec_review.reasons: ["no-review"]`, although the R-1 records appear below the table.
PROPOSAL: Remove the blank line so the binary recognizes the historical row, preserve the returned records verbatim, and check that `fresh` identifies R-1 as a stale review rather than an absent one.

### R-3

NO FINDINGS

## Sources

- [Apple app information](https://developer.apple.com/help/app-store-connect/reference/app-information/app-information/): Bundle ID locks after upload.
- [Apple new app records](https://developer.apple.com/help/app-store-connect/create-an-app-record/add-a-new-app/): Roles, app name and record access.
- [TestFlight internal groups](https://developer.apple.com/help/app-store-connect/test-a-beta-version/add-internal-testers): Group assignment and automatic distribution.
- [TestFlight external groups](https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers/): Separate external review and explicit tester invitations.
- [Apple associated domains](https://developer.apple.com/documentation/xcode/supporting-associated-domains): App ID and website association file.
- [Apple App Attest validation](https://developer.apple.com/documentation/devicecheck/validating-apps-that-connect-to-your-server?changes=l_8_4_2): App ID hash and one-time server challenge.
- [Play Console app setup](https://support.google.com/googleplay/android-developer/answer/9859152): Google Play package names are permanent.
- [Android app ID](https://developer.android.com/build/configure-app-module?hl=en): Different application ID creates a different app.
- [Android website associations](https://developer.android.com/training/app-links/configure-assetlinks?authuser=19): Package and Play signing certificate for domain verification.
- [Firebase Android app setup](https://firebase.google.com/docs/android/setup): A Firebase Android registration cannot change packages.
- [Apple APNs entitlement](https://developer.apple.com/documentation/bundleresources/entitlements/aps-environment.md): TestFlight distribution uses production APNs.
- [Apple App Attest environments](https://developer.apple.com/documentation/devicecheck/preparing-to-use-the-app-attest-service.md): TestFlight uses production App Attest.
- [Apple TestFlight purchase testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/testing-subscriptions-and-in-app-purchases-in-testflight/): TestFlight purchases operate in sandbox.
- [Apple signed transaction fields](https://developer.apple.com/documentation/AppStoreServerAPI/JWSTransactionDecodedPayload?changes=l_1%2Cl_1): App bundle and transaction environment.
- [Play subscription setup](https://support.google.com/googleplay/android-developer/answer/140504): Purchasable plans must be active.
- [Play Billing test accounts](https://developer.android.com/google/play/billing/test): Test track access alone does not prevent charges.
- [Google Play subscription verification](https://developers.google.com/android-publisher/api-ref/rest/v3/purchases.subscriptionsv2/get?hl=en): Package-scoped store verification.
- [Android link filters](https://developer.android.com/training/app-links/add-applinks): Static paths and dynamic link compatibility.
- [Apple universal link diagnostics](https://developer.apple.com/documentation/technotes/tn3155-debugging-universal-links) and [Android domain verification](https://developer.android.com/training/app-links/verify-applinks): Association updates are not an immediate revocation mechanism.
- [FCM message configuration](https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages): Restrict an Android token to a package.
- [Play App Signing](https://support.google.com/googleplay/android-developer/answer/9842756): Signing fingerprints for older and newer Android and hybrid signing.
- [APNs request expiry](https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns) and [FCM message lifespan](https://firebase.google.com/docs/cloud-messaging/customize-messages/setting-message-lifespan): Previously submitted notifications can arrive after sender shutdown.
