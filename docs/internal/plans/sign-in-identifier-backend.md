---
status: in-progress
branch: feat/sign-in-identifier
status_description: "Implement the accepted backend changes and verify them before requesting review."
issue: https://github.com/LowGok/my-chef/issues/41
goal: "Sign in to the same existing app account through the current API using its email address or a uniquely matching username."
covers:
  - internal/auth/
  - internal/http/
  - internal/importer/
  - internal/store/
  - migrations/
  - cmd/mychef/
  - cmd/mychef-migrate/
  - go.mod
  - go.sum
---

# Plan: Email or username sign-in backend

## Context

### Owner summary

This backend change lets the existing sign-in API accept a username where it
accepts an email. Either identifier reaches the same account and counts toward
its same attempt limit. Email verification, email-only recovery, existing
credentials and session handling stay in place.

The owner selected Unicode case folding: `Pantry` and `pantry` match, as do
`Étage` and `étage`. Accents are not removed. New registrations and renames
cannot take another account's folded name. If older accounts already collide,
each can still sign in with its own email and change its username. No account
is merged or renamed by the upgrade.

The backend will add an indexed comparison key to SQLite. The test plan includes
a disposable upgrade and backup-based rollback because the prior binary will
refuse the new schema. The independent spec review found no issues. The owner accepted
that revision for backend implementation. Merge and rollout require separate authority. The web
form and the final iOS and Android keyboard, autofill and password-manager
checks remain later work for #41.

### Background

- The existing endpoint accepts JSON with `email` and `password`, charges 15 attempts per source and 10 per supplied email key, and calls the session manager (`internal/http/api.go:443-466`). The limiter uses one-minute process-local windows (`internal/http/api.go:288-321`).
- Both transports ultimately use that handler: the native prefix routes to the web API path with native request context (`internal/http/native.go:13-23`), and the guard and signed-in response honor that context (`internal/http/api.go:328-350`, `internal/http/api.go:468-483`).
- Usernames are currently stored under exact uniqueness, and app registration and profile edits already reject surrounding whitespace and cap usernames at 80 bytes (`migrations/001_initial.sql:1-5`, `internal/auth/manager.go:107-124`, `internal/auth/manager.go:243-249`). This does not prevent ASCII case variants from coexisting.
- Current lookup excludes newsletter-only rows. Session completion rechecks the matched user ID, password and canonical email in the write transaction, and response handling preserves verification restrictions (`internal/auth/manager.go:136-203`, `internal/http/api.go:468-483`). Recovery validates email independently (`internal/auth/recovery.go:31-51`).

### Review preparation

The baseline passed `go test -p 1 ./internal/auth ./internal/store ./internal/http`
at `69523dba413b267281af834d51c755bee17f0fd6`, before any app code for this
feature. A separate in-memory SQLite fixture exercised the proposed ASCII
comparison and found the index in the query plan. These checks did not exercise
the new sign-in path.

The first independent review asked for a username policy decision and four
checks. The owner selected Unicode case folding with prevention of new
collisions; the revised test plan covers the four checks:

- Rehearse applying and rolling back the index on a disposable copy. The old
  binary checks for its exact schema revision at startup
  (`cmd/mychef/main.go:140`; `internal/store/migrate.go:78-101`).
  Test recovery to a compatible database with matching lifecycle state before
  any separately approved release.
- Test username ownership both before committing the sign-in transaction and at
  the handoff to native session creation. Native sign-in adopts the committed
  session separately (`internal/auth/manager.go:163-210`;
  `internal/auth/native.go:72-99`; `internal/http/native.go:141-158`).
- Test the generic response on both rejected syntax and a valid-looking but
  unmatched identifier. Password padding should cover eligible lookups without
  sending rejected syntax through the password-work queue
  (`internal/auth/manager.go:136-147`; `internal/auth/password.go:153-177`).
- Race an email request and a username request from different sources for the
  account's last attempt. The existing limiter checks and counts one key under
  a mutex (`internal/http/api.go:288-311`).

### Policy-independent sign-in baseline

