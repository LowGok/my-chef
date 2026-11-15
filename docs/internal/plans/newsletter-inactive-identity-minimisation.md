---
status: draft
status_description: "Owner approved the reduced inactive-contact scope and prepared plan through the Hub. Concrete SQL/key/restore design is being prepared for independent review before task and claim publication."
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
  - .dockerignore
  - .github/workflows/delivery.yml
  - deploy/newsletter/
---

# Plan: Minimise inactive newsletter identities

## Context

### Owner summary

The owner selected stable contact IDs without automatic age deletion, necessity review when public app signup opens or the purpose changes, and verified erasure. Those choices remain. The notice review found that keeping a stable ID does not itself justify retaining clear email and every old proof/mail reference.

Keep an inactive contact's same userID, account classification, non-consenting status and protected email-match key. A later explicitly supplied and verified email resolves to that ID. Remove unnecessary clear email and old proof/mail references once their current authority ends. Outstanding withdrawal-only links keep working. Confirmed subscribers and full app accounts keep the addresses required for their current functions.

The owner approved this prepared scope through the Hub on 15 November 2026: keep contact IDs and opt-outs, use protected matching, complete signup/confirmation/unsubscribe checks, then publish the reviewed notices and enable public signup last. Beta, public app-account signup and analytics remain unchanged. This is additional schema/identity work, separate from the newsletter role's accepted no-schema feature. This document defines its technical design for independent review; it does not claim the representation is deployed.

Matching remains personal data. The private website feature owns the supported necessity/balancing assessment, controller approval, actual provider/operations facts and localized notice. This backend feature provides the verified representation, rights and recovery contract used by that assessment. No synthetic test certifies legal compliance or real mail receipt. A reviewed technical baseline, tasks and published claim precede code; delivery and live activation retain their required gates.

### Inspected source and review

Source is merged newsletter-retention app commit 49468fd2aebf6b219c3758aa0e7cecdcbe533306, whose code matches reviewed integration 405f760c713972958882b92e0ec553b9ee9adb18. Current published schema is 26. PR 34 merged as d2df37b. The separate newsletter production runtime is implemented locally at 572435c and undergoing its own independent review; this feature does not change its accepted no-schema scope or claim. Its required delivery must precede this feature's code claim.

`migrations/001_initial.sql:1-6` requires unique non-null users.email. `005_email_identity.sql` requires nonempty unique email_key. `internal/store/email_identity.go:18-37` derives clear case-folded matching fields. Capture resolves by email_key (`internal/newsletter/capture.go:299-325`); verified signup binds and promotes the same row (`internal/auth/registration.go:244-253,414-431`); operator authority compares the same address (`internal/store/newsletter_lifecycle.go:21-39`). No persistent protected-match-to-ID facility exists.

Newsletter proofs/subscriptions also contain email_key. Outbox recipients remain clear after links are scrubbed (`internal/email/mailqueue/queue.go:27-35`, `protected.go:201-247`; `internal/newsletter/expiry.go:58-83`). Existing requests expire after 30 minutes and confirmation authority after 24 hours. `internal/store/newsletter.go:49-63` conservatively resets restored authority, so restore protection alone does not justify the entire retained payload.

The private website worksheet maps each field to purpose at website 7d04298459a479222400fb982f00fda5d603cfae. Private notice review found the copies factually consistent as drafts but left necessity/balancing and operational evidence unresolved. No private controller identity, recipient or credential evidence is copied into this public technical document.

## Scope

Include a protected inactive-email matching representation; explicit transitions between temporary clear-address handling, confirmed subscription and inactive minimised contact; shared capture/signup/email-change/operator/proof compatibility; expiry of unnecessary references; recovery and conservative backfill tests. Keep stable IDs, non-consenting state, approved review/erasure rules and anti-replay authority.

Exclude deletion of contact IDs merely because they age, changes to full app account retention, advertising/analytics/campaign activation, beta operation, new providers, migration-number reuse or changing published checksums. No reduced-data promise may be published before the actual code and migration are verified.

## Design

### SQL representation and identity ownership

Use the next available additive application migration after fresh-main coordination. Current main is schema 26; no migration number is reserved by this draft. Add a distinct `newsletter_minimised` state for newsletter-only users. Permit NULL `users.email`, `email_key` and opaque placeholder `username` only in this state, with `isNewsletterOnly=1`, no password/verified mailbox and no app profile. Normal/full-account rows retain their current required nonempty identity fields and uniqueness. Keep userID and incarnation unchanged. Unexpected app/profile data in a newsletter-only row is an ambiguity requiring operator review, not permission to erase an account.

