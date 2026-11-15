---
status: draft
status_description: "Proposal requested after the notice review; additional schema/identity scope is not accepted and no implementation is authorized."
issue: null
goal: "Inactive newsletter contacts retain their userID and non-consenting state without unnecessary clear email or expired mail/proof history."
covers:
  - internal/store/
  - internal/auth/
  - internal/newsletter/
  - internal/email/
  - internal/config/
  - internal/http/
  - cmd/mychef/
  - cmd/mychef-migrate/
  - migrations/
  - scripts/
  - README.md
---

# Plan: Minimise inactive newsletter identities

## Context

### Owner summary

The owner selected stable contact IDs without automatic age deletion, necessity review when public app signup opens or the purpose changes, and verified erasure. Those choices remain. The notice review found that keeping a stable ID does not itself justify retaining clear email and every old proof/mail reference.

The proposal is to keep an inactive contact's same userID, account classification, non-consenting status and a protected email-match key. A later supplied email can be verified and matched to that ID. Clear email and unnecessary old references can be discarded once current delivery/confirmation authority ends. Withdrawal-only links must continue working. Confirmed subscribers and full app accounts keep addresses needed for their current functions.

This needs a separate schema/identity feature. Current lookup, signup and operator checks expect a real normalized email; replacing that value with a hash would break them. No migration number is allocated and no data is changed by this draft. Existing newsletter-role and website implementation can continue while production collection stays off. This proposal does not describe reduced retention as already deployed.

The matching record remains personal data. The controller still needs a supported purpose/basis and balancing conclusion, readable notice and protected key/restore operations. The recommendation reduces unnecessary exposure; it does not certify compliance. The owner must first decide whether to authorize this additional scope. Acceptance would later authorize implementation through its own tasks and independent review, with delivery and live migration subject to their existing approvals.

### Inspected source and review

Source is merged newsletter-retention app commit 49468fd2aebf6b219c3758aa0e7cecdcbe533306, whose code matches reviewed integration 405f760c713972958882b92e0ec553b9ee9adb18. Current published schema is 26. The separate production-role spec is under PR 34; this proposal changes neither its accepted no-schema scope nor its claim.

`migrations/001_initial.sql:1-6` requires unique non-null users.email. `005_email_identity.sql` requires nonempty unique email_key. `internal/store/email_identity.go:18-37` derives clear case-folded matching fields. Capture resolves by email_key (`internal/newsletter/capture.go:299-325`); verified signup binds and promotes the same row (`internal/auth/registration.go:244-253,414-431`); operator authority compares the same address (`internal/store/newsletter_lifecycle.go:21-39`). No persistent protected-match-to-ID facility exists.

Newsletter proofs/subscriptions also contain email_key. Outbox recipients remain clear after links are scrubbed (`internal/email/mailqueue/queue.go:27-35`, `protected.go:201-247`; `internal/newsletter/expiry.go:58-83`). Existing requests expire after 30 minutes and confirmation authority after 24 hours. `internal/store/newsletter.go:49-63` conservatively resets restored authority, so restore protection alone does not justify the entire retained payload.

The private website worksheet maps each field to purpose at website 7d04298459a479222400fb982f00fda5d603cfae. Private notice review found the copies factually consistent as drafts but left necessity/balancing and operational evidence unresolved. No private controller identity, recipient or credential evidence is copied into this public technical document.

## Scope

Proposed: a protected inactive-email matching representation; explicit transitions between temporary clear-address handling, confirmed subscription and inactive minimised contact; shared capture/signup/email-change/operator/proof compatibility; expiry of unnecessary references; recovery and conservative backfill tests. Keep stable IDs, non-consenting state, approved review/erasure rules and anti-replay authority.

Exclude deletion of contact IDs merely because they age, changes to full app account retention, advertising/analytics/campaign activation, beta operation, new providers, migration-number reuse or changing published checksums. No reduced-data promise may be published before the actual code and migration are verified.

## Design

