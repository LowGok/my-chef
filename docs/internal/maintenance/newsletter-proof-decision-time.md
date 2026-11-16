# Maintenance: preserve newsletter proof decisions across a second boundary

## Definition

Outcome: repair the valid-proof failure reproduced after newsletter PR39 merged, then open a separate reviewable follow-up PR under issue35. Do not merge it or retry the broken release under PR39's approval.
Preserved contract: valid confirmation and withdrawal keep the same ID and choice, including paused/minimized contacts; SQL and current lifecycle journal record one consistent decision. Expired, wrong-owner, stale and replayed authority remains refused, failures roll back without consuming valid proofs, and fresh authorization checks remain current. Source: internal/newsletter/proofs.go and minimisation.go, lifecycle post-state validation and the existing paused-role test.
Task base: d5460ecccf45d8debef2951e9e6150cf1ce7ecff.
Affected paths: internal/newsletter/minimisation.go; internal/newsletter/proofs.go; internal/newsletter/proof_decision_time_test.go; this record.
Verification inputs: existing AGENTS.md, go.mod/go.sum, migrations, scripts/rewrite.py and its pinned tools, generation inputs/web locks, Dockerfile/.dockerignore, .github/workflows/delivery.yml and the actual process/container/upgrade scripts. They stay unchanged; no dependency, schema, permission or delivery-policy change is included.
Proposed change: pass the tracked journal decisionUnix to its SQL proof callback instead of recording two independently sampled dates. Untracked legacy decisions retain their existing clock behavior. Keep fresh expiry/ownership checks, fences, conflict validation and rollback. This restores promised behavior and qualifies as bounded maintenance under vh-adlc lifecycle intake; no feature status/frontmatter or second feature claim is introduced.
Required checks: deterministic advancing-clock confirmation/withdrawal regressions for tracked raw/reduced and ordinary contacts; delayed expiry and injected failure/retry; existing focused proof/ownership/paused-role normal and race tests; required build/generate/test/lint/audit commands; actual executable, browser, import/upgrade and final-container/context/scan checks through the unchanged normal GitHub workflow. Full normal CI must pass for the final PR head. Independent exact-commit maintenance review must have no open findings before completion/publication. Merge and normal signed delivery of the follow-up retain a separate exact-head approval. No production migration, service change, mail, public capture, analytics or backup policy is included.

## Evidence

Baseline: Delivery43825994676 failed on exact base with paused confirmation503 rather than200; release skipped. Thirty unchanged local repetitions passed but did not explain or waive it. An independent external synthetic overlay deterministically reproduced both confirmation and withdrawal when journal and SQL clock reads cross a second: lifecycle conflict becomes503, while choice/proof/journal rollback is safe and stable-clock retry succeeds. The individual CI response did not expose its underlying error, so its attribution remains consistent rather than conclusive. Original failed CI and external counterexample remain in private operational evidence.

Deterministic committed regression: `go test ./internal/newsletter -run '^TestProofDecision' -count=1 -v` fails on tracked pending/confirmed/reduced advancing-clock transitions and failure retries, while ordinary controls and the fresh-expiry guard pass (proof-time-red-ready.log). The first test draft incorrectly attempted a fresh confirmation mail for an already-confirmed contact; corrected fixture setup before recording the behavioral red result. No production source changed. Manual documentation impact is this defect/verification record; public/operator contracts stay unchanged.

## Reviews

No repair review yet. The preceding independent diagnostic proved a blocking timing-dependent availability defect, not data/consent corruption.

## State

Active: write and observe the deterministic red regression before changing production code. Publication of the separate follow-up PR is authorized; merge is not.
