# Maintenance: preserve newsletter proof decisions across a second boundary

## Definition

Outcome: repair the valid-proof failure reproduced after newsletter PR39 merged, then open a separate reviewable follow-up PR under issue35. Do not merge it or retry the broken release under PR39's approval.
Preserved contract: valid confirmation and withdrawal keep the same ID and choice, including paused/minimized contacts; SQL and current lifecycle journal record one consistent decision. Expired, wrong-owner, stale and replayed authority remains refused, failures roll back without consuming valid proofs, and fresh authorization checks remain current. Source: internal/newsletter/proofs.go and minimisation.go, lifecycle post-state validation and the existing paused-role test.
Task base: d5460ecccf45d8debef2951e9e6150cf1ce7ecff.
Affected paths: internal/newsletter/minimisation.go; internal/newsletter/proofs.go; internal/newsletter/proof_decision_time_test.go; this record.
Verification inputs: existing AGENTS.md, go.mod/go.sum, migrations, scripts/rewrite.py and its pinned tools, generation inputs/web locks, Dockerfile/.dockerignore, .github/workflows/delivery.yml and the actual process/container/upgrade scripts. They stay unchanged; no dependency, schema, permission or delivery-policy change is included.
Proposed change: pass the tracked journal decisionUnix to its SQL proof callback instead of recording two independently sampled dates. Untracked legacy decisions retain their existing clock behavior. Keep fresh expiry/ownership checks, fences, conflict validation and rollback. This restores promised behavior and qualifies as bounded maintenance under vh-adlc lifecycle intake; no feature status/frontmatter or second feature claim is introduced.
Required local checks: deterministic advancing-clock confirmation/withdrawal regressions for tracked raw/reduced and ordinary contacts; delayed expiry and injected failure/retry; existing focused proof/ownership/paused-role normal and race tests; required build/generate/test/lint/audit commands; actual executable, browser, import/upgrade and final-container/context/scan checks. Independent exact-commit maintenance review must have no open findings before local completion/publication. Required delivery checks: after the authorized PR is published, its unchanged normal GitHub workflow must pass in full for the final head, including the complete20-minute race command. Local completion or review is not a hosted-CI pass or merge authority. Merge and normal signed delivery of the follow-up retain a separate exact-head approval. No production migration, service change, mail, public capture, analytics or backup policy is included.

## Evidence

Baseline: Delivery43825994676 failed on exact base with paused confirmation503 rather than200; release skipped. Thirty unchanged local repetitions passed but did not explain or waive it. An independent external synthetic overlay deterministically reproduced both confirmation and withdrawal when journal and SQL clock reads cross a second: lifecycle conflict becomes503, while choice/proof/journal rollback is safe and stable-clock retry succeeds. The individual CI response did not expose its underlying error, so its attribution remains consistent rather than conclusive. Original failed CI and external counterexample remain in private operational evidence.

Deterministic committed regression: `go test ./internal/newsletter -run '^TestProofDecision' -count=1 -v` fails on tracked pending/confirmed/reduced advancing-clock transitions and failure retries, while ordinary controls and the fresh-expiry guard pass (proof-time-red-ready.log). The first test draft incorrectly attempted a fresh confirmation mail for an already-confirmed contact; corrected fixture setup before recording the behavioral red result. No production source changed. Manual documentation impact is this defect/verification record; public/operator contracts stay unchanged.


The repair carries the optional recorded decisionUnix into the private SQL callback. Tracked SQL confirmation/withdrawal/decision dates reuse it; ordinary untracked callbacks retain their original fresh-clock date. Fresh expiry checks and proof-consumption/mail invalidation timestamps still use the current clock. Production diff is limited to the two declared newsletter files; no validation, migration, dependency, worker, public surface or CI setting changes.

Observed green: all new deterministic cases pass, including ordinary controls, pending/confirmed/reduced choices, exact SQL/journal post-state, replay, injected write failure with unconsumed proof/safe journal and same-proof retry, and confirmation expiring after the journal decision. Five repetitions pass (proof-time-repeat.log); the unchanged paused-runtime test passes30 repetitions (proof-time-paused-repeat.log). Focused affected-package races pass: newsletter222.278s, runtime38.597s, auth236.164s, HTTP32.139s, store110.134s (proof-time-race.log).

Required local build/generation/full Go/type/Bun tests pass (88 Bun tests470 assertions); lint reports0 issues and audit reports no reachable/web findings. Actual newsletter executable/recovery, historical SQLite upgrade/restore and PostgreSQL import pass. The actual rebuilt final nonroot/read-only image sha256:9ce97d3516f53ed4144818434cd4b9d34f67f84210d859043fa7a6ef6bbf659c passes narrow context/private canaries, paused role/proofs/restart, browser/menu/personal/import paths, storage/backup/restore/replay/SIGTERM and the pinned HIGH/CRITICAL scan (scan=true). Logs proof-time-build/generate/repo/lint/audit/process/upgrade/import/container retain the executed results in private operational evidence. No local full analytics-dataset race claim is made: the unchanged complete race command remains a required final-PR CI delivery step.

Manual documentation impact: the public/operator behavior, source and schema/expiry/restore/rights handoff stay unchanged; no README or deployment-guide correction is needed. This maintenance record documents the internal timestamp regression and verification. The old completed feature and every review/history entry remain untouched. All data/mail/keys are disposable synthetic fixtures; no production state, real mail, beta or public capture/analytics operation occurred.

## Reviews

### R-1

Target: e8ebf332996da2c27dc1c4fd56d888758293e025
Model: unknown (identity not attested)
Isolation: procedural

```text
NO FINDINGS
```

The independent reviewer replayed the new regression against the original production blobs, ran exact-repair normal/race and neighboring purpose/expiry/address-binding/withdrawal tests, verified the actual paused HTTP role and independently matching binaries, and executed fresh process/historical recovery plus archived-source final container/browser/storage/scan checks. Its actual container transcript reports the same image9ce97d35 with scan=true. These are synthetic local checks; final hosted PR CI is still a delivery gate.

## State

Locally complete: required local checks pass and current independent R-1 has no findings. The affected source, verification inputs and Definition match the reviewed commit. The authorized follow-up PR still needs its full normal final-head CI, then separate exact-head merge and signed-release approval. No production activation or backup policy is included.