### Representation and boundary

Add an explicit reviewed matching representation for newsletter-only identities, binding the userID/incarnation to a purpose-keyed, versioned HMAC of the existing canonical email form. Keep a unique match-to-owner constraint. Never use an unkeyed email hash, place a token into the clear email_key column, or fabricate a mailbox to satisfy old constraints. The additive migration must explicitly permit the reduced state while retaining the existing constraints for full accounts and clear-address states. Exact SQL layout is settled against fresh main after the owner chooses this scope; no migration number is reserved here.

Retain the stable userID, isNewsletterOnly, opaque incarnation and minimum current non-consenting status/revision/order. Retain only justified current notice/decision evidence. Locale/source and obsolete history are not required merely to match an identity and must be removed or separately justified. Existing keyed erasure barriers remain limited to preventing old-job/backup resurrection, not contact or marketing authority.

### Transition and live authority

Minimise a newsletter-only pending or withdrawn identity only when no current delivery, confirmation or account-transition authority requires its clear address. Include relevant signup/email-change/recovery work when deciding that boundary. An unexpired withdrawal-only link must not prevent minimisation or become invalid because the address is removed; preserve its opaque proof binding to the reduced identity. Confirmed subscriptions and promoted app accounts are excluded.

Use an exclusive account-work fence and a transaction that rechecks classification, incarnation, status, revision/order and outstanding authority before writing the match and removing clear references. A concurrent verification, email change, signup or fresh request either completes before the recheck or is retried against the new state. A provider call already in flight settles before the transition. Partial failure rolls back; no missing-address row may be treated as delivery-ready.

A new explicit request supplies a temporary delivery address and fresh authority. Matching it must find the same userID without public enumeration. Verification can restore the address for the newly authorized operation; verified app signup promotes the same row and never grants newsletter consent by itself. New requests do not revive expired or withdrawn permission. Email-change destinations, duplicate canonical forms, placeholder transfers and explicit erasure must consult the protected match safely as well as ordinary full-account lookup.

Invalidate old dispatch before scrubbing expired recipient/proof references. Preserve only opaque ordering/idempotency/suppression evidence needed for replay protection. Do not claim that every historical record is required; tie retention to a concrete remaining authority or justified purpose. Existing short request/proof/retry periods remain; none means no automatic deletion of the identity, not unlimited live mail authority.

### Keys, operations and restore

Keep matching secrets in the existing approved protected store, separate by purpose and version. Retain the required key versions for existing matches; losing or discarding one must fail closed rather than create a duplicate account or silently reset IDs. Rebinding to a new key needs fresh verified address input or an independently safe rotation design; a one-way token cannot be blindly rehashed without the original input. Verify recovery using synthetic fixtures and disclose this trade-off in the operational record. Do not add a provider or send secrets to a new backup destination.

Extend verified operator matching/export/erasure to the reduced state using the freshly supplied address and protected match. Erasure removes the retained identity/match while the minimum anti-resurrection marker follows its existing assessed purpose. An erased identity is not promised to be recovered under its old ID by a later signup.

Old backups may contain clear inactive addresses. Restore must apply the chosen minimisation and current authoritative journal before serving or dispatching, require fresh newsletter confirmation and preserve withdrawn suppression. Backfill only newsletter-only contacts under the same transaction/fence checks. Full accounts, beta and unrelated purposes remain unchanged. A missing key/journal, ambiguous identity, unfinished backfill or unsupported old schema keeps readiness false.

### Upgrade, rollback and release

Inspect the actual next schema and coordinate with current workstreams before authoring an additive migration. Test from schema 26 and every then-supported historical restore path. Never edit published migrations. Do not downgrade a minimised namespace into code that requires clear addresses or recompute addresses from guesses. Pause capture, retain proof/withdrawal access with compatible code and repair forward if no compatible rollback exists. No data loss, duplicate ID, revived consent or full-account modification may be used to make old code run.