During spec preparation, a temporary Go test overlay exercised the existing HTTP handlers with disposable
SQLite accounts at `69523dba413b267281af834d51c755bee17f0fd6`. All five probes
and six existing HTTP tests passed on an uncached run:

| Probe | Observed result |
| --- | --- |
| `TestIssue41BaselineEmailSignInAcrossTransports` | Case and surrounding Unicode whitespace variants resolved to the same user ID on web, iOS transport and Android transport. Sign-in invalidated the previous anonymous credentials. |
| `TestIssue41BaselineGenericCredentialsAndNoSessionMutation` | Wrong passwords, absent mailboxes, invalid email syntax and empty passwords produced the same 401 code and message on both transports, without changing either session. Request IDs remained present. |
| `TestIssue41BaselineSharedCanonicalEmailAndSourceLimits` | Both transports shared a ten-attempt canonical email limit. After nine attempts, exactly one of two concurrent requests from different sources proceeded; the other received 429. One source could make fifteen attempts across inputs and both transports; the sixteenth returned 429 with `Retry-After: 60`. A separate account and a separate source were unaffected. |
| `TestIssue41BaselineVerificationAndEmailOnlyRecovery` | An unverified email login returned a restricted session without profile data on both transports. Recovery rejected a plain username without queuing work. After the synthetic user was marked verified, the browser could sign in and load that user's profile. |
| `TestIssue41BaselineEmailShapeAlwaysUsesMailbox` | A stored username matching someone else's email could not redirect sign-in, and an email-shaped username without a matching mailbox could not sign in. Both accounts could still sign in with their own emails. |

The same focused run also passed seven existing auth and store tests covering
session rotation and revocation, stale credential rejection, native verification,
recovery's verified recipient and persistent limits, and email identity migration
and collision behavior. This evidence describes the unchanged backend. Username
matching and mixed email/username throttling still require implementation and
review. The overlay documents the old behavior; it is not a substitute for
the checked-in compatibility tests specified in AC-01.

## Scope

Accept email or username in the existing API request on both transports. Keep
email validation and case handling, verification, access, password hash upgrade,
session rotation, and recovery. Reject new folded username collisions on
authorized app registration and username edits, after their existing write
authority checks. Preserve account data, including legacy spellings and
collisions.

Exclude new request fields, username display changes, confusable-name detection,
new naming syntax rules, account deduplication, UI and device changes, analytics,
billing, TestFlight and any live database migration or rollout. This document
governs the backend slice of #41.

## Design

### Identifiers and matching

Trim surrounding Unicode whitespace from **login input**. A literal `@`
selects email exclusively: run the existing `store.EmailIdentity` parser and
match its canonical key, with no username fallback, even if no mailbox matches
or the address is invalid (`internal/store/email_identity.go:17-28`). Keep the
saved email spelling, verification status and recovery behavior.

Other input is eligible for username lookup only when nonempty, valid UTF-8,
at most 80 bytes before folding and free of NUL. Apply Go's locale-independent
`golang.org/x/text/cases.Fold` to the whole string. Keep internal whitespace.
Do not strip accents, add NFC or compatibility normalization, or compare
lookalike characters: `Étage` and `étage` match, `Étage` and `Etage` do not,
and `Straße` and `strasse` match under full case folding. The existing
app write rule permits names up to 80 bytes without outer whitespace
(`internal/auth/manager.go:107-113,243-249`).

Store a nullable binary comparison key on the user row and query through a
non-unique index for app users. Use the same folding helper for reads, writes,
and migration. Derive a key from supported stored names using their **saved**
spelling, without trimming; padded legacy values, invalid UTF-8 and legacy
values outside the supported byte limit are not username aliases. Names with
`@` or NUL retain the existing display/write policy and folded collision guard
when supported by the existing write rules, but are never login aliases. Use
byte-exact equality on the folded key so SQLite comparison does not introduce
extra normalization. Newsletter-only rows neither match nor reserve a folded
app key. The existing global exact-spelling constraint still applies, including
to stored newsletter usernames (`migrations/001_initial.sql:1-5`;
`internal/newsletter/capture.go:323-341`). Return at most two matches through
the index; a username is usable only
when exactly one app account owns that key. Exact case, insertion order,
password, or verification status never breaks a tie.

