# HTTPS deployment and recovery runbook

These are operator instructions for an explicitly authorized deployment.
Code and isolated Go/PostgreSQL tests do not establish SMTP delivery or real-device
push acceptance. Actual checks and remaining release gates are recorded in
[implementation status](implementation-status.md).

Commands in this document target **Linux with Docker Engine and Compose v2**.
Docker, Caddy/ACME, filesystem mounts and recovery still require a rehearsal on
the intended host. Do not apply these commands to an existing deployment without
an identified backup and an approved change window.

## 1. Prepare a fresh installation

Choose a stable lower-case DNS hostname, such as `relay.example.com`, whose
records point to this host. The example `.test` hostname is test data, not a
public certificate/deployment target. The origin has no path, user information,
query, fragment or redundant `:443`. Origin changes create a different client
credential scope; redirects must not carry account/device credentials elsewhere.

Only Caddy publishes ports: TCP 443 for HTTPS/WebSocket relay, and optional UDP
443 for HTTP/3. HTTP port 80 is not published and HTTP redirects are disabled.
Automatic certificates use TLS-ALPN-01 on 443 (HTTP-01 is disabled); installations
using a different public HTTPS port must privately provision a DNS-validated
certificate or an appropriate DNS ACME issuer. Do not publish
PostgreSQL or the backend's plaintext port. The Compose database network is
internal. Other local container/host administrators remain trusted operators.

From a fresh checkout, initialize once:

```sh
python3 -B scripts/init_deployment.py --domain relay.example.com --admin-login admin
docker compose config --quiet
docker compose run --no-deps --rm --entrypoint caddy https validate --config /etc/caddy/Caddyfile --adapter caddyfile
```

The helper creates `.env` and five `.local/secrets/` files, with no secret values
on stdout. It refuses existing targets and symlink escapes; it is not a rotation,
upgrade or recovery tool. It creates separate PostgreSQL administrator and
application passwords, the application's database URL, an encryption key and
the setup-only administrator password. A fresh
default installation uses `admin/admin`; those credentials cannot create normal
sessions or authorize management, devices or relay access before a password
change. There is no manual initialization code. Public default credentials can
be claimed by another visitor, so log in and change the password immediately
after deployment, or use the initializer's custom administrator username option
to generate a random initial password. The `bridge` database role owns its database
but is not a superuser and cannot create databases or roles.

The secret directory is `0700`; individual bind-mounted files are `0444` so the
distinct non-root container UIDs can read their explicitly granted files. Do not
make the parent directory public or assume Compose remaps file ownership. Check
effective ACLs on non-POSIX hosts; Python's Windows `chmod` is not a POSIX ACL
security guarantee. Keep `.env`, secrets, dumps and operator overlays out of Git,
container build contexts, chat and diagnostic reports. Disable shell tracing when
handling any credential.

The default edge subnet is `172.30.74.0/28` and Caddy's fixed address is
`172.30.74.2`. If it conflicts with a host/VPN network, configure
`BRIDGE_EDGE_SUBNET` and `BRIDGE_PROXY_IP` together before creation. Only that
exact Caddy `/32` is trusted for client-IP forwarding; do not replace it with a
wildcard or a broad private-network CIDR. Preserve the public Host header and use
Caddy's normal forwarded-header handling, not client-supplied identity headers.
Outside explicit loopback development, the backend rejects plaintext account,
web and relay requests unless the configured trusted proxy confirms HTTPS with
`X-Forwarded-Proto: https`. The backend's internal HTTP socket is never published;
only local, credential-free liveness/readiness probes may use plaintext HTTP.

## 2. Hosted and push opt-ins

Selfhost is independent: username login, administrator-created accounts and no
hosted service-day check. The minimal installation does not contact a hosted
licensing server. If SMTP is absent, do not promise email password recovery.

Both modes use the same independent administrator username login. Administrators
do not require an email address. The website is administrator-only; ordinary
users use client APIs. Hosted ordinary users still register with email, verify
ownership and pass server-controlled service-day checks. Email verification and
reset links remain public security pages, not console login authority.

For a fresh hosted installation:

```sh
python3 -B scripts/init_deployment.py --domain relay.example.com --mode hosted
```

