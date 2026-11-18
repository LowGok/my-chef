---
status: draft
status_description: "Specification for review; no store or application changes authorized."
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
  - docs/reference/
  - README.md
---

# Plan: MyChef native app identity

Prepare `dev.mychef.app` for iOS and Android and use those same store records for a private beta and the eventual release. Keep the current Preview as a separate app during the transition.

## Context

### Owner summary

The owner chose `dev.mychef.app` for both platforms, for private beta and the eventual release. That choice avoids setting up another temporary identity before release. Apple locks the bundle ID after the first upload, and Google Play treats a different package as a different app. The installed Preview will still work, but the new app needs a fresh installation and sign-in.

People keep the same MyChef account and server data. They should let pending shopping changes sync in the old app before starting with the new one. The new install starts with its own session, shopping cache and notification permission. The current Preview, its tester access and its subscription tests stay in place until there is a verified alternative. The old installation is also the fallback if a pilot fails.

First prove both identities in public CI with test fixtures, then verify private signed builds against the new store records using synthetic accounts. Load only the non-secret link association files on the nominated website paths after approval; the backend stays private. Send real sandbox pushes and exercise store sandbox purchase and restore with the new app identity. Tests on the old identity and Xcode or Android fixtures cover different things and cannot stand in for this proof.

The spec does not authorize creating store records, changing app access, publishing a website or build, moving testers, or opening paid checkout. Before each of those steps we need the exact target and separate authorization. Public store release, the recipe pipeline, and analytics and automated emails stay outside this work.

### Background

