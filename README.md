# Codex Mobile Bridge Server

The MIT-licensed Go/PostgreSQL core for an optional cloud relay between the
desktop gateway and the native mobile companion. The same core supports an
independent `selfhost` installation and a `hosted` service. Private operations,
payment collection, signing credentials and production infrastructure are not
part of this repository.

This is a local implementation checkpoint, not a production rollout. See
[implementation and verification status](docs/implementation-status.md) for the
checks that actually ran and the remaining release gates.

## Security and behavior

- Cloud credentials belong to one exact canonical HTTPS origin and account.
  A cloud login does not approve a phone on a desktop.
- Pairing requires a short-lived QR, pinned keys, an encrypted proof and explicit
  approval on the original local desktop. Noise KK protects control records;
  single-message Noise K uses separate notification-preview keys.
- The relay forwards opaque records bounded to 32 KiB. It does not queue offline
  commands or automatically replay a write with an unknown result.
- Direct and push delivery share a durable random notification ID per event and
  binding. Native clients persist receipt state before ACK. A direct lease lasts
  45 seconds, and notifications expire after one hour.
- Private preview plaintext is bounded to 1 KiB, with a generic fallback. Full
  APNs JSON is bounded to 4 KiB. Provider acceptance is not a device receipt.
- Hosted entitlement is account-wide service days. Redemption is desktop/web
  only; the mobile app has no purchase or redemption link. Selfhost has no hosted
  entitlement requirement or hosted registration dependency.

## Run and configure

Use Go **1.27.3**, pinned in `.go-version` and the Docker builder, and PostgreSQL
**18.1** for the supplied deployment and integration tests. `go.mod` retains its
minimum supported language version. Development HTTP is permitted only for an
explicit loopback selfhost configuration.

Configuration requires `DATABASE_URL` and a standard-Base64 32-byte
`BRIDGE_DATA_KEY`. Prefer their `_FILE` forms. Secret variables and their `_FILE`
counterparts cannot be set together. Bootstrap administration requires both
`BRIDGE_ADMIN_LOGIN` and `BRIDGE_ADMIN_PASSWORD` (or its `_FILE` form). Bootstrap
does not replace an existing administrator's password.

The executable accepts only these commands:

| Command | Behavior |
| --- | --- |
| `bridge-server serve` (default) | Migrate, optionally bootstrap, then run HTTP, relay and workers |
| `bridge-server migrate` | Apply idempotent migrations and exit; no notification/mail worker |
| `bridge-server healthcheck` | Probe local `/readyz` with the exact public Host; no database migration |

`GET /healthz` reports HTTP liveness. `GET /readyz` checks PostgreSQL with a
bounded timeout. Both require the configured public Host. The container probe
connects only locally, ignores environment proxies and refuses redirects.

For HTTPS, secret initialization, hosted SMTP and backup/recovery, follow the
[deployment runbook](docs/deployment.md). It documents operator actions; those
actions have not been performed by this implementation checkpoint. Umeng and
APNs are disabled unless their complete server configuration is provided.

## Local verification

**Use an isolated database only.** Integration tests create and drop their own
schemas. Setting a production database URL is not safe.

```sh
export BRIDGE_TEST_DATABASE_URL='postgresql://postgres@127.0.0.1:55440/bridge_integration?sslmode=disable'
go mod verify
go mod tidy -diff
go vet ./...
go test -count=1 -v ./...
python -B -m unittest discover -s scripts/tests -v
```

Database tests skip when the test URL is absent; that is not an integration pass.
CI supplies PostgreSQL and rejects skipped integration tests. Linux CI also runs
`go test -race`; a Windows run without a CGO-capable C compiler cannot establish
race coverage.

Generate/check only the public, deterministic protocol fixtures:

```sh
python -m pip install -r scripts/requirements-protocol.txt
python -B scripts/protocol_vectors.py --check
```

The fixed test keys must never be used in production. The matching fixture copies
in the desktop and mobile repositories must remain byte-identical. The workflow
also defines an ephemeral Docker migration/readiness smoke test; configuring a
workflow is not evidence that it has run successfully.
