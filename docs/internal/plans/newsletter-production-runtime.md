---
status: in-progress
status_description: "Claim published through owner-approved PR 34 after required CI; PR 33 is merged/released. Fresh spec review R-4 passed; Runtime, purpose-scoped dispatch and recovery rehearsal are implemented locally. Independent review and exact delivery/operational receipts remain pending."
branch: feat/newsletter-production-runtime
issue: null
goal: "A durable newsletter-only runtime can safely receive the public website's authenticated capture and proof requests without exposing app routes or recipe credentials."
covers:
  - cmd/mychef/
  - cmd/mychef-migrate/
  - migrations/
  - .github/workflows/delivery.yml
  - internal/config/
  - internal/http/
  - internal/email/
  - internal/newsletter/
  - internal/auth/
  - internal/store/
  - scripts/
  - deploy/newsletter/
  - Dockerfile
  - .dockerignore
  - go.mod
  - go.sum
  - README.md
---

# Plan: Newsletter-only production runtime

## Context

### Owner summary

The public website needs a receiving SQL backend before its newsletter forms can open. This feature adds `mychef newsletter serve`, a restricted service using the existing newsletter, proof, mail and account-lifecycle code. It needs no Gemini or Cloudinary credentials and serves no app login, signup, recipe, native, analytics or fixture routes. The existing full app keeps its current behavior.

The service uses a dedicated durable production application database and separate deletion journal on the existing host. Beta remains untouched. This is the future public app identity namespace; verified app signup must use it later to retain subscriber userIDs. Beta test identities are not imported or silently merged. The role requires a reviewed compatible release, protected credentials, verified sender and contact, public notice and tested export/erasure/restore operation before capture.

The owner authorized newsletter launch and selected no automatic age deletion for pending/withdrawn contacts, reviewing necessity when public app signup opens or the purpose changes, verified erasure and minimization when no longer necessary. Those product choices do not establish a lawful basis by themselves. The private website feature owns the complete localized notice and controller assessment. No new campaign, analytics or app-enrollment permission is introduced.

Local verification and production operation remain distinct. Acceptance authorizes task-by-task code and synthetic checks. The existing launch authority applies only after this feature and the website feature pass their own independent reviews, delivery and exact-pair checks. A real mail test needs an approved recipient. No runtime has been changed or deployed by this draft.

### Background and dependency

Automatic approval review rejected direct default-branch publication. The proposed claim and accepted task plan are therefore delivered through a docs-only PR with normal CI. The owner approved this exact fallback, and PR 34 merged as d2df37b after required CI passed on the normal failed-job retry. The published claim now owns feat/newsletter-production-runtime in the managed worktree. No backend code was created before publication. This is a requested workflow exception, not a claim that direct push succeeded.

After acceptance, default main advanced to weekly-reminders merge
`7ff466952834a0d295e0d483456fa312e2b97d1b`. It adds schema
25/26 while preserving the schema-24 newsletter foundation. The current role
must initialize the current published schema, not downgrade to 24, and must not
start reminder/push workers. This is dependency integration, with no new
newsletter schema or processing scope. Main was merged without rewriting any
reviewed commit; renewed spec freshness is required before tasks.

App main b9e9699755fa752a3816bd6f8172e35c65ed16f9 contains the signed 0.20.0 identity-compatibility foundation. Retention/sender PR 33, head 2f6aaa7e9d790e32f3acd3ce9ad54bbbebcebe3a, adds explicit none policies, dispatch/withdrawal fencing and purpose-specific sender configuration. It must pass CI and merge before this feature is claimed; do not duplicate or rewrite its implementation. Its independent review passed all eight criteria, but this spec requires its actual delivery receipt before production use.

At that candidate, `cmd/mychef/newsletter_runtime.go:14-21` deliberately returns identityCompatibility false in production. `cmd/mychef/main.go:204-225` constructs the normal remote recipe provider; `internal/providers/gemini.go:45-48` requires a Gemini key. `internal/http/api.go:93-145` registers newsletter and app routes together. The newsletter-only role must avoid both dependencies and broad route exposure.

`internal/config/newsletter.go:123-137` includes collectionEnabled in readiness. `internal/http/newsletter.go:20-118` already applies bearer service authentication, exact Origin, source attribution, bounded input and proof actions independent of collection readiness. `cmd/mychef/newsletter.go` provides operator export/erase with exact mailbox/user/incarnation authority and process locks. The new role reuses these contracts.