The pinned Go SQLite implementation is 3.54.1. A disposable probe at runtime source 572435c measured `SELECT sqlite_version()` and successful `ALTER TABLE ... ALTER email DROP NOT NULL`, followed by multiple NULL insertions under UNIQUE. Use this supported transactional alteration and replace the email-key validation triggers with explicit normal/minimised-state validation. Do not rebuild/drop `users`, disable foreign keys, use writable_schema, fabricate mailboxes or put a token into an address column. Preserve all existing indexes, foreign keys, IDs and published migration checksums. Recheck the actual pinned engine in migration tests rather than assuming Python's SQLite version supports the ALTER statement.

Add `newsletter_identity_matches` with userID, incarnation, algorithm/key version and purpose-keyed email tag. Its constraints prevent duplicate match ownership. Full raw identities and protected matches share one resolver: a fresh canonical email must resolve to at most one consistent owner across both representations. A disagreement or unknown key/version fails closed. The resolver is used by capture, signup issuance/completion, email-change destination and operator authority. Low-level user creation must reject a known protected match instead of bypassing the resolver. Normal accounts without a newsletter match keep their existing behavior.

Reduced subscriptions retain current non-consenting status, revision/proved order and the justified current notice/decision evidence. Remove the redundant clear email key, locale/source and obsolete request/confirmation history when no current operation requires them. Use NULL for absent optional data, never a misleading locale or address. The private field-purpose map identifies each remaining item and its purpose. This feature introduces no automatic deletion of contact IDs. Existing finite-policy generic deployments remain supported without silently applying the selected none-policy minimisation to them; an incompatible policy change with minimised records refuses readiness.

### Purpose key and namespace binding

Reuse the existing protected proof file's stable 32-byte rate root through a separate HMAC-SHA256 key-derivation domain, `mychef/newsletter/identity-match/v1/key`. Match the existing case-folded canonical email using that derived key and a distinct `v1` email-tag domain. Persist a non-secret key identity with the algorithm version in both SQL metadata and the authoritative journal. Do not use an unkeyed email digest or reuse rate/erasure tags as identity tags.

The root is already required to remain stable for existing erasure/rate authority. This initial feature supports that immutable v1 key, not blind rotation. An absent, changed, discarded or unsupported key refuses matching, backfill and new identity creation; never interpret an unmatched token as a new customer. A later key version requires its own safe migration with the old key and fresh verified addresses where necessary. A one-way tag cannot be rehashed without its original input. Restore the approved key from its existing protected backup when recovering; do not create another provider, store or secret-transfer route.

Explicit `mychef-migrate up` with the protected keyring initializes/upgrades the matching namespace and backfills relevant existing contacts. Repeating the command completes an interrupted backfill safely. Generic empty/non-newsletter stores may initialize without a matching binding; a production newsletter namespace cannot become ready before its explicit binding/backfill is complete. Startup validates the immutable store key configuration against both metadata copies. It never silently adopts a replacement key. A SQL snapshot missing its older binding is checked against the current journal before any restored contact can be served or matched.

### Transient work and minimisation

Use the exclusive account-work fence and a transaction that rechecks classification, incarnation, current status, revision/order and active authority. Provider dispatch keeps the shared fence through its bounded provider call. Minimisation, withdrawal, confirmation/classification transitions and erasure cannot race past that call or partially change ownership. Shared transition helpers own locks once; nested self-fencing must not deadlock.

Run bounded cleanup at startup before readiness and on a one-minute idle schedule, draining eligible batches with cancellation and SQL bounds. A failed cleanup/backfill worker makes capture unavailable. The normal successful sweep removes only expired/unneeded references, not the identity. It invalidates dispatch before removing old recipients, sealed proposals or proof references. Do not promise successful cleanup during a storage/process failure or erase current verification authority to satisfy a timer.

For an existing raw newsletter-only identity, defer removal of its required clear fields while current newsletter confirmation/delivery or signup/email-change/recovery work genuinely requires them. Use actual proof/job expiries and purpose/ownership references, not the indefinite existence of a withdrawal-only token. Unconsumed signup proofs already have a keyed address tag and sealed draft; connect contact-bound signup outbox rows to their incarnation when issuing them. Expired signup, verification, email-change and recovery payloads use the existing bounded cleanup contracts; the dedicated role may call a shared expiry helper but starts no app/signup/recovery worker. Do not retain all historical rows merely because one new operation is live.

