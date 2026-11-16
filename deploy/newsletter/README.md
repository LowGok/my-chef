# Dedicated newsletter service

These templates prepare the reviewed newsletter role. They do not enable public
collection or prove the host, mail sender, privacy notice or backup is ready.
The full app and beta stay separate. The role binds a literal loopback address;
its container uses Linux host networking so that listener remains reachable by
the approved host ingress. Do not change it to `0.0.0.0` for port publishing.

## Resolve the operator receipt first

Keep the resolved receipt in the private website workstream. Record:

- Exact reviewed backend SHA, release version, final image digest, successful CI,
  signature identity/issuer, SBOM and provenance verification.
- Existing host identity/location, dedicated SQL and separate authoritative
  journal paths, UID 65532 permissions and exclusive service ownership. These
  paths must be outside beta, the checkout, container layers and temporary files.
- Protected sending-only Resend credential, fingerprint key, immutable proof
  keyring and separate stable rate key; no Gemini, Cloudinary or invented webhook
  secret. Record presence/scope, never their values. Keep files readable only by
  the intended operator/service identity and mounted read-only.
- Existing approved backup destination, encryption/access, frequency, expiry,
  restore owner and a successful restore/export/erasure rehearsal. Back up SQL,
  preserve the authoritative journal and required proof keys separately, and
  reconcile all deletion intents before service. Do not send subscriber data or
  keys to an unapproved backup provider.
- Verified sender domain, disabled tracking, provider region and actual storage,
  processor/transfer assessment, selected monitored rights mailbox and approved
  real-test recipient. API acceptance and `delivered@resend.dev` do not prove a
  human mailbox received the mail.
- Approved localized notice, controller/contact and necessity/retention review;
  exact consent version. A configuration flag cannot establish these facts.
- Existing eligible HTTPS ingress and method/path filtering. `ingress.conf` is
  an Nginx example for an already approved host installation, not authorization
  to install a new gateway. Verify its syntax and behavior on the actual host,
  or use the existing eligible gateway with the same boundary. Cloudflare Tunnel
  path routing alone does not restrict methods; retain the local method gate.
- Exact reviewed website SHA, artifact digest, saved/deployed Sites version,
  protected environment revision, canonical origin and notice version. Drift
  invalidates the pair and requires affected checks again.

## Initialize and start paused

Use the existing signed distroless image by its verified `@sha256` digest.
The Compose template adds no shell or package manager. Create the dedicated SQL
and journal directories as UID/GID 65532 with mode 0700. Deliver secrets through
the approved secret store with restrictive read permissions. Fill placeholders
in private copies only; examples deliberately fail startup.

Run `/app/mychef-migrate up --db /data/mychef.db --lifecycle-db
/journal/mychef.lifecycle` as an explicit one-off with the same mounts, proof-key
file, role environment, read-only root and nonroot identity. For this namespace,
provide the selected `none`/`none` policy, exact origin and notice version during
that one-off too. It installs the immutable v1 purpose binding, resumes current
contact backfill and sanitizes active files; normal runtime cannot initialize a
missing binding or adopt a replacement key. Never start a second server for a
migration, and never automatically initialize a missing authoritative journal
for an existing namespace. Initialization uses the current schema; old-schema
snapshots require the explicit reviewed migration path. Schema27 and journal3
must pass before using the new role; older code cannot be used against them.

Set `MYCHEF_NEWSLETTER_COLLECT=0`. Mark operations ready only after its receipt
is complete. Start `mychef newsletter serve` with `compose.yaml`. Health at
`/readyz` means the process is operational even while capture is paused. Direct
`/internal/newsletter/readiness` requires the service Bearer credential and must
never be forwarded by ingress. Use a protected client; do not put the credential
in a URL, command argument, trace or shell history.

Check `role=newsletter`, `mode=enabled`, the expected release version, all checks other than
`collectionEnabled` are true, `collectionEnabled=false` and
`ready=false`. Verify all three POST actions through HTTPS and confirm the
public ingress rejects other paths/methods. The three actions retain their
service-token, exact Origin and trusted source checks. Ingress forwards no
health, readiness, login, signup, static, native, fixture or webhook route.

## Activate and recover

Keep website collection off. After both source reviews, delivery receipts and
all operational/privacy prerequisites pass, stop the owned role, set backend
collection to 1, restart and require every check and aggregate `ready=true`.
Perform the authorized real signup, confirmation and unsubscribe flow with the
exact website/backend pair. Inspect receipt and headers in the approved mailbox;
keep email and proof values out of logs and receipts. Enable website collection
last only after its own readiness gate. This runbook grants no additional launch
approval and does not mark missing evidence passed.

Pause the website first, then backend capture on any failed prerequisite. Proof
actions stay available when their storage/authority is healthy. A failed worker
keeps capture unavailable; inspect sanitized diagnostics, repair the cause and
restart. The physicalCleanup check remains false after busy/incomplete file
maintenance; its bounded attempts retry on the one-minute schedule. Preserve the
pending marker and adequate owned working space. /readyz remains an operational
check; it is not permission to collect when an aggregate readiness check is false. Wrong-purpose queue work is refused without sending and stops dispatch.
Never start app, signup/recovery, reminder or analytics workers in this role.

Graceful shutdown cancels workers, bounds outstanding requests/provider calls
and releases both database locks. Stop the service for exclusive export/erasure,
policy/key changes or restore. Existing verified-mailbox operator authority is
still required. The public API never accepts operator erasure authority.

Use `mychef-migrate backup` for a verified SQL snapshot and preserve the separate
live journal. Restore a copy with `mychef-migrate restore --lifecycle-db ...` and
the authoritative journal/proof keys; never replace that journal with an older
backup. Rehearse with a current journal copy and separate restore target so the
production authority is unchanged. Check same IDs/high-water, absent erased
identities, latest withdrawal preserved, confirmed subscriptions requiring fresh
proof, recognizable unchanged-mailbox withdrawal still usable, no expired or
superseded authority and no automatic reconfirmation mail. Verify obsolete clear
canaries absent from active DB/WAL/SHM with authorized controls preserved. Missing
keys, an older app epoch or unknown placeholders must refuse publication; never
guess the current full-account address or discard its profile to proceed. Resume
paused preflight after recovery. Withdrawal and erasure are independent of app
accounts and analytics consent.

There is no earlier newsletter-only production role at initial launch. If no
reviewed none-compatible role can replace it, keep the current safe role paused
for proof actions and repair forward. Do not deploy 0.20.0 finite-only behavior
against none-policy contacts, resurrect old consent or delete IDs to roll back.
Website rollback must keep the issued proof pages and gateway available.

## Reproduce the synthetic checks

Run the repository's required build, generate, test, lint, audit, browser,
upgrade and final-container checks, then:

```sh
python3 scripts/test_newsletter_runtime.py
```

It creates disposable SQL/journal/keys, explicitly upgrades schema 23, starts
the actual executable without recipe credentials, checks pause/readiness,
restarts, key mismatch, actual one-minute cleanup, active-file removal,
export/erasure, authoritative-copy restore and worker failure. Seed
proofs are generated by a test-only helper while stopped; a separate actual HTTP
capture reaches the fake sender. This proves no human mailbox receipt or live
host/HTTPS/backup readiness. The temporary proof data is removed on exit; only
safe check names are printed. Independent implementation review binds these
checks to an exact commit before delivery.