The website's owning feature is `newsletter-public-activation` in LowGok/mychef-website. Its gateway already forwards only three same-origin POST actions using a fixed HTTPS backend and service bearer credential. Its private source and release receipts remain separate from this public app repository. This app feature owns backend code, tests, service templates and the signed image; the website feature owns forms, notices, Sites configuration and combined activation verification.

## Scope

Include the newsletter-only CLI/runtime and dedicated HTTP mux, role-specific protected email loading, production compatibility capability, fail-closed worker/readiness semantics, durable storage/service templates, operator backup/restore/export/erasure, and exact-image rollout/rollback documentation.

Exclude beta migration/restart, full-app public enrollment, analytics, campaigns, native releases, new providers/subscriptions, new mailboxes/inbound automation and new SQL schema. Do not change published migrations, weaken full-app configuration, or reuse beta secrets. Public forms and provider routing are the website/operations dependency, not effects of the CLI command.

## Design

### Command and HTTP boundary

Add `mychef newsletter serve`; preserve existing export/erase parsing and exit codes. Load common address/database/origin settings and newsletter/proof/mail settings, but never initialize recipe/image providers, menus, app signup/recovery workers, analytics or frontend assets. Shared lifecycle/startup helpers may be extracted only with unchanged full-app regression behavior. Fake role mode remains explicitly loopback-only for synthetic tests; public operation never selects fixture/dev behavior as a fallback.

Bind an explicit loopback address behind approved HTTPS ingress, require canonical HTTPS newsletter Origin, and refuse unsafe listener/config combinations. A dedicated mux serves exactly three authenticated newsletter POST routes. Local health and bearer-protected newsletter readiness are direct operator endpoints; ingress must not forward them. Reject other methods and app/static/native/login/register/session/analytics/fixture/webhook routes. No public email-delivery webhook is introduced in this role. Use existing request bounds, no-store/noindex responses, rate-source validation, response contracts and constant-time service credential comparison. Never trust browser-supplied service headers; the website proxy rebuilds them.

Use existing newsletter capture, confirmation, withdrawal, live-dispatch authority and transient expiry. Keep six newsletter templates only in this role's mail queue dispatch; refuse an unrelated app/security template if present in the dedicated namespace rather than sending it. Normal app worker semantics remain unchanged. Withdrawal retains its exclusive account-work fence across consent invalidation and waits for an already-dispatched provider call to settle; accepted mail cannot be recalled. Pause new capture without disabling proof confirmation/withdrawal.

### Configuration and readiness

Read purpose-scoped sender, sending-only Resend API key, fingerprint key, service token, proof/rate keys from the protected newsletter credential store. The role must not require a fabricated delivery-webhook secret when no webhook is exposed; refactor shared email validation without relaxing full-app webhook/security requirements. Require newsletter From MyChef <newsletter@mychef.dev> and Reply-To feedback@gkldevelopment.com in the approved deployment profile; preserve configurable generic full-app profiles. Invalid/missing fields fail closed. API acceptance alone is not mailbox delivery.

Enable identityCompatibility only for this reviewed role/release after its same-ID signup/email-change/restore dependency has merged and passed exact-source review. Preserve the false default full-app production guard until its own deployment scope authorizes changing it. This is an explicit role capability, never an environment bypass of old code. Readiness also requires explicit retention policies, controller/contact and operations/notice version, storage/journal, proof/service keys, enabled real delivery configuration and live workers. The deployment receipt must verify provider/contact/operations facts; a boolean configuration value is not independent evidence that those tasks happened.

Distinguish paused operational preflight from aggregate capture readiness. With backend collection paused, require every check except collectionEnabled true, expect collectionEnabled false and aggregate ready false, and preserve proof actions. Enable backend capture only with the website still off; require every check and aggregate ready true, then let the website feature enable public capture last. On any failed prerequisite, pause backend/site capture and retain withdrawal. Do not add a public readiness endpoint or relax a missing check.

### Durable namespace, concurrency and restore

Use a dedicated persistent production application SQL database plus separate authoritative lifecycle journal outside the container/build layer and beta directories. Explicitly initialize schema/journal, take existing process locks and reconcile journal/deletion intents before accepting work. Do not migrate/import the beta or share its database with a second process. This namespace is reserved for the future public app; the role does not expose that app today.

Start only newsletter capture, none-policy sweep/transient cleanup and purpose-scoped mail workers. Freeze settings during a worker lifetime; stop the owned service before changing policy or performing exclusive operator work. A failed/panicked worker, uncertain journal state or SQL failure makes capture unavailable. Graceful shutdown stops workers and bounds outstanding requests/provider calls before releasing locks. Restart must not duplicate accepted messages or resurrect withdrawn authority.