A new request for an already reduced identity supplies a temporary delivery address and fresh purpose-bound authority. Keep that address in the bounded request/proposal/outbox work needed for the requested operation; do not recreate a clear permanent user row merely for lookup. A verified newsletter confirmation may restore the required raw address for an active subscription. A verified app signup promotes the same userID, fills the normal profile/credential fields and does not grant newsletter consent. A fresh request alone never clears an opt-out or revives expired consent. Email change retains its existing full-account ID and verified placeholder-retirement semantics.

Inspect all duplicated clear addresses and personal payloads: users, subscription/proof fields, outbox recipient/data/fingerprint and provider-event/ownership references, and account-transition work. Clear or remove expired purpose-owned rows when retry/delivery authority ends; preserve only justified opaque idempotency/order/suppression data. Existing 30-minute capture, 24-hour confirmation/signup and 23-hour delivery retry horizons remain. An unconsumed valid withdrawal proof stays usable without an age limit; consumed withdrawal retry metadata may be bounded to 24 hours, after which removing it cannot regain authority or consent. Full accounts and confirmed subscriber retention are outside the inactive-identity transition.

### Withdrawal and rights without a clear mailbox

Bind withdrawal proofs to userID, incarnation, current recipient tag/key version and consent revision, not a retained plaintext address. Backfill that binding for currently valid old withdrawal links while the canonical address is available. Preserve the actual token/hash and validity; replacing the contact representation does not issue a different link or consume the current one. Confirmation proofs keep only their bounded purpose-specific payload. Old, consumed, erased, reclassified or address-changed authority cannot affect another incarnation or a later decision.

A newly supplied address plus normal operator mailbox verification is matched through the protected resolver for export/erasure. Export describes the reduced stored identity and distinguishes the freshly supplied verification address from data still stored. No public handler accepts operator authority or exposes a match, userID or classification. Erasure removes the identity and active match and uses the established minimum anti-resurrection barrier. Later explicit registration after verified erasure is not promised the erased ID.

### Authoritative state, concurrency and old backups

Upgrade the separate lifecycle journal explicitly to hold a minimal current newsletter-identity registry: userID/incarnation, opaque match/key identity, classification/retirement state, current non-consenting decision/order and minimised marker. It contains no clear mailbox, profile, proof token or mail body. This current-state record is needed to preserve an opt-out, prevent ID reuse and avoid restoring unnecessary raw fields from a snapshot taken before the latest transition. Do not retain every old decision or duplicate the raw SQL history in the journal.

Identity creation, minimisation, withdrawal, confirmation and contact promotion/retirement use prepared journal intents plus SQL commit markers, then journal finalization. A crash before/after either commit is reconciled under the exclusive fence before readiness. Unknown/unfinished intents, key mismatch, ambiguous ownership or unsupported journal version refuse capture. Preserve the existing account-deletion reconciliation. Compact superseded committed transition history only after a durable current-state checkpoint makes older SQL markers unambiguous; retain the existing assessed erasure barriers. The current identity registry follows the approved ID/review/erasure rules.

Maintain the authoritative allocated-ID high-water mark. A committed newsletter contact created after a SQL snapshot can be reconstructed from the journal as the same reduced, non-consenting userID/incarnation; an ID cannot be reassigned to another account after restore. If a candidate copy already has a conflicting incarnation or cannot safely reproduce a known later app-account promotion, refuse that restore and require a suitable current snapshot/recovery. Do not discard full-account credentials/profile or silently turn a promoted account back into a placeholder.

Restore applies current journal state and explicit minimisation before serving or dispatching. Latest withdrawn state remains withdrawn unless a later explicit confirmation decision superseded it. A restored confirmed subscription becomes pending and needs a fresh request/confirmation; restoring a snapshot never restores consent. Old links/jobs/mail are invalid, no automatic reconfirmation is sent, and erased/retired identities are not recreated. Replay a snapshot from before minimisation, withdrawal, creation and promotion in tests. A direct old-copy startup also reconciles conservatively; it is not a shortcut around the explicit restore command.

### Upgrade, rollback and notice handoff

Stop the owned service for explicit migration/backfill/key or restore operations. Test from schema 26 and all retained supported historical restore paths, with SQL integrity, foreign-key checks and unchanged migration checksums. Record actual schema/journal versions and current ID allocator in the private rollout receipt. Beta data is neither imported nor migrated. Coordinate the application migration number with other current workstreams at claim time.