Backend and website retain their own independent reviews/releases. Update the notice only when its actual representation, expiry and rights operations pass, with the controller's supported purpose/basis assessment and existing launch authority. This draft grants neither implementation nor live migration.

## Acceptance Criteria

- [ ] AC-01: Inactive newsletter-only contacts preserve their IDs and non-consenting state in the protected matching representation while unnecessary clear-address and expired proof/mail references are absent.
- [ ] AC-02: Fresh verified newsletter/app/email-change flows safely resolve the same identity without enumeration, duplicate ownership or implicit newsletter consent; valid withdrawal links and operator rights continue working.
- [ ] AC-03: Concurrency, expiry, erasure, restart, key recovery and old-backup restore cannot resurrect clear inactive data, old consent or dispatch, and do not change full accounts or unrelated purposes.
- [ ] AC-04: The additive migration/backfill and compatible rollback are independently verified, with fail-closed readiness for missing keys/journal/ambiguous state and untouched published checksums.
- [ ] AC-05: The actual notice and field-purpose/basis/retention record match the deployed representation and approved review/erasure criteria, with no claim that pseudonymous matching is anonymous or that current code already implements the proposal.

## Test Plan

| Criterion | Proof |
| --- | --- |
| AC-01 | Synthetic pending/withdrawn/confirmed/full-account fixtures; inspect every clear-address/proposal/outbox reference before/after authority expiry and repeated sweeps; ID/status unchanged, no age-based ID deletion and no inactive delivery authority. |
| AC-02 | Same-ID verified signup/newsletter confirmation, case-folded collisions, email-change placeholder transfer, replay and concurrent requests, original unsubscribe links, verified export/erasure; generic public responses and no duplicate account. |
| AC-03 | Race tests for send/withdraw/minimise/signup/expiry; process restart; missing/rotated/recovered key fixtures; current-journal restore of older raw snapshots; deleted-ID non-resurrection and full-account/analytics/reminder preservation. |
| AC-04 | Explicit additive upgrade/backfill, migration checksum checks, integrity/foreign keys, mixed authority states, partial failures and safe pause/forward-repair; final container and exact independent implementation review. |
| AC-05 | Field-purpose map against actual SQL and shared resolvers, independent notice/privacy review, controller decision on supported necessity/balancing, localized notice version/release binding and operational proof; missing evidence remains a blocker. |

## Tasks

None; additional scope is not accepted.

## Open Questions

### Q1: Should the next specification use a protected inactive-contact match instead of retaining clear email and expired history?

**Recommendation:** A. Specify the reduced representation while retaining the same userID and non-consenting state, the approved no-age-deletion/review rule and verified erasure.

| Option | You get | It costs |
| --- | --- | --- |
| **A. Protected matching and reduced inactive records** | Same future verified ID and remembered choice with less retained clear data | A separate reviewed schema/shared-identity change, key recovery and integration tests before launch |
| **B. Retain current fields with a supported necessity case** | Current implementation can remain if every retained field has a justified continuing purpose | A reasoned necessity/balancing conclusion explaining what reduced matching fails to achieve; product preference or a label alone is insufficient |

**Why this is yours:** This is additional data-model and processing scope. The owner has already chosen stable IDs and review/erasure criteria; those are not being reopened. No adequate reason for the broader clear payload was found in the inspected source/product evidence.

## Review Log

| Date | Review | Kind | Commit | Model | Isolation | Outcome |
| --- | --- | --- | --- | --- | --- | --- |

## Sources

- App 49468fd2aebf6b219c3758aa0e7cecdcbe533306, with inspected paths/lines above; no reduced matching implementation is claimed.
- Private website assessment and focused notice review at d8b8ea2, field map/copy completion at 7d04298: this proposal follows a real unresolved review issue.
- [European Commission, legal grounds](https://commission.europa.eu/law/law-topic/data-protection/information-business-and-organisations/legal-grounds-processing-data_en): necessity/balancing, consent withdrawal and separate grounds. This source does not certify this proposed representation.
