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
Canonical main pushes can publish a new exact-version GHCR image after checks.
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