Retain explicit none/none production policies. Preserve finite configurations for existing generic deployments, transient expiries, same-ID promotion and verified erasure. Extend executable rehearsals for pending/confirmed/withdrawn contacts, concurrent withdrawal, queued stale mail, restart and authoritative-journal copy restore. Restored consent becomes pending, no automatic reconfirmation is sent, and old links remain invalid. Replaying an old backup must not undo erasure or restore mail authority. Never alter migration checksums.

### Host templates and delivery

Add declarative service/ingress/config templates under deploy/newsletter with placeholders only. Reuse the existing digest-pinned distroless nonroot runtime and exec-form entrypoint; do not add a shell/package manager, use an unpinned image, or replace the existing shared release action/security controls. The pinned container conventions and release profile apply to actual final-image verification. Templates describe an owned service identity, restricted filesystem permissions, nonroot/read-only image, separate writable SQL/journal mounts, bounded logs/resources, automatic restart, and loopback listener. Do not embed real credentials, private recipients, account IDs or host-local protected paths in public Git. The private operator receipt records resolved values and verifies permissions, durable mounts, lock ownership, backup frequency/expiry and restore ownership. Reuse existing platform facilities; no new paid storage/provider is selected here.

Provider/tunnel/Sites changes occur only through the owner's already-authorized operational workflow. Public ingress forwards only the three POST paths and falls back to 404. Verify direct unauthorized/malformed/hostile requests fail. Operator readiness stays loopback/private. Sender domain/DNS must use actual authenticated provider records, including the selected domain's TXT/CNAME configuration; do not add speculative MX/SPF alongside CNAMEs. Mailbox existence, monitoring and actual receipt are separate proof items. No new real test recipient is implied by deployment permission.

Build and publish using the existing public CI/signed container workflow. Bind exact backend source SHA, signature/SBOM and image digest to the private deployment receipt. The website feature independently binds its source SHA, saved/deployed Sites version, protected environment revision, notice version and HTTPS origin. Source/config/notice/image drift invalidates that pair and requires the affected review and integration checks again; a website review never marks app code reviewed.

### Upgrade and rollback

This feature follows merged retention/sender code and schema 24; no SQL migration is planned. Rehearse new initialization and existing schema-23 upgrade with disposable data. Public activation starts paused, verifies non-collection checks, enables backend while site off, verifies aggregate readiness, then enables site last. Record failure and recovery at each step without invoking real recipients.

Rollback pauses site then backend capture, preserves proof routes, SQL, authoritative journal and proof keys, and selects a previously reviewed none-compatible backend image. The old 0.20.0 finite-policy service must not run against none contacts or be made ready with guessed ages. If no earlier none-compatible role exists, pause capture and retain the current safe role for withdrawal while repairing forward; never restore old consent or discard live identities to force rollback. A website rollback preserves issued confirmation/unsubscribe pages and gateway. Do not change beta services or analytics.

## Acceptance Criteria

- [ ] AC-01: The newsletter-only command starts with only its approved credentials and exposes no full-app, fixture, provider, analytics or public operator surface.
- [ ] AC-02: Missing prerequisites and failed workers keep capture unavailable; the exact paused-preflight/backend-only/site-last activation sequence works while valid confirmation/withdrawal remains available during pause.
- [ ] AC-03: The role sends only authorized newsletter templates with the selected sender profile, preserves full-app settings, and rejects stale consent and unrelated queue work without real mail in tests.
- [ ] AC-04: Durable SQL and the independent journal preserve IDs, suppression, operator erasure and conservative restore across process restart/concurrency and compatible rollback without beta changes or automatic age deletion.
- [ ] AC-05: Reviewed host templates/runbooks, independent implementation review, normal CI and signed-image receipts bind the exact backend to the separately reviewed website and truthfully identify any remaining live-operation evidence.

## Test Plan

| Criterion | Proof |
| --- | --- |
| AC-01 | Red-first CLI/config/mux tests; process starts without Gemini/Cloudinary keys; forbidden route/method matrix; invalid listener/origin/mode and fake public startup rejection; no frontend, analytics or remote-provider construction. |
| AC-02 | Red-first readiness and worker-failure tests plus process rehearsal: paused expected boolean vector, proof actions usable, backend enabled/site still off ready true, site integration last; failures/rollback at each transition preserve withdrawal. Missing key/contact/operations/storage/notice settings refuse capture. |
| AC-03 | Captured fake Resend payloads for all six templates, wrong-purpose queue rows, legacy/shared/full-app config tests, stale proofs/revisions and blocked-provider withdrawal race; no actual provider call. |
| AC-04 | Existing retention/signup/email-change/operator/race suites plus dedicated role process init, schema-23 upgrade, restart, backup/copy restore with authoritative journal, erasure replay, lock contention, shutdown/restart and compatible pause rollback. Compare beta PID/health and analytics/full-app guard before/after. |
| AC-05 | Run required build/generate/test/lint/audit/e2e/upgrade/final-container checks and independent review at exact app commit; CI and signature/SBOM verification; inspect resolved private host/backup/contact/provider receipt and exact backend/site pair. Unavailable live proof is a named blocker, not a pass. |

