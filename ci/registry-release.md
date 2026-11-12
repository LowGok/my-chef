# Private GHCR release

Restore container release to `ghcr.io/lowgok/my-chef-app`, a private package
containing builds from the canonical public source. Keep the normal Delivery
checks and exact-version rules. A 401 or 403 still stops release preflight.
The prior source tags and registry package are outside this operation.

Delivery holds publication while `MYCHEF_GHCR_PACKAGE_STATE` is unset. The
initialization job runs only on canonical `main`, after the owner dispatches
it for the exact SHA in `MYCHEF_GHCR_INITIALIZE_SHA`. Its build context is an
empty OCI marker. It pushes only by digest and creates no application image,
release tag or `latest` alias. Verify private package visibility and repository
association before dispatching the separate first release for that same SHA.

The first release must pass application and container checks, the registry
preflight, the blocking vulnerability scan, signing, SBOM and provenance
attestations, and runtime smoke against the published digest. Keep the
release receipt. Once that succeeds, set the package state to `ready` and
clear the initialization SHA. This enables ordinary main releases and
disables manual initialization. If publication fails, leave it held and
inspect the failed step and any tag or digest before retrying.

The release helper tests exercise held and ready pushes, owner and SHA checks,
the inert marker context, explicit missing-manifest handling, and the digest
reference for the new package. Test the workflow on a PR before merging. The
first authorized main dispatch is required to verify GHCR permissions and
publication; local tests cannot prove either.