Before starting hosted mode, privately provision `.local/secrets/smtp_password`
and the public `SMTP_ADDRESS`, `SMTP_HOST`, `SMTP_FROM`, optional `SMTP_USER`, and
`SMTP_TLS` settings. Apply the explicit overlay for every subsequent Compose
operation on that stack:

```sh
docker compose -f compose.yaml -f deploy/compose.hosted.yaml config --quiet
docker compose -f compose.yaml -f deploy/compose.hosted.yaml up -d --build --wait
```

SMTP must use verified `starttls` or `implicit` TLS. Hosted startup fails when
SMTP configuration is missing; do not disable certificate verification or switch
production to selfhost to bypass this requirement. Actual verification/reset
mail delivery is a separate acceptance test.

Remote push is disabled by default in both modes. Add a private operator overlay
only after consent, server identity, client signing and device acceptance are
approved. Provision secrets privately and use file mounts:

| Server configuration | Meaning |
| --- | --- |
| `UMENG_APP_KEY`, `UMENG_APP_MASTER_SECRET_FILE`, `UMENG_PRODUCTION` | Matching Android app; **server HTTP signing** secret |
| `APNS_TEAM_ID`, `APNS_KEY_ID`, `APNS_TOPIC`, `APNS_PRIVATE_KEY_FILE`, `APNS_SANDBOX` | Matching Apple team/key/app topic and environment |

Never use the Android Message Secret as a server signing secret. It belongs only
to the opt-in Android build; App Master Secret and APNs private keys must never
enter a client artifact. Supply each provider's complete configuration together.
Keep providers disabled during an unapproved recovery or smoke test. A successful
provider HTTP response is not evidence of notification display, correct preview,
tap routing or receipt persistence on a device.

## 3. Start and observe

For the ordinary selfhost stack:

```sh
docker compose up -d --build --wait
docker compose exec -T server /bridge-server healthcheck
docker compose ps
```

The backend is non-root, read-only, drops capabilities and has bounded container
logs. It migrates before accepting HTTP traffic. Its healthcheck probes database
readiness locally, with the exact public Host and no authority headers. Caddy
waits for that healthcheck and obtains HTTPS certificates for the configured
hostname. External `/readyz`, certificate/hostname validity and WebSocket upgrade
must also be tested from an authorized client; local readiness does not test TLS.

Open `/admin` on the configured HTTPS origin. For a new default installation,
enter `admin/admin` to go directly to the mandatory password-change page. No
manual initialization code or email address is required. The resulting ten-minute setup cookie or
API setup token can only change administrator credentials, not access account or
management APIs. Set a different password (12 to 256 bytes); optionally change
the username, then sign in again. All setup sessions are invalidated on success.
The page defaults to Chinese; `?lang=en` selects English and stores a language
preference. Ordinary users cannot create or reuse website sessions, including
pre-upgrade website cookies; their client bearer login remains independent.

The public `/` homepage and authenticated `/admin` console use separate layouts.
The console uses the available screen width, a dedicated navigation rail and
responsive data tables; it is not a small panel inside the homepage. Runtime
metrics describe server-wide live connections. The device and session sections
are explicitly scoped to the signed-in administrator, not every user's devices.
User and audit tables show the latest 200 records, with local user filtering.
Hosted administrators can generate and manage codes in a separate console
section. Each batch supports 1-100 codes and 1-3650 service days per code. The
server returns plaintext only at generation; save the private CSV before leaving
the page or generating another batch. If automatic download is blocked, use the
visible download link for that batch. The latest-200 code list contains metadata
only; used/revoked codes cannot be exported again. Unused codes can be revoked,
and client redemption history is read-only. After an uncertain network failure,
inspect the metadata list before resubmitting to avoid unintentionally generating
another batch. The console still has no personal-entitlement or redemption form.
Desktop client redemption remains available through bearer-authenticated
`/v1/account/redeem`; website cookies are rejected. Privileged code
generation/revocation APIs are not available to ordinary clients. Selfhost has no
official service-day code issuance. No mobile purchase/redemption UI is introduced
by this update.

Bootstrap settings do not reset an existing administrator or create another one
when the login changes. Existing email-named administrators remain compatible.
Existing initialization-code files and variables are no longer required or read.
Do not rerun the initializer over existing secrets when upgrading.
Use authenticated administration for account
creation, reset/disable, device revocation and hosted code/audit management; do
not use QR/account membership as desktop approval. Do not expose credentials in
URLs, command arguments, screenshots or shared logs.