### New registrations and renames

Reserve the folded name in the same SQLite write transaction as the account
create or rename. Check whether any **other** app account owns that key, then
persist the display spelling and comparison key together. The existing
SQLite store starts writable transactions immediately
(`internal/store/open.go:57-74`); check and write there without hashing a
password under the lock. Two concurrent writes for the same key cannot both
claim it. Return the existing generic 409 account-details conflict for a new
collision. Keep the original exact uniqueness constraint. Never reveal the
owner or the collision set.

Check write authority before testing or disclosing availability. Verify live
native access in the same transaction before the profile collision query;
keep the existing live access predicate on the actual update. A revoked or
expired token gives the existing authorization failure for both free and
occupied names and writes nothing. For verified signup, recheck the live
anonymous session, proof, mailbox ownership, retirement and any promotion
conditions before the conflict query. Invalid signup authority continues to
return its existing result without revealing name availability
(`internal/auth/manager.go:243-248,266-286`;
`internal/auth/native_profile_test.go:13-35`;
`internal/auth/registration.go:400-445`).

Apply the check to compatibility registration, verified signup completion
including an existing newsletter contact becoming an app user, and profile
username changes (`internal/auth/manager.go:110-128,266-282`;
`internal/auth/registration.go:375-435,436-452,479-487`). Retain the current
signup request shape and acknowledgement; do not reserve or expose username
availability before mailbox proof. A conflict does not consume the verified
signup proof or leave an account, changed subscription association or new
session behind. The existing completion request allows a different username
while the proof is live. Editing other profile fields with an unchanged
grandfathered username remains possible. Changing its spelling to another
variant of a key still owned by another user is a conflict; changing to a free
key succeeds. A spelling change by the sole owner of a key is allowed.

Keep key derivation in the shared create path used by the legacy importer, so
the importer can preserve pre-existing collisions without creating a new
registration exception (`internal/store/email_identity.go:30-45`;
`internal/importer/import.go:65-75,164-187`). Only the existing offline
historical transfer path may import already-existing duplicates; it does not
become a product route. Promoting a newsletter contact sets the key as part of
the checked app write. Newsletter-only creation or minimization does not
populate an app key. Deletion removes the key with the user row. Restoring
older app data derives the key through the normal migration and keeps existing
account-lifecycle reconciliation. No change to the recipe data pipeline is
needed.

### Throttle, password and session authority

Keep the request contract, existing guard, 64 KiB credential request bounds,
and 15-attempt per-source check before identifier lookup. Charge a single
10-attempt/minute account bucket for the resolved user ID across both inputs
and both transports, before password work. Keep the limiter process-local
and atomic. If a request cannot resolve an account, use a separate hashed
unresolved-input bucket at the same limit, canonical to the eligible input
(e.g. folded username or canonical email) and separated by identifier kind
from real account keys. Keep the existing 429 and `Retry-After: 60`.
Baseline source: `internal/http/api.go:288-326,443-466`;
`internal/http/password.go:14-32`.

Resolve the account only once before password verification and never select
another user after charging the bucket. Under the limits, valid-format
identifiers with no unique owner and valid-format passwords take the existing
dummy password-work path. Rejected identifier syntax still counts toward
source and unresolved-input limits but does no lookup or password work.
Malformed passwords keep the quick generic rejection after the applicable
account or unresolved-input limit. All these credential errors retain the
existing 401 code and message; malformed JSON keeps its current 400.
Do not log raw identifiers or passwords. Do not promise that deliberately
exhausting a shared bucket makes both identifiers indistinguishable from all
unresolved inputs (`internal/http/api.go:250-261`;
`internal/auth/manager.go:136-158`;
`internal/auth/password.go:153-177`).

