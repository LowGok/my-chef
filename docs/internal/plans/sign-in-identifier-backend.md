---
status: draft
status_description: "Backend slice specified for review; no implementation, publication or rollout."
issue: https://github.com/LowGok/my-chef/issues/41
goal: "Sign in to the same existing app account through the current API using its email address or a uniquely matching username."
covers:
  - internal/auth/
  - internal/http/
  - internal/store/
  - migrations/
---

# Plan: Email or username sign-in backend

## Context

### Owner summary

This first slice lets the existing sign-in API accept a username in the same field where it accepts email. Both identifiers reach the same account and share its attempt limit. Existing email sign-in, verification, sessions, and email-only password recovery keep their current behavior. No account is converted or merged.

Email matching stays as it is now. For usernames, the proposed rule trims input and ignores ASCII letter case, leaving other characters exact. If multiple app accounts match a username, none can sign in through that username. Those accounts keep email sign-in. Inputs containing an at sign are reserved for email, so a legacy username cannot take over an email identifier. One non-unique SQLite index keeps username lookups bounded without changing saved usernames.

The owner authorized development of the backend slice, with merge and rollout separate. This document is the review target for its precise rules. Test work will use disposable databases and fake providers. Client work still follows: first update the web form, then verify the field, keyboard, autofill and password-manager behavior on iOS and Android before closing #41. A backend review or merge alone cannot close #41.

### Background

- The existing endpoint accepts JSON with `email` and `password`, charges 15 attempts per source and 10 per supplied email key, and calls the session manager (`internal/http/api.go:443-466`). The limiter uses one-minute process-local windows (`internal/http/api.go:288-321`).
- Both transports ultimately use that handler: the native prefix routes to the web API path with native request context (`internal/http/native.go:13-23`), and the guard and signed-in response honor that context (`internal/http/api.go:328-350`, `internal/http/api.go:468-483`).
- Usernames are currently stored under exact uniqueness, and app registration and profile edits already reject surrounding whitespace and cap usernames at 80 bytes (`migrations/001_initial.sql:1-5`, `internal/auth/manager.go:107-124`, `internal/auth/manager.go:243-249`). This does not prevent ASCII case variants from coexisting.
- Current lookup excludes newsletter-only rows. Session completion rechecks the matched user ID, password and canonical email in the write transaction, and response handling preserves verification restrictions (`internal/auth/manager.go:136-203`, `internal/http/api.go:468-483`). Recovery validates email independently (`internal/auth/recovery.go:31-51`).

## Scope

Accept email or a username in the existing API request on both transports. Keep existing email validation, verification, access, login credential upgrade, session rotation and recovery behavior. Define how the read path treats legacy usernames and preserve stored account data.

Exclude registration and profile naming policy changes, data cleanup or backfill, new request fields, UI or accessibility changes, native distribution, analytics, billing, TestFlight and live migration or rollout. This document governs the backend slice of #41 only.

## Design

### Identifier resolution

- Trim input with Go's Unicode whitespace semantics. Inputs containing `@` take the existing `store.EmailIdentity` route exclusively: use its normal validation and key; an invalid or unmatched email has no username fallback. A valid email always takes precedence over any matching legacy username. `store.EmailIdentity` already trims and applies Unicode case folding to a parsed address (`internal/store/email_identity.go:18-29`).
- Other nonempty inputs within the existing 80-byte username limit are username candidates. Use ASCII case-insensitive matching with SQLite `NOCASE`; retain exact bytes for other characters, including accents, and for internal spaces. Reject embedded NUL from username lookup and exclude stored values containing NUL from matches. Never trim or rewrite stored usernames; old values padded on either side cannot be used as aliases. Email still works for these accounts.
- Search only existing non-newsletter app rows. Resolve up to two matching rows through a non-unique index on the case-insensitive username; exactly one is eligible. Two or more produce the same wrong-credentials response even when the caller supplies the exact case or a correct password. Neither insertion order nor verification status breaks ties. Leave duplicate rows and original display spelling as they are. Use the next explicit schema migration for the index, compatible with both fresh and upgraded SQLite databases; do not run it outside tests under this approval.

### Throttle and session authority