Use compatible new code for an existing reduced namespace. Old code that requires clear fields must refuse its new schema/version. Pause site then backend capture, retain working proof/withdrawal access with compatible code and repair forward if there is no safe previous image. Do not restore clear inactive addresses, delete IDs, create duplicate ownership or make up finite ages to make a rollback start.

Backend and website retain separate independent reviews and delivery receipts. This feature produces the source-grounded field/purpose/expiry/rights/key/restore handoff for the private website notice review. That feature publishes the complete notice only after deployed representation and actual operations match it, then verifies authorized real signup, mailbox confirmation and unsubscribe while public collection stays closed. The website enables collection last and verifies EN/FR/NL. Normal app enrollment, beta and analytics stay unchanged. This document does not report publication or actual delivery.

## Acceptance Criteria

- [ ] AC-01: Inactive newsletter-only contacts preserve their IDs and non-consenting state in the protected matching representation while unnecessary clear-address and expired proof/mail references are absent.
- [ ] AC-02: Fresh verified newsletter/app/email-change flows safely resolve the same identity without enumeration, duplicate ownership or implicit newsletter consent; valid withdrawal links and operator rights continue working.
- [ ] AC-03: Concurrency, expiry, erasure, restart, key recovery and old-backup restore cannot resurrect clear inactive data, old consent or dispatch, and do not change full accounts or unrelated purposes.
- [ ] AC-04: The additive migration/backfill and compatible rollback are independently verified, with fail-closed readiness for missing keys/journal/ambiguous state and untouched published checksums.
- [ ] AC-05: The verified backend field/purpose/expiry/rights/key/restore handoff supports the separate private website assessment and localized notice against the approved review/erasure criteria, with no anonymous-data, deployed-behavior or live-delivery claim before its actual proof.

## Test Plan

| Criterion | Proof |
| --- | --- |
| AC-01 | Synthetic pending/withdrawn/confirmed/full-account fixtures; inspect every clear-address/proposal/outbox reference before/after authority expiry and repeated sweeps; ID/status unchanged, no age-based ID deletion and no inactive delivery authority. |
| AC-02 | Same-ID verified signup/newsletter confirmation, case-folded collisions, email-change placeholder transfer, replay and concurrent requests, original unsubscribe links, verified export/erasure; generic public responses and no duplicate account. |
| AC-03 | Race tests for send/withdraw/minimise/signup/expiry; process restart; missing/rotated/recovered key fixtures; current-journal restore of older raw snapshots; deleted-ID non-resurrection and full-account/analytics/reminder preservation. |
| AC-04 | Explicit additive upgrade/backfill, migration checksum checks, integrity/foreign keys, mixed authority states, partial failures and safe pause/forward-repair; final container and exact independent implementation review. |
| AC-05 | Field-purpose map against actual SQL/shared resolvers and recovery tests; independent backend review and accurate public runbook; private website handoff identifies its notice/controller/operational gates and exact backend source/release. Actual notice publication and real-flow approval remain that feature's gate, not a synthetic backend claim. |

## Tasks

None; concrete technical spec review precedes accepted implementation tasks and claim publication.

## Open Questions

None for the product scope. The owner selected Q1 option A, protected matching and reduced inactive records, retaining stable IDs, opt-outs, the necessity-review points and verified erasure. Actual provider/key/host/backup/mailbox facts remain the private website rollout gates. This technical baseline still needs independent review before implementation tasks and a published claim.

## Review Log

| Date | Review | Kind | Commit | Model | Isolation | Outcome |
| --- | --- | --- | --- | --- | --- | --- |

## Sources

- App 49468fd2aebf6b219c3758aa0e7cecdcbe533306, with inspected paths/lines above; no reduced matching implementation is claimed.
- Private website assessment and focused notice review at d8b8ea2, field map/copy completion at 7d04298: this proposal follows a real unresolved review issue.
- [SQLite ALTER TABLE](https://www.sqlite.org/lang_altertable.html): direct nullable-column alteration is available since 3.53.0; the pinned engine was independently probed as 3.54.1, avoiding a users-table rebuild.
- [European Commission, legal grounds](https://commission.europa.eu/law/law-topic/data-protection/information-business-and-organisations/legal-grounds-processing-data_en): necessity/balancing, consent withdrawal and separate grounds. This source does not certify this proposed representation.