- The [identity issue](https://github.com/LowGok/my-chef/issues/42) calls for a move from `com.gkldevelopment.mychef.preview`. The owner selected `dev.mychef.app` for both platforms in the specification discussion.
- At public main `9a4079667ddc2bdfe32ae6455ec60d0d68b4eb98`, the Android package and Java namespace are defined in `mobile/android/app/build.gradle:23-26`; resource names are in `mobile/android/app/src/main/res/values/strings.xml:5-6`. The iOS build settings use the same ID in `mobile/ios/App/App.xcodeproj/project.pbxproj:328,350`. Capacitor generation in `scripts/mobile-build.ts:15-41` uses that ID with a separately configured private API origin. `scripts/mobile-delivery.ts:5` also pins the ID for delivery.
- The Android app disables backups (`mobile/android/app/src/main/AndroidManifest.xml:5`) and encrypts the shopping payload using an account ID and instance from its local app state (`mobile/android/app/src/main/java/com/gkldevelopment/mychef/preview/ProtectedShopping.java:34-67`). Native sessions are separate server bindings (`internal/auth/native.go:74-114,141-217`).
- Push reads settings for Apple and Google from private runtime configuration (`cmd/mychef/notifications.go:19-65`) and sets the APNs topic on the request (`internal/notifications/provider.go:257`). The native verification matrix builds both platforms (`.github/workflows/native-compile.yml:14-58`). As checked on 18 November 2026, [weekly reminders](https://github.com/LowGok/my-chef/pull/11) had merged and the [native billing PR](https://github.com/LowGok/my-chef/pull/37) was open and draft at `8904a7a`. Billing and App Attest checks on the new identity depend on the reviewed billing contract and existing sandbox results; this work does not substitute for them.
- Apple and Google identify uploaded builds by their app IDs. Firebase cannot rename a registered Android package. See the provider sources below. This is a new app installation and record on each store. Changing a display name or the backend URL alone does not move the app identity.

## Scope

Cover both native build identities, store and tester isolation, signing, private backend targeting, website link association, push, and sandbox billing compatibility. Reuse the current Apple and Play accounts and the existing approved Firebase project where suitable. Keep code publication separate from provider setup; private operational values and actual tester details stay outside Git.

Excluded: replacing the current Preview in place, bulk tester invites, exposing the private backend or its database, copying app-local secrets or unsynced files, production billing or App Store/Play release, public distribution links, provider or DNS changes for marketing or email, analytics, and the recipe pipeline. There is no account database migration for the identity change.

## Design

### Candidate identity and access

| Lane | iOS bundle ID / Android application ID | Distribution |
| --- | --- | --- |
| Existing MyChef Preview | `com.gkldevelopment.mychef.preview` | Existing groups, tracks, builds, API routing and tests |
| MyChef private beta and future release identity | `dev.mychef.app` | Private TestFlight and Play test tracks only during this work; no automatic distribution |

Select the lane explicitly in the build. Validate the selected app ID against Capacitor, Gradle (application ID, namespace/class resolution, flavor definitions, resources and intents), Xcode (all configurations, entitlements, schemes and URL types), native test selectors, and the artifact manifest. Validate the API origin separately. Fail on missing or inconsistent values before signing, uploading or running a live test. Do not infer store or backend targets from the branch, hostname, or display name. The legacy lane stays available from the existing source and can still tag which artifact it produced. Keep current #37 tests and approved distributions on the old lane.

Verify availability and exact team, app/package, signing fingerprints, tracks and group roles using read-only access before preparing any provider change. Use separate App Store Connect and Play records under the existing accounts after approval. On Apple, verify the new explicit App ID, matching provisioning profile and capabilities, with push topic and App Attest scoped to that identity. On Google, verify Play App Signing versus upload signing and register a new Android app in the approved Firebase project; keep its registration config outside the public repo. Confirm the new TestFlight and Play groups with an exact approved roster; use internal groups or tracks for the first live tests, with TestFlight automatic distribution off. Any approved external TestFlight pilot uses a separate named email-only group, requires Apple's approval for the build, and does not grant App Store Connect roles. No external invite, upload, review submission, or activation is implied by this spec.

Public GitHub Actions runs secret-free variants for both identities, including Android, iOS simulator and unsigned iOS device compile. Use the existing approved protected signing path separately. Only the scoped service identities and operator approvals for the selected environment can perform store and provider changes. Record those approvals, artifact hashes and provider readbacks privately, without secrets or tester addresses in the repository.

### Account continuity and links

First use disposable accounts on a private backend that matches the candidate contract. Before any approved move of existing testers, confirm the new lane can use their same authorized private account backend, not a copy that could diverge. Keep the current route and API behavior available while both apps run. A fresh installation signs in normally and loads saved recipes, favorites, plans, synced shopping changes and server-side entitlement from the current account. Do not change the account ID, import a session or share keys or caches across installs. Tell the tester how to confirm shopping edits have synced in the old app; if offline or pending, keep the old installation and wait before switching. Use existing server write and shopping sync semantics for overlapping changes; no import or second account is created.

List the supported website link paths and their owner before deployment. Associate the Apple App ID with a narrow path set via the AASA file and the Android package with the Play app-signing fingerprint via Digital Asset Links. Keep the existing public website, unrelated paths, recovery behavior and the legacy routing intact. Exercise the existing native navigation boundary (`mobile/ios/App/App/NativeRuntime.swift:6`) for both platforms. Public association files contain only identifiers, site path policy and the public certificate fingerprint. Private rehearsal can use signed debug association proof plus a separate hosted fixture; a public association publishes only after approval for the exact host and path set. Do not claim two apps can reliably own the same URL: assign each path one intended handler, verify that with both apps installed, and use browser fallback where a handoff is intentionally unavailable. Unassociated and unsupported URLs stay in the browser.

### Provider separation and retries

Treat store, app identity and environment as part of push and sandbox billing authority. Register devices afresh and only after opt-in. Never copy old tokens into the new lane. If both identities need service on the same private account backend, require exact identity-to-provider routing and explicit verified registration on each lane before moving a tester. If the shared backend cannot prove separation, stop the pilot at the disposable environment. The existing per-opted-in-install policy stays in force; the tester can disable reminders for the old installation after checking the new one. Retries and replay preserve the same registration/delivery identity; delayed or mismatched tokens do not fall through to another topic, project or lane.

Use the reviewed [billing feature](https://github.com/LowGok/my-chef/pull/37) before the real store phase. Recreate only approved sandbox products and routes for the new store records; match the Apple app ID and attestation environment, Google package and plan identifiers, and the approved store/environment when validating purchases, callbacks or restores. A real sandbox purchase and restore must be proven on the new record of each platform; a restore from the legacy store app does not become a purchase on the new app. Existing server-granted Premium stays attached to the account. No production purchase or grant, catalogue activation, introductory offer, or paid checkout is enabled.

Track provider operations as one change set. Acquire one operator reservation per lane; before retries, read back the exact provider IDs and configuration to reconcile operations that may already have succeeded. Store record names alone are insufficient. If signing, review, link verification, push or store checks fail on either platform, keep that lane private and unpromoted. For rollback, stop new assignments and disable only the new lane's affected link, push or sandbox billing routing where applicable. Keep the old app, group, API and account state active. Verify a tester can reopen the old build and see changes already synced by either app; retain any local unsynced state in its originating install. Do not delete the new record or try to recycle the reserved app ID.

### Inputs before implementation and live proof

| When | Verify or obtain | Owner involvement |
| --- | --- | --- |
| Before provider setup | Verified Apple app-management and Play Organization access, identifier availability, Firebase app-registration access, selected store records and website path owner | Authenticate only if existing access fails; approve specific record, profile and provider changes after seeing the plan |
| Before signed test | Approved protected signing route, profile and package signing custody, private origins, exact group and licence scope, explicit sandbox products and callback credentials | Approve the exact build upload, group and track; hand off any needed credentials through a verified private flow |
| Before a tester move | All criterion evidence, readback of the exact approved recipients and access, site association and rollback drill | Approve publication of association files and tester moves separately; a final store release needs separate approval |
| During verification | CI, simulator and device relay first; supported Apple device for real App Attest and sandbox checkout, licensed Play target for real billing, signed device for actual push where a fixture cannot prove it | Ask for one combined check only for device behavior unavailable through the approved test rigs |

## Acceptance Criteria

- [ ] AC-01: Both native platforms package as `dev.mychef.app` for the new private lane; the original Preview lane can still be built independently, and inconsistent identities or endpoints are rejected before delivery.
- [ ] AC-02: Authorized testers can install the signed candidate beside the legacy app through the exact approved private groups or tracks; automatic distribution stays off and no other group, track or public audience receives it.
- [ ] AC-03: Existing accounts sign in on the new app and see the same server-owned data and Premium grants. The two installations use separate credentials and device-local state, and pending offline shopping changes are preserved until they sync from the original installation.
- [ ] AC-04: Approved site links open only the intended native app when both are present and fall back to the website when it is absent. Other links, the current website and legacy app routes retain their behavior.
- [ ] AC-05: New installations receive a real opt-in push on each platform, through the matching app identity, without delivering to unrelated or opted-out installations. Users can still manage reminders on the legacy app.
- [ ] AC-06: Actual Apple and Google sandbox purchase and restore work on their respective new signed store apps; a wrong app or environment cannot attach a purchase to the new lane, and live checkout remains off.
- [ ] AC-07: A partially failed candidate handover leaves legacy distribution usable. Retrying or withdrawing only the candidate does not duplicate store resources, expand access or lose synced account data.

## Test Plan

| Criterion | Proof |
| --- | --- |
| AC-01 | Run Delivery and the public native matrix (Android, iOS simulator, unsigned iOS device) for both lanes. Inspect packaged IDs, intents, entitlements and origin fixtures against the lane manifest. Intentionally mismatch the measured app ID, lane or allowed origin and confirm the packaging or delivery guard refuses it. |
| AC-02 | Compare signed artifact IDs, entitlements and fingerprints with authenticated store reads. Install each new build on an approved target with the old one still installed. Verify groups, testers, track, build and auto-distribution flags; query other groups and tracks for absence of the candidate and verify an unlisted test identity has no access. |
| AC-03 | On both platforms, seed a synthetic existing account with a saved recipe, favorite, week, shopping entries and server Premium. Create an offline shopping edit in the old app, launch the new app signed out, reconnect and sync the original edit, then sign in and check the same ID and data. Make overlapping writes and relaunch both; revoke one session and verify the sibling session and private cache behavior. Confirm the new install receives no old local state. |
| AC-04 | Validate the association documents over HTTPS, path restrictions and the real intended certificate/Apple identity. On each signed target test approved and rejected URLs with both apps installed, then with the new app removed. Verify browser fallback, existing unrelated links and legacy behavior. Test the end-to-end on a controlled host before any approved public association update and recheck after it. |
| AC-05 | In the private target, exercise registration, allow/deny, replay and mixed-lane routing with fake senders, then register and deliver one real notification on each signed platform. Verify sender acceptance and device receipt separately, confirm the alert opens the right app, and use a co-installed opted-out control plus a legacy opted-in control. |
| AC-06 | Exercise mismatch rejection and callback replay with fake providers in CI. In the isolated billing target run an Apple App Attest-backed sandbox purchase and store restore and a Play-licensed test purchase and restore. Verify exact app identity, acknowledged server-side entitlement, replay stability, rejection for the wrong app/environment and ordinary checkout still disabled. Record redacted provider readbacks. |
| AC-07 | In disposable fixtures interrupt setup after one provider write, retry from readback and verify only the intended scoped resource exists. Simulate failed candidate verification, rollback candidate-only routing, read back unchanged legacy group/track and restart the original app against its existing account; confirm synced edits survive. Rehearse the pilot withdrawal with the still-installed legacy app before moving testers. |

## Tasks

None yet; written once the document is planned.

## Open Questions

None. The owner selected one new identity for both platforms, with the current Preview as fallback. Later live operation approvals do not reopen that product choice.

## Review Log

| Date | Review | Kind | Commit | Model | Isolation | Outcome |
| --- | --- | --- | --- | --- | --- | --- |

| 2026-11-18 | R-1 | spec | `49876561b72921b80e9082eb02add453fb99a39b` | unknown | procedural | Five blocking and three should-fix findings |

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