An operator who explicitly needs to reset the sole administrator can run:

```sh
docker compose run --rm --no-deps server reset-admin
```

Use all overlays belonging to that installation. This local-only command restores
`admin/admin` in setup-only state, and revokes old login sessions and mail tokens.
Account ID, entitlement, device rows, approved bindings and public keys are
retained. Clients must authenticate and reenroll with their existing local keys.
The command refuses zero or multiple administrators; there is no HTTP reset route.

Plan upgrades with a verified backup. Record source revision and image digests,
build the intended version, and check migration compatibility in an isolated
restore before restarting production. `migrate` has no mail/push worker, but does
require normal configuration and applies database changes. There is no automated
destructive downgrade or general data-key rotation procedure. Never use
`docker compose down --volumes` against a retained deployment.

## 4. Backup

A PostgreSQL dump is sensitive: it includes account/password hashes, entitlement,
device/session metadata and durable notification/mail state. Some fields depend
on `BRIDGE_DATA_KEY`; possession of a dump does not replace that key. Encrypt
backups at rest, restrict access, define retention/RPO/RTO and maintain an
off-host copy. Back up the original data key **separately** in a recoverable secret
store, with its version and database-backup association. Loss of that key makes
encrypted state unrecoverable. Do not regenerate it for a restored database.

On the correctly identified stack, an application-role logical backup is:

```sh
backup=".local/backups/$(date -u +%Y%m%dT%H%M%SZ)"
umask 077
mkdir -p "$backup"
docker compose exec -T database sh -c 'PGPASSWORD="$(cat /run/secrets/database_password)" exec pg_dump -h 127.0.0.1 -U bridge -d bridge --format=custom --no-owner --no-acl' > "$backup/bridge.dump"
test -s "$backup/bridge.dump"
```

Use the hosted/other overlays when they belong to that stack. Record a checksum,
source revision, PostgreSQL version and UTC time, and verify the dump with
`pg_restore --list`. Do not copy a live PostgreSQL data directory as a substitute
for a consistent database backup. Preserve the public configuration, relevant
secret-store material and TLS operational state according to the host's backup
policy. None of these backup/recovery commands has been rehearsed here.

## 5. Isolated recovery drill

1. Identify an empty recovery host/Compose project and the intended backup.
   Confirm it is not the production volume or project. Keep backend, HTTPS,
   vendor push and real SMTP workers stopped; publish no recovery ports.
2. Restore the public configuration and matching private secret material. Keep
   the **original** data key. A restored origin/account/device identity is not a
   new installation; do not run the initializer over restored files or silently
   replace their identity. If database credentials change, provision the matching
   role password and `DATABASE_URL_FILE` together through the secret workflow.
3. Start only `database` in the empty recovery project. Its initialization creates
   the non-superuser application role and empty `bridge` database. Confirm the
   dump target is empty; do not clean/drop a populated database to force success.
4. Restore with the matching PostgreSQL client, stopping on any error:

   ```sh
   docker compose exec -T database sh -c 'PGPASSWORD="$(cat /run/secrets/database_password)" exec pg_restore -h 127.0.0.1 -U bridge -d bridge --exit-on-error --single-transaction --no-owner --no-acl' < .local/backups/SELECTED_BACKUP/bridge.dump
   docker compose run --rm server migrate
   ```

5. Validate row/entitlement totals, password login, encrypted-state access, revoked
   sessions/devices, pairing ownership, expired work and notification-ID/receipt
   continuity using synthetic credentials. An empty schema/migration pass does
   not prove a real encrypted-data recovery.
6. For a hosted backend drill, use an approved isolated TLS SMTP sink. Starting a
   restored backend with real SMTP can resume queued mail. Rehearse database
   failure/restart, origin/TLS, direct-to-push transition and no unknown-write
   replay; keep vendor credentials absent until live device tests are authorized.
7. Record measured recovery time and the acceptance results. Production cutover,
   DNS changes, reinstating live providers and any cleanup require a separate
   approval. Recovery and load acceptance remain unverified release gates.
