# MyChef

A Go and React application with SQLite persistence for recipes, weekly menus,
favorites and shopping lists. The source supports local fixture-based evaluation.

## Build

Run `python3 scripts/rewrite.py bootstrap`, then `python3 scripts/rewrite.py build`.
The bootstrap verifies pinned Go, Bun and lint tool downloads. Python 3 is used
for the standard-library build helpers. Run `python3 scripts/rewrite.py test`
and `python3 scripts/rewrite.py lint` for the retained automated checks.

## Local fixture

Create a disposable database with `.test-artifacts/rewrite-tools/mychef-migrate up --db /tmp/mychef-demo.db`.
Start `.test-artifacts/rewrite-tools/mychef` with `MYCHEF_DB=/tmp/mychef-demo.db`,
`MYCHEF_ADDR=127.0.0.1:8080`, `MYCHEF_ORIGIN=http://127.0.0.1:8080`,
`MYCHEF_DEV=1`, `MYCHEF_PROVIDER=fixture` and `MYCHEF_E2E=1`. Open `/app/` on that origin.
Keep credentials outside source control. Fixture mode uses synthetic providers.
Production provider configuration requires separate operator setup.

## Native clients

The [Capacitor projects](mobile/) package the same React interface for Android
and iOS. The [Native verification workflow](.github/workflows/native-compile.yml)
compiles from the selected pull request commit with an isolated HTTPS fixture.
Installed checks run separately on an Android emulator and iPhone simulator.
They publish JSON receipts for the exact source and artifact, without publishing
the packages or using store credentials. Simulator results do not cover an
iPhone installation through TestFlight.

## Container

Run `docker build -t my-chef:local .`. The image contains the app and
migration command and runs as a non-root user. Use a durable `/data` volume for
SQLite and explicit migrations before starting the application.

## Analytics dataset

The [analytics command and data dictionary](docs/reference/analytics-dataset.md)
describe a separate SQLite extract with 1,000 synthetic participants, aggregate
reports, retention and subject deletion. Build it with
`go build -o .test-artifacts/mychef-analytics ./cmd/mychef-analytics`.
The core app-event integration uses a first-party SQLite journal and curated
extracts. Live collection stays disabled pending the recorded lawful-basis
assessment and separate activation approval. The guide records the remaining
iOS simulator and Android emulator verification. Provider ingestion remains
outside this integration.
The [catalogue ERD reconciliation](docs/reference/implemented-catalogue-erd.md)
documents the separately maintained recipe pipeline and its application links.

## License

Copyright (c) 2026 Louis Gokelaere. All rights reserved.

The MyChef Proprietary Source-Available License permits private, non-production
evaluation and private evaluation-only modifications. Commercial use, production
operation, competing services and redistribution require written permission,
subject to the platform and statutory rights stated in LICENSE.