For username sign-in, recheck that the same user still uniquely owns the
matched key in the current credential/session commit transaction. The user,
password and email-key fences remain in force for both identifiers. If that
transaction observes a rename away from the key, a new historical ambiguity,
deletion or credential change, it creates no anchor and performs no hash
upgrade. The commit of that transaction is the authorization point for web
and native. Native keeps its existing separate adoption of the committed
anchor; a subsequent rename does not revoke already established authority.
Existing security actions that invalidate the anchor still do so. Keep the
current browser Origin/CSRF and cookie rotation, native bearer-family rotation,
verification restriction, and verified-email-only recovery
(`internal/auth/manager.go:161-209`;
`internal/auth/native.go:72-99`; `internal/http/native.go:141-158`;
`internal/http/email_verification.go:27-52`;
`internal/auth/recovery.go:31-51`).

### Upgrade and rollback

Add the derived key, populate it for existing app rows, create the non-unique
lookup index and advance the checksummed schema metadata in one explicit
SQLite migration transaction, following the current transaction pattern
(`internal/store/newsletter_minimisation_schema.go:18-42`). Do not edit any
prior migration. On failure or interruption the old compatible schema and
account data remain intact, and the explicit upgrade can be retried. On
success verify no account ID, display name, email, credential or session
changes. A dependency change that alters the folding result must keep the
existing comparison stable or rebuild stored keys as an explicit schema
change before use (`go.mod`, `go.sum`).

The prior binary requires its exact schema and cannot reopen an upgraded
database. Rehearse rollback on a disposable backup with the previous
compatible binary and the matching account-lifecycle state, using the existing
backup and restore checks. For any separately approved live attempt, keep
writes paused through verification so rollback does not drop writes made after
the backup; never replace the current independent deletion journal with an
older one. Confirm old email sign-in works after the rehearsal and keep the
database and account-lifecycle backups under the existing controls
(`internal/store/migrate.go:78-101`; `cmd/mychef/main.go:140`;
`internal/store/backup.go:43-83,152-183`; `README.md:416-427`).

### Issue #41 closeout

- [ ] Backend: review and test the API slice. Publication, merge and rollout are separate decisions.
- [ ] Web: change only the sign-in field to **Email or username**, allow a username without email-format validation, and retain explicit username/current-password autofill semantics. Keep recovery email-only.
- [ ] Final roadmap review: verify on iOS and Android that both identifiers sign in, the keyboard accepts either, native autofill and password managers can fill and update sign-in, and recovery still uses verified email. Record target builds and automated evidence plus any owner device evidence. Do not mark #41 complete until both clients pass this checkpoint.

## Acceptance Criteria

- [ ] AC-01: The existing web and native sign-in requests accept an account's normalized email or uniquely matching Unicode-folded username and reach the same account without changing the request or session contract.
- [ ] AC-02: Unknown or ambiguous usernames, disallowed legacy aliases and wrong passwords retain the existing generic credentials failure; email-shaped input cannot sign in as a username, and newsletter-only rows cannot sign in.
- [ ] AC-03: Email and username variants for the same account share one attempt limit across both transports, including concurrent requests, while the separate source limit and response stay unchanged.
- [ ] AC-04: A stale username resolution cannot commit sign-in or a password upgrade after the account loses that alias or its credential changes. Native may still adopt an already committed valid anchor under its existing checks. Verification and verified-email recovery keep their current requirements.
- [ ] AC-05: Fresh and upgraded SQLite databases use an indexed username lookup and preserve account data, aliases and prior access. A failed upgrade is retryable, and the prior version can still use a correctly restored pre-upgrade database.
- [ ] AC-06: New app signup and username changes cannot create a collision under the same Unicode case folding. Requests without live write authority do not reveal whether a name is taken. Existing collisions preserve email sign-in and can be resolved by one user choosing an available username.

## Test Plan