## Tasks

- [x] T1 `internal/config/newsletter_email_test.go` - add failing tests for a newsletter-only protected sender profile without fabricated webhook settings, missing/invalid mail keys, fake-mode limits and unchanged full-app parser requirements (AC-01, AC-03).
      Evidence: targeted Go config tests failed to compile with undefined LoadNewsletterEmail, establishing the absent role loader. Full-app validation, fake-mode limits and sender-only fields are asserted. Transcript: private staging evidence/role-t1-red.log; no real provider call.
- [x] T2 `internal/config/email.go` and `internal/config/newsletter_email.go` - implement the role-specific mail loader while preserving normal app validation; T1 and existing email tests pass (AC-01, AC-03).
      Evidence: all config tests and uncached config race tests pass, including T1, six-template profile validation and unchanged full-app webhook/shared-profile requirements. The new role loader accepts only explicit sending mode and its protected profile, bounds input and does not invent a webhook secret. Staging role-t2-config.log and role-t2-race.log. No provider call or production configuration.
- [x] T3 `cmd/mychef/newsletter_serve_test.go` and `internal/http/newsletter_only_test.go` - add failing CLI/mux/config/readiness/shutdown tests, including no recipe credentials, forbidden app/native/operator routes, paused check vectors and proof actions (AC-01, AC-02, AC-04).
      Evidence: uncached targeted Go command tests and HTTP tests fail on the missing role loader, worker fence, server and dedicated mux. Tests cover loopback/config refusal, actual listener shutdown and lock release, protected paused readiness, forbidden routes and valid proof actions while paused. Transcript role-t3-red.log. Manual docs-impact review: this accepted feature document covers these contracts; operator README additions remain in T7.
- [x] T4 `cmd/mychef/newsletter.go`, `cmd/mychef/newsletter_serve.go` and `internal/http/newsletter.go` - implement the narrow role/mux and current-schema journal/worker wiring, explicit production capability and safe shutdown without normal app/reminder/analytics startup; T3 and full-app regressions pass (AC-01, AC-02, AC-04).
      Evidence: T3 targeted tests pass; uncached cmd/mychef, HTTP and configuration regressions pass, and newsletter command/HTTP race suites pass. A real local listener starts with synthetic sending-only credentials and no recipe credentials, reports the paused check vector, rejects app/native/webhook/fixture routes, processes valid proof actions while capture is paused and releases both locks on cancellation. Protected readiness binds role/mode/version. Transcripts role-t4-target.log, role-t4-regression.log and role-t4-race.log. No public runtime changed. Manual docs-impact review: operator startup/deployment documentation remains the explicit T7 task.
- [x] T5 `internal/email/newsletter_only_test.go` - add failing tests for unrelated queue rows in the role and capture exact six-template profile/withdrawal/retry behavior with fake transport (AC-03).
      Evidence: uncached email tests fail to compile because the role-specific NewNewsletter constructor is absent. Tests require no app-template delivery and failed readiness, all six live-authority templates with captured fake payloads and stable retry, stale/expired suppression and withdrawal waiting for the provider fence. Transcript role-t5-red.log. Manual documentation review: dispatch policy is covered by this accepted feature; deployment instructions remain T7.
- [x] T6 `internal/email/service.go`, `internal/email/worker.go` and minimal mailqueue helper - restrict the dedicated role to newsletter templates while preserving normal workers; T5 and concurrent withdrawal/expiry/transfer tests pass (AC-03, AC-04).
      Evidence: all email, newsletter and command suites pass uncached, and all newsletter email race tests pass. The dedicated worker commits an unexpected template as refused without contacting a provider, then stops so readiness fails. Six-template fake payloads use the selected profile; retry bytes remain stable; stale/expired authority never sends; withdrawal waits for an in-flight provider. Normal worker/profile regressions pass. Transcripts role-t6-target.log, role-t6-email.log and role-t6-race.log. Manual docs-impact review: no additional public interface beyond the T7 role runbook.