Read [LICENSE](LICENSE) before using the source. Third-party components retain
their respective licenses and notices. Licensing requests may be directed to
[Louis Gokelaere](https://github.com/LowGok).

## GitHub delivery

Delivery runs the retained app, dependency, browser, database and container checks
for pull requests and main. PR version previews run without publication.
Canonical main pushes can publish a new exact-version image to the private
`ghcr.io/lowgok/my-chef-app` package after its access is verified and automatic
publication is enabled. First publication uses a separate owner dispatch at the
approved main commit. Normal pushes cannot initialize the package.
The release uses Conventional Commit versioning, a clean-history floor of 0.16.1,
blocking vulnerability scans, an SPDX SBOM, keyless signing and attestations.
The shared publisher pushes its image before scanning, so a failed scan can leave
an unverified image in the registry. Consume the final verified digest.

For local checks, run `python3 scripts/rewrite.py generate`, `test`, `lint`, `audit`
and `e2e`, `python3 scripts/test_postgres_import.py`,
`python3 scripts/test_sqlite_upgrade.py`, and
`python3 scripts/test_rewrite_container.py --scan`. Run Python tooling tests with
`uv sync --locked`, `uv run --locked pytest` and
`uv run --locked python scripts/audit_dependencies.py`.
SQLite upgrade tests project synthetic API-created data into the retained
version-three migrations and exercise current upgrade, backup and restore paths.
Third-party CI services and native signing inputs are outside this source edition.

## Journal newsletter capture

The published analytics migrations retain versions 19 through 23 and their
checksums. Newsletter schema 24 follows them. Disposable databases from the
unreleased newsletter schema 19 are incompatible and are refused by migration;
recreate those fixtures. Do not renumber or modify published migrations.

Schema 24 adds the constrained SQL boolean `users.isNewsletterOnly`, durable
newsletter requests, subscriptions and protected action proofs. Existing app
users keep their IDs and default to false. New email-only contacts receive an
allocated ID and true. Their password is empty and a random internal username
satisfies the existing unique constraint. Contact admission guards cover app
sessions, login, recovery and entitlements. Consent status is independent of
account type.

Normal verified signup promotes a newsletter-only contact in place, preserving
its userID and subscription provenance. The mailbox owner still completes the
normal profile and password steps. Signup does not confirm newsletter consent.
Existing newsletter action links for the unchanged address retain their original
purpose and expiry. A signup proof bound to an erased or replaced contact is
rejected; the reader must request a fresh signup link.
Queued signup requests accepted before contact erasure are discarded as well.

A verified app email change may use an address held by a newsletter-only
contact. The normal password, session and mailbox proofs still apply. The
existing app account keeps its ID, credential and data; the placeholder is
retired through the independent lifecycle journal. Only destination-address
consent transfers. Confirmed consent receives a fresh withdrawal link. Pending
or withdrawn consent needs a fresh newsletter request and confirmation.
Destinations owned by another full app account retain the usual conflict result.

### Runtime configuration

Apply the explicit migration with the migration executable and the independent
lifecycle journal before starting the application. The container's explicit
`.dockerignore` allowlist includes the newsletter production sources and SQL.

```sh
mychef-migrate up --db /data/mychef.db --lifecycle-db /journal/mychef.lifecycle
```

`MYCHEF_NEWSLETTER_MODE` defaults to `disabled`. `enabled` selects the production
configuration. `fixture` requires explicit development mode, fixture providers,
fake email and a literal loopback listener. HTTP fixture capture accepts only
addresses ending in `.test` or `.invalid`.

| Setting | Meaning |
| --- | --- |
| `MYCHEF_NEWSLETTER_ORIGIN` | Exact public HTTPS origin, default `https://mychef.dev` |
| `MYCHEF_NEWSLETTER_CONSENT_VERSION` | Published localized consent version, default `newsletter-v1` |
| `MYCHEF_NEWSLETTER_CONTACT` | Approved controller contact email |
| `MYCHEF_NEWSLETTER_PENDING_DAYS` | Explicit pending-contact policy: `none` for no automatic age deletion, or 1–3650 days |
| `MYCHEF_NEWSLETTER_WITHDRAWN_DAYS` | Explicit withdrawn-contact policy: `none` for no automatic age deletion, or 1–3650 days |
| `MYCHEF_NEWSLETTER_OPERATIONS_READY` | Set to `1` after verifying contact export/erasure procedures |
| `MYCHEF_NEWSLETTER_COLLECT` | Set to `0` to pause new capture while preserving proof actions |
| `MYCHEF_CREDENTIALS` | Existing protected credential JSON, extended with `newsletter_service_token` |
| `MYCHEF_EMAIL_PROOF_KEYS_FILE` | Existing protected encryption keyring and separate rate key |

The service credential is 32–256 visible ASCII characters. Store it in the
protected credential file and the Sites server environment. Preserve the rate
key through key rotation because it binds durable rate and erasure records.
Retain encryption keys needed by outstanding protected messages and proposals.

Newsletter-only sender overrides use `newsletter_email_from` and optional
`newsletter_email_reply_to` in that protected JSON. The selected MyChef values
are `MyChef <newsletter@mychef.dev>` and `feedback@gkldevelopment.com`.
They apply only to the six newsletter confirmation/withdrawal templates.
App/security mail continues using `email_from` and `email_reply_to`.
With no newsletter From, the legacy shared profile is preserved. With an explicit
newsletter From, an absent newsletter Reply-To is omitted instead of inheriting
the app Reply-To. A newsletter Reply-To without its From is invalid. Existing
attempted mail stops rather than retrying under a changed effective sender,
Reply-To or provider credential. Fixture mode can load the same override while
still using its fake sender; tests never need real recipient delivery.

The authenticated backend accepts only the newsletter JSON contracts through
`POST /api/v1/newsletter/subscribe`, `/confirm` and `/unsubscribe`. The Sites
Worker supplies the fixed canonical Origin and trusted source address. The
browser receives generic acknowledgement, failure and retry guidance.
Confirmation and withdrawal require an explicit POST after opening the fragment
link. Capture queues encrypted durable work before identity lookup. Identity,
consent proposal, proofs and protected mail outbox issuance commit together.
Existing outbox retry and idempotency limits apply.

Operators can request `GET /internal/newsletter/readiness` directly from the
backend with the service Bearer credential through their protected client. It
returns `ready` and boolean `checks` for each prerequisite, including storage,
email, proof keys and worker health. The public Sites gateway excludes this
route. Keep the credential in the protected client's header configuration.

The full-app runtime keeps its production `identityCompatibility: false` guard.
The dedicated `mychef newsletter serve` role uses the reviewed signup/email-change
compatibility foundation and exposes only newsletter actions plus local health
and protected readiness. It requires its sending-only credential profile, proof
keys, explicit loopback address, dedicated SQL/journal and every operational
prerequisite even when capture is paused. It constructs no remote recipe/image
provider or app, recovery, reminder or analytics worker. Its queue dispatches
only the six newsletter templates; unrelated app/security mail stops dispatch
and fails readiness. The normal full-app worker and sender rules are unchanged.

See [the newsletter role runbook](deploy/newsletter/README.md) for restricted
service/ingress templates, initialization, protected credentials, backup/restore,
exact source/image/site receipts and paused/backend-only/site-last activation.
Public launch still requires approved notices, controller/retention assessment,
actual hosted readiness and authorized real-mail verification. Local tests and
configuration flags do not establish those operational facts.

Both policies must be specified; unset values keep readiness false and zero is
invalid. MyChef's selected policy is `none` for both statuses, subject to the
separate production privacy and launch approvals. Finite policies remain
independent: `none` on one side does not disable the other side's finite sweep.
Retaining a contact does not extend a confirmation link or permit newsletter
mail. Expired queued requests and confirmation proposals/mail are invalidated
without deleting the contact. Current withdrawal links remain available until
invalidated by a later consent/identity decision or erasure.

Generic finite-policy deployments check expiry hourly in batches of at most 100
contacts. The explicitly bound MyChef namespace drains bounded batches at startup
and on a one-minute schedule. With a finite policy, pending and
withdrawn contact erasure rechecks the exact email, incarnation, account type,
consent status, revision and cutoff inside the lifecycle deletion transaction.
Confirmed contacts and promoted app users remain governed by their own request
process. Explicit verified erasure remains available under every policy.
Expired queued requests are removed. Routine tests use fake delivery.

### Minimized inactive identities

Schema 27 is additive and preserves published migrations and account IDs. With
the selected `none`/`none` policy, an inactive newsletter-only row keeps its ID,
incarnation and non-consenting choice. Its unnecessary `email`, `email_key` and
opaque placeholder `username` become SQL NULL. No address is fabricated. Current
confirmation, signup, verification, email-change or recovery authority defers
removal of fields it still needs; confirmed subscribers and full app accounts
retain their required addresses and account data.

| Stored data | Purpose and lifetime |
| --- | --- |
| User ID, incarnation, classification and minimized flag | Stable identity, admission and erasure; no automatic age deletion under the selected policy |
| `newsletter_identity_matches` version and purpose-keyed address tag | Resolve a newly supplied canonical mailbox to the same reduced contact; remove the active match on promotion, verified address change/retirement or erasure |
| Subscription status, revision/order, current notice and decision evidence | Prevent consent replay and preserve the latest choice; remove redundant clear address, locale/source and obsolete request/confirmation history when authority ends |
| Withdrawal hash, ID/incarnation, key version/tag and revision | Keep still-valid withdrawal usable without an age limit; consumed retry metadata may be removed after 24 hours |
| Protected request/proposal and purpose-owned mail work | Request queue30 minutes, confirmation/signup authority24 hours and mail retry23 hours; ended payload, recipient/fingerprint and provider references are removed in bounded maintenance |
| Independent journal version3 | Immutable key binding, complete current identity registry, opaque app ownership epochs, ID high-water, current consent evidence and prepared/checkpoint state; no clear email, profile, message or proof body |

The matching tag remains personal data. It uses a separate HMAC-SHA256 domain
derived from the existing stable rate root, not an unkeyed email hash. Only the
immutable v1 key is supported. A missing, changed or unsupported key refuses
matching, creation and readiness; restore the approved existing key rather than
silently replacing it. An ordinary full account with no newsletter-only origin
does not gain an invented historical address or ownership record.

Use the same protected key file and role environment (`none`/`none`, exact origin
and notice version) for explicit `mychef-migrate up`. It initializes/resumes the
journal binding and current-contact backfill. A completed registry refuses late
adoption of an unknown old placeholder. Runtime startup only validates an
existing binding; it never initializes or rotates one. Generic finite-policy
stores keep their existing behavior; the minimized newsletter-only role refuses
an incompatible policy change.

Every writable connection reads back `secure_delete=ON`. Initial legacy cleanup
and restored working copies use bounded VACUUM and a checked truncating WAL
checkpoint; later cleanup uses secure deletion and truncation. Logical changes
persist a physical-pending marker. A busy reader, storage failure or interrupted
attempt retains it, pauses capture and retries; healthy withdrawal authority
remains available. Each physical attempt has a five-second bound. Completion
covers the managed active DB/WAL/SHM files, not discarded storage blocks, RAM,
historical backups or the provider's separately assessed retention.

### Production readiness and sender setup

Keep website `NEWSLETTER_COLLECTION_ENABLED=0` and backend collection paused
until the separate launch decision. The production compatibility guard remains
false in this feature. A fake fixture's readiness and captured headers do not
prove production hosting, domain verification, mailbox delivery or legal approval.

For sending from `newsletter@mychef.dev`, retrieve `mychef.dev` in the existing
Resend account and record its domain ID, sending capability/status, region and
exact generated records. Use the provider's actual names, types, values,
applicable priorities and proxy settings; do not prescribe a fixed TXT/MX layout.
The current verified domain uses DKIM TXT at `resend._domainkey`, DNS-only CNAME
`rsend` to `rsend-euw1.forge.rmta.net` and DNS-only CNAME `send` to
`send.forge.rmta.net`. Public DNS confirms those records; the authenticated
provider receipt separately establishes account status, region and settings.
Do not infer a region from a hostname or add speculative TXT/MX beside a CNAME.
Copy the actual provider DKIM key rather than an example. Keep receiving and
tracking disabled and preserve website, apex MX, SPF/DMARC and other-provider
records. Sending does not require a newsletter inbox. See
[Resend domain setup](https://resend.com/docs/guides/dns/cloudflare).

The selected newsletter Reply-To is `feedback@gkldevelopment.com`. Public Zoho
MX records and the completed app-feedback specification establish a documented
destination, not an independently verified mailbox or ingestion automation.
`MYCHEF_FEEDBACK_TO` routes app-form reports outward. The current email webhook
processes outbound delivery events and does not ingest received messages. Verify
the existing mailbox/alias, monitoring and any ingestion route through the Hub's
authorized provider access; do not create a new inbox or send a real-recipient
test as part of local verification.

Before collecting real contacts, document the controller's purposes, lawful
bases, necessity and objective retention/review criteria for keeping pending and
withdrawn email/contact IDs, including whether less data would suffice. The
owner has selected the controller and public rights contact; operational mailbox
verification remains separate from those choices. Complete the localized notice,
processor/transfer record, backup/recovery and verified export/erasure procedure.
`none` disables automatic age deletion; it does not establish a lawful basis for
indefinite retention. Newsletter withdrawal does not change analytics consent.
The owner separately authorized public newsletter activation when reviewed
production readiness passes. That launch requires its own compatible runtime and
exact backend/site evidence; this feature leaves the production guard and public
controls unchanged. Campaigns and real-recipient testing retain their separate
scope and authorization.

For rollback, pause capture and stop the owned newsletter worker before changing
policy or release. Version 0.20.0 rejects `none`; keep newsletter mode disabled
when reverting to it. Do not replace `none` with positive ages to make an old
release ready, run an older finite sweeper on retained contacts, restore consent
from a backup or change shared beta mail settings. Preserve the schema-24 data,
independent journal and verified backup throughout.

### Transfer recovery and restored subscriptions

Withdrawal commits `newsletter_subscriptions.status='unsubscribed'`, invalidates
old newsletter proof/proposal and queued-mail authority, and retains the contact
identity. It does not change app or analytics consent. Dispatch checks live
newsletter authority before claiming a message. Withdrawal waits for any provider
call already holding the shared account-work fence to settle (the send timeout
is ten seconds) before it commits and reports success. Mail already accepted by
the provider cannot be recalled. Retrying an old withdrawal does not reactivate
or overwrite a newer subscription decision.

Placeholder retirement prepares an independent journal intent, then commits
the address/consent transfer, placeholder deletion and application receipt in
one SQL transaction. The independent marker is completed afterward. A failed
SQL transaction cancels its intent after rollback. An uncertain commit or
unfinished marker blocks new identity and newsletter work and cannot report
ready. The email worker checks journal readiness before claiming or sending a
message, so unsettled intents leave queued mail and attempt counts untouched.
Reconcile against the authoritative application database before retrying. If
the mail worker has stopped, restart the service after reconciliation.
An older backup's missing receipt is not evidence that an intent was rolled back.
Restore refuses unresolved intents rather than publishing an uncertain database.

A restore uses the current independent journal and existing protected key. In a
bound namespace, its latest withdrawal and allocated ID high-water override an
older SQL copy. Missing reduced contacts are reconstructed with the same ID and
incarnation, without guessing an address. Confirmed subscriptions require a
fresh request and confirmation. A known app account cannot be downgraded to a
placeholder or an older mailbox epoch; an unsuitable snapshot is refused without
discarding credentials/profile or publishing the destination. Generic unbound
stores retain their existing conservative pending reset.

Restore prepares and sanitizes an unpublished working copy. The retained source
snapshot stays byte-identical. Old confirmation/capture work and unissued signup
requests cannot replay; no mail is sent automatically. Recognizable current
withdrawal hashes for an unchanged mailbox remain usable. Superseded revisions
are acknowledgments with no authority to change later consent, and stale mailbox
bindings are discarded. A proof absent from the retained snapshot cannot be
recreated. Signing into the app does not grant newsletter consent.

Keep a verified SQL snapshot, the current authoritative journal and protected
keys under their approved access/expiry rules. Restore into a separate target
and use the current journal, never an older journal copied alongside the SQL
snapshot. For a rehearsal, use a verified current journal copy while preserving
the production authority. Do not replace the namespace with an old raw SQL file
or downgrade to code that cannot read schema27/journal3. A direct stale copy
refuses validation; use explicit recovery and forward repair. No forensic or
provider-deletion guarantee follows from these active-file checks.

### Contact export and erasure

Verify mailbox ownership through the operator's established request procedure.
Record its opaque request reference. In a maintenance window with the app
stopped, use the protected SQL connection to select the exact allocated ID,
canonical address supplied by the verified requester and
`account_incarnations.incarnation`. A reduced row has no clear stored mailbox;
resolve that supplied address with the protected identity resolver and validate
the exact current ID/incarnation. Place that
verified authority in a permission-0600 file outside source control.

```json
{
  "emailKey": "contact@example.test",
  "incarnation": "COPY_THE_CURRENT_OPAQUE_INCARNATION",
  "requestReference": "verified-request-reference"
}
```

Set the existing protected proof-key file, then run the contact command with
that authority on stdin. Substitute the verified ID and protected paths.

```sh
mychef newsletter export --db /data/mychef.db --lifecycle-db /journal/mychef.lifecycle --user-id 47 --output /protected/contact-export.json < /protected/authority.json
mychef newsletter erase --db /data/mychef.db --lifecycle-db /journal/mychef.lifecycle --user-id 47 < /protected/authority.json
```

Export creates a new permission-0600 file and refuses an existing destination.
Its contents identify the actual stored fields and current consent/proof evidence.
A reduced contact's stored email and removed optional fields are NULL; the newly
supplied verification address is explicitly separate.
Erasure records the independent lifecycle receipt and prevents older queued
capture from recreating the identity. Changed email/incarnation and promoted
app accounts fail contact authority. Console results contain opaque IDs and
status. Resume the app and process the verified request through the approved
private response channel.

### SQL group examples and restore

Use the protected SQL connection for aggregate counts. These queries distinguish
account classification from newsletter eligibility.

```sql
SELECT isNewsletterOnly, count(*) AS identities
FROM users GROUP BY isNewsletterOnly;

SELECT n.status, u.isNewsletterOnly, count(*) AS subscriptions
FROM newsletter_subscriptions n JOIN users u ON u.user_id = n.user_id
GROUP BY n.status, u.isNewsletterOnly;

SELECT count(*) AS confirmed_current_addresses
FROM newsletter_subscriptions n JOIN users u ON u.user_id = n.user_id
WHERE n.status = 'confirmed' AND n.email_key = u.email_key;
```

Create a verified snapshot before an approved migration. Restore to a fresh
application database with the retained independent lifecycle journal, then
check schema readiness, allocated IDs and consent records before service.

```sh
mychef-migrate backup --db /data/mychef.db --lifecycle-db /journal/mychef.lifecycle --file /protected/newsletter-backup.db
mychef-migrate restore --db /data/restored.db --lifecycle-db /journal/mychef.lifecycle --file /protected/newsletter-backup.db
```

A website rollback pauses or removes capture while SQL records remain durable.
The independent journal reconciles deletion receipts during restore. Database
restore is a separate operator decision. The local container check exercises
fake confirmation delivery, restart and withdrawal, alongside the existing
app backup/restore and private-artifact checks.