| Criterion | Proof |
| --- | --- |
| AC-01 | Use disposable SQLite manager and HTTP tests with unchanged JSON on web and native. Sign in with a trimmed, folded email and a mixed-case, accented username; check the same account ID and old/replacement session behavior. Include Unicode full-fold and accent-preservation examples. Commit regression tests for all five policy-independent scenarios above, with names starting TestSignInCompatibility; the reviewer runs `go test -p 1 -count=1 -run '^TestSignInCompatibility' ./internal/http` from the candidate commit. |
| AC-02 | Seed accented case collisions, padded/NUL legacy names, an email-shaped username of a different account, a non-mailbox email-shaped username and newsletter-only rows. Verify exact-case input and correct passwords cannot resolve ambiguity; compare the existing 401 code and message for unknown, ambiguous and wrong password without request ID. Verify eligible unresolved identities use padding and rejected syntax/passwords skip it while still charging limits. Verify both mailbox owners still sign in by email. |
| AC-03 | Alternate known email, username, case and spacing variants across web/native requests from distinct fixture sources. The eleventh attempt receives 429 on either alias. Race the two transports for the last slot after nine attempts: one proceeds and one returns 429 with the existing header. Verify the separate 15-per-source limit, independent account buckets and the cap on repeated unresolved input. |
| AC-04 | In disposable tests resolve and verify, then change alias ownership, introduce legacy ambiguity, delete the user or change the password/canonical email before committing; assert no anchor, password upgrade or session replacement. Separately commit the anchor, rename, and demonstrate normal native adoption of that valid anchor; repeat with existing anchor revocation to verify rejection. Exercise browser CSRF, invalid native authority, unverified profile restriction through either alias and verified mailbox recovery. |
| AC-05 | Migrate fresh SQLite and an old-schema fixture with collisions; compare account IDs, names, credential data and sessions; confirm `EXPLAIN QUERY PLAN` uses the index and newsletter rows have no folded app key. Inject an upgrade failure after key/index work, prove the old schema and rows are unchanged, then retry. Use the prior binary and protected disposable pre-upgrade backup with lifecycle reconciliation to rehearse rollback and confirm prior email sign-in. Test store/import creation preserves legacy rows and derives their keys. Run `go test -p 1 ./internal/auth ./internal/store ./internal/http ./internal/importer`, with the existing disposable test provider for importer integration if required. |
| AC-06 | Test direct registration, verified signup with and without newsletter conversion, and profile rename. Use case-only and non-ASCII collisions; confirm generic 409, no consumed proof or unintended state change, and a successful retry with an available name. Race two attempts for one folded key, including across registration and profile operations, and verify only one can claim it. For revoked or expired native access and invalid signup authority, compare outcomes against free and occupied names: same authorization failure and no writes. With a disposable newsletter row, check that only its exact stored spelling remains reserved by the old constraint. Verify unchanged legacy edits, a sole-owner case change, grandfathered email sign-in and recovery of username sign-in after resolving an existing collision. |

## Tasks

- [x] T1 `internal/store/login_identity.go`, `internal/store/login_identity_test.go`, `internal/store/email_identity.go`, `internal/store/models.go`, `internal/store/migrate.go`, `migrations/` - make the disposable schema and query-plan test fail first; then derive and index folded keys for fresh, upgraded, and legacy imported rows without rewriting account data, and pass migration failure/retry checks (AC-01, AC-02, AC-05).
      Evidence: The tracked migration test initially failed for a missing key column. Six store checks then passed for the prior schema, indexed Unicode collisions, preserved users and sessions, injected backfill failure and retry, and shared import creation.
- [x] T2 `internal/auth/manager.go`, `internal/auth/registration.go`, `internal/auth/username_policy_test.go` - reproduce conflicting registrations and renames, then require live authority before reserving the same folded key in each write path; verify one winner across signup and rename, proof retry, and resolved legacy collisions (AC-02, AC-06).
      Evidence: Before the change, the tracked tests accepted folded duplicates and both concurrent writes succeeded. The focused auth suite now passes compatibility registration, verified signup and promotion with reusable proof, one registration-or-rename winner, unchanged legacy edits, and revoked/expired native authority.