- [ ] T7 `scripts/test_newsletter_runtime.py`, `deploy/newsletter/`, `.dockerignore` and `README.md` - add a reproducible disposable executable/init/restart/backup/restore/erasure/lock/pause rehearsal, restricted deployment templates, narrow production-file Docker allowlist entries and exact-source/image/site receipt instructions; run required build/generate/test/lint/audit/browser/upgrade/final-container checks and independent review before delivery (AC-01, AC-02, AC-03, AC-04, AC-05).

      Prepared: the actual executable rehearsal passes fresh initialization/schema-23 upgrade, pause/readiness/proof actions, backend-only capture, fake dispatch, locks/restart, protected export/erasure, authoritative-copy restore and failed-worker recovery. Build, generate/deterministic API, complete repository tests, lint, audit, all browser suites, PostgreSQL import, SQLite upgrade/restore and sequential menu restart pass. The final nonroot/read-only image passes the dedicated role/restart/route matrix, existing app/container journeys and HIGH/CRITICAL scan. Compose syntax/security/mount checks pass. Full race checks passed the affected packages but failed unchanged analytics cancellation and timed out the unchanged dataset CLI after 20 minutes; isolated reruns are pending. Earlier required CI at PR 33 passed the same analytics sources with its race step taking 16m21s; no test, timeout or CI gate was weakened. Private staging evidence/role-t7-*. No production service or collection setting changed. Manual documentation review updated README and the role runbook; live ingress, backup/mailbox/provider evidence, independent review, exact CI/signature/SBOM receipt and website pairing remain unavailable and T7 stays unchecked.

## Open Questions

None for the technical scope. Controller, contact, retention-review criteria and conditional newsletter launch authority are recorded in the private owner workstream. The website feature owns complete notice/necessity/basis assessment and its approval. Real mailbox/provider/backup/HTTPS readiness remains an operational prerequisite; no synthetic test is labeled real delivery. The child and website specs need loop acceptance before code work.

## Review Log

| Date | Review | Kind | Commit | Model | Isolation | Outcome |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-11-15 | R-1 | spec | `4c3722702a4a45f3b37350281f5fdf3bfbe2cc71` | unknown | procedural | Review stopped because required pinned container pages were absent from cache. Model identity not attested. |
| 2026-11-15 | R-2 | spec | `8e5cdee68f34719d89dc4d905beb2bb0870b159f` | unknown | procedural | No findings after complete pinned-source review. Model identity not attested; owner acceptance pending. |
| 2026-11-15 | R-2 | spec | `8e5cdee68f34719d89dc4d905beb2bb0870b159f` | n/a | n/a | accepted by owner |
| 2026-11-15 | R-3 | spec | `946c91c5cd424f35ac8d0d660d5266d064050a20` | unknown | procedural | No findings after current-main/schema-26 freshness review. Accepted normative baseline unchanged; model identity not attested. |
| 2026-11-15 | R-4 | spec | `d2df37b9e1c1ee59c55a11bf93a671055d00ef83` | unknown | procedural | No findings after merged dependency and published claim; accepted normative baseline unchanged. Model identity not attested. |

### R-1

FINDING: F-01
KIND: factual
SEVERITY: blocking
WHERE: Design > Host templates and delivery; Acceptance Criteria > AC-05; Test Plan > AC-05
EVIDENCE: The pinned docs README.md routes container work to conventions/containers.md and knowledge/container-release.md. Reading both from /home/lowgok/.cache/vh-adlc/docs/d8f07c9dd69038375a535af8ad586086688f85ab returned "No such file or directory". This review therefore cannot verify the proposed container/service templates against their required pinned conventions. The reviewer instructions require stopping when a required convention page cannot be read.
PROPOSAL: Make both pages available at the exact pinned revision and rerun the independent specification review before acceptance.

### R-2

NO FINDINGS

### R-3

NO FINDINGS

### R-4

NO FINDINGS

## Sources

- Pinned `conventions/containers.md` and `knowledge/container-release.md` at d8f07c9dd69038375a535af8ad586086688f85ab: distroless nonroot, immutable inputs, explicit security profile and final-image/signature/SBOM verification; fetched unchanged after R-1 cache preflight.
- App main b9e9699755fa752a3816bd6f8172e35c65ed16f9 and retention/sender PR 33 at 2f6aaa7e9d790e32f3acd3ce9ad54bbbebcebe3a: the source paths and behavior inspected above; no dependency merge is claimed.
- The owner-directed newsletter launch and privacy/contact/review decisions were verified through the private Hub workstream on 15 November 2026. Private evidence remains outside this public source repository.
- LowGok/mychef-website newsletter-public-activation feature: website-owned gateway, notices, UI and paired activation; independently reviewed local spec, not a live-backend claim.
