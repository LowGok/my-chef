# Native identity provider operations

`scripts/native-provider-operations.ts` guards separately approved operations for
`dev.mychef.app`. It is an operator library, with fake-provider regression tests;
it neither authenticates an operator nor grants human approval. No live provider
adapter is enabled by importing it. The existing Preview delivery path remains
separate and cannot distribute the new identity.

Prepare one plan for one operation. Record its canonical digest privately with
the owner's approval and expiry, then use an adapter that reads the authenticated
provider account and its current role. A JSON approval file alone is not owner
consent. Never put credentials, tester addresses or raw provider responses in the
plan, journal, diagnostics or public CI artifacts. Member IDs refer to the exact
privately verified account roster.

| Stage | Facts needed before the operation | Facts obtained afterwards |
| --- | --- | --- |
| App or explicit bundle registration | Account, platform, package/bundle and name. Apple app creation also names the SKU, Limited Access and every effective app user | Immutable record ID and exact identity readback |
| Internal group creation | Parent app, group name, empty roster, automatic distribution and public link off | Group ID and unchanged parent/type/settings |
| Capability or profile preparation | Existing record, exact capabilities, certificate, production push and App Attest, associated domain | Capability/profile identity, expiry and signed entitlement readback |
| First Play upload | Existing app, Play App Signing mode, upload certificate, exact signed bundle, inactive internal draft | Complete applicable app-signing fingerprint set, distinct from the upload certificate |
| Later signing, upload or assignment | Read-back record and profile, artifact/source hashes, signing facts, exact group/track and roster where applicable | Exact artifact/build, scope and distribution readback |
| Website publication | Existing site/origin and state hash, exact staged association bundle and three paths | Published bytes, headers and untouched unrelated routes |

On Apple app creation, bind `userAccess: "limited"` and the complete effective
`appUserIds` roster, including users with account-wide access through their
role. In the authenticated New App dialog choose **Limited Access** and verify
the visible users and any inherited access before creating the record. Read
back the same scope before progressing to group creation, upload or
assignment. [Apple app creation controls](https://developer.apple.com/help/app-store-connect/create-an-app-record/add-a-new-app/)

Creation does not require an ID that only the provider can return afterwards.
Every later operation requires a current before-state digest. The authenticated
adapter must hash the complete operation-relevant provider projection, not input
facts. Read back every approved field, including the full signing set and exact
roster; return a receipt only if all match. Never construct a receipt by copying
the requested target. Artifact callbacks inspect immutable artifact bytes and
packaged metadata, including the prepared bundle digest, rather than trusting a
filename. Use provider conditional writes where available and stop on drift.

A private directory holds an exclusive account/provider/lane lock and a synced
pending journal before a write. Keep the directory on durable local storage,
mode 0700, with files mode 0600. There is one provider submission per invocation;
unknown failures remain pending. A new operation ID cannot bypass an unresolved
write on the same lane. Reconcile through authenticated readback first; ambiguous
or multiple matching records stop the operation. A stale lock after a process
crash needs operator investigation and read-only reconciliation before removal;
there is no automatic lock stealing or retry loop. A provider's explicit
no-write rejection may be retried only with current approval and fresh readback.

Approval expiry and account scope are rechecked immediately before submission.
They cannot revoke a provider write already sent. After submission, verify the
actual result before marking the journal verified. This guard is one part of
the delivery evidence: it does not establish paid membership, public CI
capacity, provider authentication, signing validity, an installed-device result
or permission to create records, publish files, upload builds or change testers.
Those facts and approvals remain separate gates in the native identity plan.