- [ ] T3 `internal/auth/manager.go`, `internal/auth/login_identifier.go`, `internal/auth/login_identifier_test.go` - reproduce missing and stale username sign-in, then implement shared identity selection and the session-commit check; verify generic rejection, password-work paths, email precedence, and native anchor adoption (AC-01, AC-02, AC-04).
- [ ] T4 `internal/http/api.go`, `internal/http/login_identifier_test.go`, `internal/http/login_compatibility_test.go` - reproduce the missing shared account limit with mixed alias and transport requests, then wire the existing API to the resolved user and verify the last-slot race, source limit, recovery, and all five checked-in compatibility probes (AC-01, AC-02, AC-03, AC-04).
- [ ] T5 `internal/store/login_identity_test.go`, `internal/importer/import_test.go`, `cmd/mychef-migrate/` - verify the exact new and prior binaries on disposable migration, import, backup, and restore fixtures; prove the old binary can serve the restored account with its original email and confirm the focused Go suite and repository checks (AC-05, AC-06).

## Open Questions

None. The owner selected Unicode case folding and preventing new collisions.
Existing collisions retain email sign-in. Similar-looking but distinct names
are outside this policy.

## Review Log

| Date | Review | Kind | Commit | Model | Isolation | Outcome |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-11-18 | R-1 | spec | `4df5bcc5d337709899dbe8ad640c3c028c35743d` | unknown | procedural | 1 blocking, 4 should-fix |
| 2026-11-18 | R-2 | spec | `6e3f76b065ebdbdba7d3b5c709d183a95e0ff926` | unknown | procedural | 1 blocking, 2 should-fix |
| 2026-11-18 | R-3 | spec | `5bc04e63ccf60c1210396101306e0a231bec83b3` | unknown | procedural | No findings |
| 2026-11-18 | R-3 | spec | `5bc04e63ccf60c1210396101306e0a231bec83b3` | n/a | n/a | accepted by owner |

### R-1

FINDING: F-01
KIND: judgement
SEVERITY: blocking
WHERE: Context > Owner summary; Scope; Design > Identifier resolution; Acceptance Criteria AC-01 and AC-02; Test Plan AC-01 and AC-02; Open Questions
EVIDENCE: Issue #41 requests a consistent case and collision rule without selecting one. The draft selects ASCII folding, exact non-ASCII spelling and rejection of padded stored names, while leaving username writes unchanged. The database enforces only exact uniqueness and the profile update accepts case variants (`migrations/001_initial.sql:2-4`, `internal/auth/manager.go:243-248,266-278`). A collision can therefore be introduced after rollout and make a previously usable username stop working, with email sign-in still available. The pinned feature standard's Open questions section reserves consequential tradeoffs for the owner.
PROPOSAL: Put the matching choice and its effect on existing and newly created collisions to the owner together. If the owner keeps write validation out of scope, state the accepted limitation and verify both email accounts remain usable when a collision is introduced.

FINDING: F-02
KIND: factual
SEVERITY: should-fix
WHERE: Context > Owner summary; Scope; Design > Identifier resolution; Acceptance Criteria AC-05; Test Plan AC-05
EVIDENCE: The new index requires a checksummed schema version, even without rewriting a user row. The previous app binary rejects a schema version newer than its own at startup (`internal/store/migrate.go:78-101`, `cmd/mychef/main.go:140`). The draft defines fresh and forward upgrade checks but leaves failed migration and rollback undefined.
PROPOSAL: Specify transactional migration and test failure and retry against a disposable old schema. Document and rehearse the supported way to restore a compatible database with matching lifecycle state during a later authorized rollout; do not imply that switching back to the previous binary alone will work.

FINDING: F-03
KIND: factual
SEVERITY: should-fix
WHERE: Context > Owner summary and Background; Scope; Design > Throttle and session authority; Acceptance Criteria AC-01 and AC-04; Test Plan AC-01 and AC-04
EVIDENCE: The stale-alias guarantee currently reads as applying to any session creation. Native sign-in commits the anchor in `internal/auth/manager.go:175-206`, then separately adopts it and issues native credentials in `internal/http/native.go:141-158` and `internal/auth/native.go:72-99`. An alias can change between those steps. The draft prescribes the first transaction check but does not identify the authorization point for the two-stage native flow.
PROPOSAL: Define when alias ownership must hold. If it is the existing anchor transaction, state that a later rename does not revoke a login already committed there and keep existing native adoption semantics. Test both a rename before the authority check and the boundary after commitment.

