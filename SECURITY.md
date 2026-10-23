# Security policy

zipgo is a public-facing static host: it terminates TLS, serves files other
people deployed and sits in front of password-protected sites. Its defaults are
chosen so that reading this repository gives nobody a way in — there is no
secret in the design, only in the deployment (passwords, the session key, the
certificates). The README's *Security* section lists what is on by default.

## Supported versions

The latest release only. The Docker image and the install scripts always
fetch a release binary and verify it against that release's `checksums.txt`;
pin `ZIPGO_VERSION` in production and follow the releases to pick up fixes —
most of all the ones that come from Caddy, the server zipgo embeds.

## Reporting a vulnerability

Please report privately through GitHub's
[private vulnerability reporting](https://github.com/GabrielVidal1/zipgo/security/advisories/new)
rather than in a public issue. Include the version (`zipgo version`), the
smallest configuration that shows the problem, and what an attacker gains.
You should hear back within a week.

## What this project does to stay patched

- `go.mod` pins every dependency and a Go toolchain; CI runs
  [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) on
  every push and weekly, so a new advisory against a pinned module fails the
  build.
- Dependabot proposes the Go, GitHub Actions and base-image updates weekly.
- Releases are built with `-mod=readonly -trimpath` by workflows whose actions
  are pinned to commits, and ship a `checksums.txt`.
