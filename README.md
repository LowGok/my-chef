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
Real event collection and provider ingestion require a later integration.
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

Schema 19 adds the constrained SQL boolean `users.isNewsletterOnly`, durable
newsletter requests, subscriptions and protected action proofs. Existing app
users keep their IDs and default to false. New email-only contacts receive an
allocated ID and true. Their password is empty and a random internal username
satisfies the existing unique constraint. Contact admission guards cover app
sessions, login, recovery and entitlements. Consent status is independent of
account type.

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
| `MYCHEF_NEWSLETTER_PENDING_DAYS` | Approved pending-contact retention, 1–3650 days |
| `MYCHEF_NEWSLETTER_WITHDRAWN_DAYS` | Approved withdrawn-contact retention, 1–3650 days |
| `MYCHEF_NEWSLETTER_OPERATIONS_READY` | Set to `1` after verifying contact export/erasure procedures |
| `MYCHEF_NEWSLETTER_COLLECT` | Set to `0` to pause new capture while preserving proof actions |
| `MYCHEF_CREDENTIALS` | Existing protected credential JSON, extended with `newsletter_service_token` |
| `MYCHEF_EMAIL_PROOF_KEYS_FILE` | Existing protected encryption keyring and separate rate key |

The service credential is 32–256 visible ASCII characters. Store it in the
protected credential file and the Sites server environment. Preserve the rate
key through key rotation because it binds durable rate and erasure records.
Retain encryption keys needed by outstanding protected messages and proposals.

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

Public capture currently reports `identityCompatibility: false`. Issue
[#14](https://github.com/LowGok/my-chef/issues/14) must supply and verify signup
promotion with the same subscriber ID and verified email-change handling for
newsletter-only destinations before that capability changes. Launch also needs
the approved controller/privacy and retention information, durable hosted
backend and backup, verified sender/reply route, service connection and release
approval. A fixture's synthetic compatibility result supports local testing.

The worker checks expiry hourly in batches of at most 100 contacts. Pending and
withdrawn contact erasure rechecks the exact email, incarnation, account type,
consent status, revision and cutoff inside the lifecycle deletion transaction.
Confirmed contacts and promoted app users remain governed by their own request
process. Expired queued requests are removed. Routine tests use fake delivery.

### Contact export and erasure

Verify mailbox ownership through the operator's established request procedure.
Record its opaque request reference. In a maintenance window with the app
stopped, use the protected SQL connection to select the exact allocated ID,
normalized email key and `account_incarnations.incarnation`. Place that
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
Its contents include the contact's email, userID and consent/proof-time history.
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