FINDING: F-04
KIND: factual
SEVERITY: should-fix
WHERE: Context > Owner summary and Background; Design > Identifier resolution and Throttle and session authority; Acceptance Criteria AC-02; Test Plan AC-02
EVIDENCE: The draft can be read as applying password padding to any well-formed JSON request whose identifier has no unique account, including syntactically invalid or disallowed identifiers. Existing sign-in returns the same generic error before password hashing when email parsing or password normalization fails (`internal/auth/manager.go:136-147`). The padded path uses the shared password-work slots, bcrypt and Argon2id (`internal/auth/password.go:153-177`).
PROPOSAL: Distinguish syntactically eligible unresolved identifiers, which need existing padding, from rejected syntax, which can return the same generic error without database-dependent or password verification work. Keep the approved source and unresolved-input limit for both, and include both paths in the fixture tests.

FINDING: F-05
KIND: factual
SEVERITY: should-fix
WHERE: Design > Throttle and session authority; Acceptance Criteria AC-03; Test Plan AC-03
EVIDENCE: The current process limit atomically checks and increments a key (`internal/http/api.go:288-311`), but the new account key is derived after identity resolution. The proposed sequential test verifies the total after alternate identifiers but does not verify mixed-transport requests contending for the last account slot.
PROPOSAL: Add a concurrent fixture check with email and username using distinct sources and one account attempt left. Exactly one should reach credential verification, while the other receives the ordinary 429 response.

### R-2

FINDING: F-01
KIND: factual
SEVERITY: blocking
WHERE: Scope; Design > New registrations and renames; Acceptance Criteria AC-06; Test Plan AC-06
EVIDENCE: The revised design requires an in-transaction collision check but does not order it after write authorization. For native profile changes, `internal/http/api.go:514-527` passes the access token to `Manager.Update`, which checks the token's shape first and the live session only in the UPDATE predicate (`internal/auth/manager.go:243-248,266-286`). The existing expiry regression verifies this check at execution time (`internal/auth/native_profile_test.go:13-35`). An earlier conflict return could give an expired native caller 409 for an occupied name and an authorization error for a free one. Verified signup also validates the live anonymous session, proof and mailbox ownership before acting (`internal/auth/registration.go:400-445`).
PROPOSAL: Require existing live authorization before returning collision information and retain the native authority predicate at the actual update. Test free and occupied names with expired or revoked native authority and invalid signup authority, confirming identical authorization failures and no writes.

FINDING: F-02
KIND: factual
SEVERITY: should-fix
WHERE: Design > Identifiers and matching and New registrations and renames; Test Plan AC-05 and AC-06
EVIDENCE: The revised design says newsletter-only rows do not reserve an app alias, but it also retains the global exact username constraint (`migrations/001_initial.sql:1-5`). A newsletter-only row with a non-null username can therefore still prevent an exact-spelling signup or rename, even though the new folded lookup excludes it (`internal/newsletter/capture.go:323-341`).
PROPOSAL: State that newsletter-only rows do not reserve a folded app key and that the existing exact-spelling constraint remains unchanged. Check both cases with a disposable newsletter fixture.

FINDING: F-03
KIND: factual
SEVERITY: should-fix
WHERE: Context > Policy-independent sign-in baseline; Test Plan AC-01
EVIDENCE: The revised plan requires running the policy-independent email probes, but their five named tests were run through a temporary overlay. At `6e3f76b065ebdbdba7d3b5c709d183a95e0ff926`, `git grep TestIssue41Baseline` finds those names only in the feature document. The independent implementation reviewer cannot run those checks from that evidence alone.
PROPOSAL: Require equivalent probes to be committed as regression tests and name a reproducible invocation for them. Keep the temporary overlay results as baseline evidence only.

### R-3

NO FINDINGS

## Sources

- [MyChef issue #41](https://github.com/LowGok/my-chef/issues/41): requested email-or-username behavior and device autofill scope.