- Keep the existing request bounds and source throttle before lookup. Resolve identity once into a login candidate carrying the matched user and matched identifier. Charge the existing 10-attempt bucket by the immutable app user ID, shared by email, username and both transports, before password verification. Use a separate hashed unresolved-input bucket for unknown, malformed and ambiguous values, keyed consistently with their input normalization; keep the existing source and identifier windows and 429 response. The existing database assigns app IDs with `AUTOINCREMENT` (`migrations/001_initial.sql:1-5`).
- Do not select a different account after charging a bucket. Use the same candidate's password and the existing password-work padding for all well-formed credential requests with no unique account. Unauthorized requests retain the existing credentials response status, code and message (`internal/http/api.go:253-261`, `internal/auth/password.go:153-177`). A shared exhausted limit may yield 429 for either known alias; the guarantee is a common error body for credential failures under the limit, not that account aliases are indistinguishable across arbitrary throttle history.
- Keep the existing session and legacy hash-upgrade transaction. For a username candidate, recheck its unique ownership by the same user in that transaction before any write; a rename, new ambiguity, deletion, password change or canonical email change must not complete a stale login. Preserve browser CSRF/session replacement, native bearer-family rotation, and restricted responses for unverified users. Leave all verification and recovery entry points unchanged. Do not log raw identifiers or passwords.

### Issue #41 closeout

- [ ] Backend: review and test the API slice. Publication, merge and rollout are separate decisions.
- [ ] Web: change only the sign-in field to **Email or username**, allow a username without email-format validation, and retain explicit username/current-password autofill semantics. Keep recovery email-only.
- [ ] Final roadmap review: verify on iOS and Android that both identifiers sign in, the keyboard accepts either, native autofill and password managers can fill and update sign-in, and recovery still uses verified email. Record target builds and automated evidence plus any owner device evidence. Do not mark #41 complete until both clients pass this checkpoint.

## Acceptance Criteria

- [ ] AC-01: The existing web and native sign-in requests accept an account's normalized email or uniquely matching username and reach the same account without changing the request or session contract.
- [ ] AC-02: Unknown or ambiguous usernames, disallowed legacy aliases and wrong passwords return the existing generic credentials failure; email-shaped input can never sign in as another account's username, and newsletter-only rows cannot sign in.
- [ ] AC-03: The same per-account attempt limit applies when a caller alternates email and username, including between web and native requests; the existing per-source limit still applies independently.
- [ ] AC-04: Stale username resolution cannot create a session or upgrade a password after the account loses ownership of that alias or the credential changes. Verification and verified-email recovery keep their current requirements.
- [ ] AC-05: The same account records and usernames are preserved when applying the username lookup index on fresh and upgraded SQLite, and case-insensitive login does not require a table scan.

## Test Plan

| Criterion | Proof |
| --- | --- |
| AC-01 | Disposable SQLite manager and HTTP tests: updated web and native request paths use the existing JSON fields; sign in with folded/trimmed email and mixed-case username and verify same user ID and the ordinary response and rotation per transport. Existing email-only clients still pass. |
| AC-02 | Seed case-only collisions, compatible accented usernames, whitespace-padded and NUL-containing stored names, an email-shaped username belonging to another account, unknown aliases and newsletter-only rows. Compare error status/code/message without request ID, including correct passwords on ambiguous fixture accounts and invalid addresses containing an at sign. Verify valid email still signs in to the mailbox owner. |
| AC-03 | In one API process, mix email and username, case/space variants, web and native requests from distinct simulated sources. Attempt 11 hits the same 429 in both directions. Separate users have separate buckets; a single source reaches the unchanged limit even across unrelated inputs. Check repeated unresolved inputs and separate account versus unresolved buckets without sleeping by using fresh API fixtures. |
| AC-04 | Resolve/verify against fixtures, then rename, add an ambiguous alias, delete or change a relevant credential before completion. Assert no new session and no hash upgrade. Check missing browser CSRF/native bearer rejection, restricted unverified sign-in for either alias and recovery remaining addressed solely to the verified mailbox. Run existing password, recovery, verification and native session tests. |
| AC-05 | Open a fresh schema and upgrade a disposable pre-index fixture with duplicate legacy values; assert no changed rows or uniqueness changes and inspect the plan for the production lookup on the index. Run `go test -p 1 ./internal/auth ./internal/store ./internal/http`. |

## Tasks

None yet. Break the approved slice into failing-test-first storage, credential and HTTP tasks after the planned gate.

## Open Questions

None.

## Review Log

| Date | Review | Kind | Commit | Model | Isolation | Outcome |
| --- | --- | --- | --- | --- | --- | --- |

## Sources

- [MyChef issue #41](https://github.com/LowGok/my-chef/issues/41): requested email-or-username behavior and device autofill scope.
