# Repository instructions

Read the shared conventions at
https://github.com/vhco-pro/docs/blob/d8f07c9dd69038375a535af8ad586086688f85ab/AGENTS.md.
Use conventions/coding.md for code, conventions/go.md for Go, and
conventions/typescript.md for TypeScript. Feature work follows
conventions/feature-documents.md and conventions/delivery.md. Edit prose with
guides/humanize-prose.md and follow conventions/documentation.md.

## Local commands and constraints

Use `python3 scripts/rewrite.py generate`, `test`, `lint`, `audit` and `e2e`.
The build helper resolves the pinned Go and Bun tools. Keep database migrations
explicit. Test with disposable SQLite storage and fake providers. Preserve
unrelated changes and keep credentials and personal data outside Git.

Public delivery and native distribution follow the existing README and CI
workflows. Local feature development grants no production migration or release.
