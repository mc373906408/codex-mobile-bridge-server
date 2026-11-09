# Cloud relay implementation checkpoint

## Registration trial campaigns deployed on 2026-11-09

The hosted instance runs source `d35a7a502f2975411e0fb730e85c3af483857974`
as `bridge-server:registration-campaigns-20261109-d35a7a5`. The running binary
SHA256 is `056ea5a77412a9c01bcfd47bb681bf14787ad3f1a8be795515c2bb17ec484da7`.
Migration `005_registration_campaigns.sql` adds campaign administration and
atomic trials for verified new registrations. Its default seven-day campaign
remains a draft with no grants; production activation is a separate admin action.

An encrypted production dump was restored to a separate database for two migration
runs. All 21 existing business tables and 423 rows were preserved. The application
was then stopped for a fresh backup and the same production migration comparison.
Existing account, entitlement, redemption and device data remain intact; database,
HTTPS and P2P containers, secret mounts and configuration are retained. The prior
image and encrypted backups remain available; the rehearsal database was removed.

The deployed revision passed Linux PostgreSQL race CI and container smoke tests.
Production TLS, readiness, admin authentication boundaries, nine embedded assets
and external HTTPS checks passed. Local functional acceptance includes 109 Go tests,
14 Node tests and eight Chinese/English desktop/mobile-width browser runs.
No production campaign was activated and no real signup, email or push was triggered
for release verification. This scoped restore/migration check does not certify a
full off-host recovery. See the [campaign decision](../.agents/notes/implemented/feature/2026-11-09-registration-trial-campaigns.md).

## Persistent relay v2 deployed on 2026-11-05

The hosted instance runs `bridge-server:relay-v2-32987e26dac75937`, binary SHA256
`d149ab63fef4ac0d235711aa782d7fc4de1c48b9e22dab26e0faafcc7d52886d`.
The incremental `003_refresh_recovery.sql` migration preserves existing accounts,
entitlements, devices, bindings and redemption data. Database/HTTPS containers and
credential mounts are unchanged; a private database backup and the prior image
are retained. TLS, readiness, capability `[2]`, admin and unauthorized-access
checks passed. Desktop and Lenovo Android have been upgraded to the matching protocol.

The real Go/Python/Rust/Dart business phase completed 107,648 requests in 1,800
seconds on one business handshake. Separate final tests cover concurrent SSE,
inbox/list reads, 20 MiB download/upload, heartbeat, automatic reconnect, listener
cancellation and unknown-write no-replay. Go/PostgreSQL 118 tests pass without
skips; deployment-host isolated regression and actual candidate startup pass.
Background encrypted test push reached a durable phone ACK. iOS verification is
limited to static project checks. See the [v2 decision](../.agents/notes/implemented/architecture/2026-11-05-persistent-relay-v2.md)
for the original failures and exact validation boundaries.

For the current architecture, permission flows and reproduced gaps, see the
[server architecture audit](../.agents/notes/implemented/architecture/2026-11-03-server-architecture-and-scope-audit.md).
The dated checkpoints below retain their original verification scope.

## U-Push configured and verified on 2026-11-05

The existing server image now loads the matching U-Push AppKey and server-only
App Master Secret through a private Compose secret file. Only the application
container was recreated; database and HTTPS containers retained their identities.
Trusted TLS, readiness and `push.umeng=true` passed verification. This deployment
uses `UMENG_PRODUCTION=false` for the existing debug test phone.

The updated Lenovo L71091 app initializes cloud notifications after login/restore
without an app toggle. Its active endpoint registered successfully. One generic
encrypted test delivered while the app was backgrounded: the provider accepted
the first attempt, Android posted the matching task notification, and the native
receipt reconciled to server status `acked`. This is test-device evidence, not a
signed production release or iOS/force-stop acceptance. See the
[push configuration decision](../.agents/notes/implemented/bug-fix/2026-11-05-cloud-push-configuration.md).

## Per-account redemption batch limit deployed on 2026-11-04

The hosted instance runs `bridge-server:redemption-batches-3f6d61f36b7a8cc0`,
binary SHA256 `1727f7df25822356101ad87ff05a23bd2cdc6adb4da7d0137e54a19b1c88f4cd`.
Each administrator generation creates one UUID batch. An account can redeem one
code from that batch; different batches extend service, while retrying the same
code returns its original receipt. Duplicate batch claims return HTTP 409
`redemption_batch_already_used` and do not consume the other code.

The current 001 baseline is upgraded by `002_redemption_batches.sql`. Historical
generation groups and their receipts count toward the limit. The deployment
preserved 2 accounts, 51 code digests across 2 batches, 1 redemption and the existing
gateway/mobile/binding; semantic data hashes match before and after migration.
The application was stopped before migration; PostgreSQL, HTTPS and credentials
were retained. The retired server image was removed after verification.

The PostgreSQL-backed Go suite passes 114 tests without skips; vet, administrator
UI and desktop error-translation tests pass. The deployment host repeated the
four storage batch tests and the cross-session API test three times in an isolated
database, then verified actual candidate executable startup. Public TLS, health,
capability and static-resource checks pass. No production code was consumed for
testing. See the [batch-limit decision](../.agents/notes/implemented/feature/2026-11-04-redemption-batch-account-limit.md).

## Relay close isolation deployed on 2026-11-04

The hosted instance now runs `bridge-server:relay-stability-1fd6a04962d31b61`
with binary SHA256 `884e5430674b87d3165098089d75688fff21ac8f73654ce481b48cb736753c69`.
Normal mobile request completion could retire a channel before the desktop
worker's final Close reached the server; the old router treated that acknowledgement
as an unauthorized route, disconnecting the shared gateway and every other phone.
The regression reproduces that failure before the fix.

Recently closed routes are remembered per gateway connection with a 128-entry,
one-minute bound. Matching late frames are dropped, while unknown/mismatched or
cross-gateway routes remain rejected. Phone channels cannot cross a gateway
reconnect, and a slow phone no longer cancels its shared desktop connection.
The 109-test PostgreSQL-backed Go suite, vet and module verification pass. Linux
WebSocket verification on the deployment host passes 400 channel-close cycles
while another channel keeps communicating. Windows race detection was not run.

This update retains the current database, accounts, passwords, devices, bindings,
credential mounts and HTTPS/database containers; it does not repeat the earlier
fresh-database initialization. Public TLS/health checks pass and the desktop has
reconnected. Phone/desktop reinstallation is unnecessary. See the
[relay close decision](../.agents/notes/implemented/bug-fix/2026-11-04-relay-channel-close-isolation.md)
for evidence and diagnostic limitations.

## Current-schema deployment on 2026-11-04

The hosted instance at `https://bridge.waken.cloudns.be:8787` now runs
`bridge-server:current-aed977f0cef2e2fb`, built from the verified local working tree
with Go 1.27.3 for Linux amd64. The running binary SHA256 is
`3a6010aec363594527d5440c4549e25043d00b73c564b4ad983128f9e8666dda`.

The fresh `bridge_current_20261104` database uses only `001_current.sql` and starts
with `admin/admin`, restricted to mandatory initial setup. The deployment does not
change this password on behalf of the operator. Previous development accounts and
devices need registration/enrollment again. At the operator's explicit request,
the old application database, ten old server images, historical release packages
and old database backups are removed after the new service passes verification.
No rollback backup is created. PostgreSQL and HTTPS containers, certificates and
SMTP configuration continue in place.

An isolated production-image smoke test covers default login, setup-only access,
password change, fresh login, administrator search/paging and target-account routes,
then restart without restoring default credentials. Its database/container are
removed. Thirteen public checks pass before and after cleanup, covering trusted
TLS/HSTS/readiness, capabilities/admin page, unauthenticated rejection, retired
routes and exact static-asset hashes. No real email or push is sent; Umeng/APNs are
not configured on this deployment. The [deployment decision](../.agents/notes/implemented/process/2026-11-04-current-schema-server-deployment.md)
records the operational entry point, cleanup and evidence.

## Administrator management and audit on 2026-11-04

Default `admin/admin` first login and local `reset-admin` remain as explicitly
requested. They grant only short-lived setup permission until a different strong
password is saved and the administrator signs in again; startup never resets an
existing administrator. The first-claim window remains a documented tradeoff.

The console now searches all users on the server and pages through users, devices,
bindings, sessions, codes, redemptions and audit. Selecting a user scopes its device
and session panels; privileged revocation uses separate target-account routes,
checks current administrator authority and ownership, closes affected sockets and
cancels relevant queued/leased pushes. Ordinary client routes remain own-account.
State-changing authorization events and audit entries commit or roll back together,
with safe before/after metadata and no credentials. Directory polling and unchanged
endpoint registration do not append duplicate audit events. Details and verification
are recorded in the [management and audit decision](../.agents/notes/implemented/bug-fix/2026-11-04-admin-management-and-audit.md).

## Current prerelease contracts on 2026-11-04

The current source requires fresh development storage and supersedes the historical
data-preservation and migration claims below. `001_current.sql` creates the current
schema directly; retired migration histories are rejected. Manual cloud pairing
routes/tables, old account-bound email verification and pending-account password
activation are removed. Registration only creates an account after email proof
and password completion. Desktop and mobile use the automatic directory and
explicit desktop permission with independent encrypted proof.

Push withdrawal checks the current phone and binding ownership independently of
desktop access or entitlement. Closing and later restoring a connection does not
restore withdrawn notification consent. The [current schema decision](../.agents/notes/implemented/simplification/2026-11-04-prerelease-account-and-binding-schema.md)
and [push withdrawal fix](../.agents/notes/implemented/bug-fix/2026-11-04-push-withdrawal-independent-of-desktop-access.md)
record the local verification. No deployment, data reset on an existing installation,
real email or vendor push occurs in this source cleanup.

## Account directory and reversible permission on 2026-11-03

- Phone login/enrollment discovers stable pending relationships with every
  same-account desktop in either login order. Directory reads do not create
  bindings or grant trust. Pending/offline devices remain visible.
- `GET /v1/mobile/bindings` and the extended gateway directory return identity,
  display name, status, online state and access revision. The desktop-only access
  POST separates reversible `disabled` from permanent `revoked`; every close
  advances the fence so late openings cannot override it. Close cancels only the
  target socket and its queued/leased notification jobs, retaining independent consent.
- Migration 007 preserves active identities, keys and revocation tombstones, and
  fills only missing relationships. Updated clients require `account_directory_v1`
  and `desktop_switch_v1`; no manual-request/QR fallback is added.
- Desktop first approval still checks encrypted proof and safety codes. The
  phone performs cancellable foreground sync independent of the page/chat lock,
  and has read-only cloud device info rather than removal/withdrawal controls.
- The authorized hosted deployment now runs
  `bridge-server:account-directory-5b20bb6e0005edb8`. An authenticated encrypted
  backup and private restore/migration/rollback-image rehearsal pass. Only the
  server container changes; database/proxy identities, secrets and existing
  account/device identity snapshots are preserved. The running binary matches the
  verified release manifest, and public TLS/HSTS/readiness/capabilities and
  unauthenticated directory rejection pass.
- The first Compose attempt is rejected on an invalid null build field before
  replacing any container. After read-only reconciliation, a separate validated
  release/rollback overlay succeeds with a fresh encrypted backup. Original
  backups and diagnostics remain on the host; production data is never restored
  automatically.
- Go verification has 106 passing PostgreSQL-backed test events. Coordinated
  desktop/mobile regression and Android ARM64 debug build/install pass; the
  installed APK checksum matches the verified artifact, with user data retained.
  No real phone is approved automatically. Desktop controller/gateway reload
  remains a manual operator action; real-device login/approval/push and signed iOS
  acceptance are not claimed.

## Desktop-approved pairing on 2026-11-03

This follow-up is source-only and is not deployed by these edits. Phone requests
replace cloud QR invitations and have no expiry. The computer requires encrypted
proof, matching safety codes and an explicit per-device approval switch; account
membership cannot authorize a phone. Desktop/Flutter/Rust/Go consumers change
together. Migration 006 preserves active identity, rejects old pending QR records
and prevents time-based session cleanup from deleting retained devices. Existing
direct QR login, E2EE, entitlement, CSRF, owner and notification-consent rules stay
independent. A production update requires backup/rehearsal and coordinated clients.

The 2026-10-30 checkpoint was local-only and did not authorize a commit, push,
deployment or production credential access. The later server-only deployment
below was explicitly requested by the operator. The private operations
repository's credential files were not changed, committed, removed or rotated
by this update.

The historical cross-repository checks describe the local worktrees on 2026-10-30, not a
published revision, successful hosted CI run, signed distribution, or device
delivery result. Existing unrelated work is preserved.

## Sixteen-character redemption codes on 2026-11-02

- New codes encode ten bytes from `crypto/rand` as 16 uppercase Base32 characters
  (`A-Z`, `2-7`), with no fixed prefix, time component or truncated UUID. All
  80 random bits are retained. Shorter codes improve usability; they do not have
  more entropy than the previous 192-bit codes or a full UUID.
- Record IDs remain UUIDs. Existing 39-character codes, their digests and redeemed
  entitlements are unchanged. Both formats use the same single-use, revocation,
  normalization and idempotency rules; no schema migration was added or changed.
- Hosted capabilities report `redemption_code_length: 16`; selfhost reports zero
  and keeps official redemption disabled. The website accepts both generation
  formats during rollout and still copies actual codes, not record IDs.
- Local verification passes: 90 Go test events with no skipped test, including
  fresh PostgreSQL-backed code/compatibility/concurrency tests and persisted
  account/IP redemption limits; `go vet`, module verification and formatting;
  four Node production-UI tests with mocked DOM/clipboard/network; nine deployment
  helper tests. No browser window or native desktop input was automated. Race
  detector, remote CI and real-device acceptance are not claimed.
- The hosted backend now runs `bridge-server:redemption16-3b7b1e4dc62c4e0c` after
  an on-host authenticated encrypted backup and private restore/migration/rollback
  rehearsal. Only the backend was recreated; database/proxy identities and all
  existing credential/certificate files stayed unchanged. Public TLS, HSTS,
  readiness and generation-length capabilities pass. No production code was
  generated, redeemed or revoked for verification; no account, email or push test
  was submitted. Existing website tabs require a manual refresh before generating
  a new batch so their old 39-character-only UI validator is replaced.

## Email-first registration on 2026-11-02

- This correction is local-only: no commit, deployment, real account creation or
  SMTP delivery. The previously deployed instance is not updated by these edits.
- The desktop registration form puts email, code entry and **Get code** together.
  Password fields appear only after successful email verification; account creation
  happens only after the second password/confirmation step. Success returns to
  sign-in without enabling relay or granting device authority.
- `registration-code` creates an account-independent challenge;
  `registration-verify` exchanges a code for a ten-minute single-use proof;
  `register` requires that proof and a password. Codes and proofs are scoped to
  exact server/email, resends invalidate both, and failed code guesses persist.
- Migration 005 preserves accounts, reset mail and device records, and gives
  pre-account signup mail a validated challenge owner. The worker checks that
  owner and cancels stale deliveries. Legacy unverified signups can finish with
  fresh proof and a new password; requesting/verifying the new code does not
  activate their old password. Old password-first clients fail closed.
- Local checks pass: 78 Go test events with no skipped test, including isolated
  PostgreSQL 18.1 migration/concurrency tests, and `go vet`; 28 Python cloud tests;
  1,326 Node tests; 58 installed Edge wide/narrow browser cases. Registration
  screenshots are visually inspected. WebKit, race-detector and signed-device
  acceptance are not claimed.
- The desktop repository still has the pre-existing upward import in
  `cloud_connector.py:416`; this registration-only change does not alter it or
  add a boundary exception. A running controller needs restarting to load the
  new IPC/Python modules; actual signup also requires deploying the server update.

## Email verification codes on 2026-11-01

- This follow-up is local-only: no commit, deployment, real signup or email send.
  It does not update the previously deployed instance described below.
- Hosted registration/resend now issue six-digit email codes only. The old
  `/verify` page, `/v1/auth/verify` endpoint and their email-link UI are removed.
  Password-reset links remain separate; administrator and device authority rules
  are unchanged.
- Migration 004 removes legacy verification tokens/mail and preserves verified
  accounts and password-reset state. New code rows use account-scoped HMAC
  digests, ten-minute expiry, single-use consumption and five committed failed
  attempts. Durable signup/resend limits and pending-password/code replacement
  prevent unrestricted retries and stale pending-signup activation.
- Go tests pass with 68 test events, including subtests, against a fresh isolated
  PostgreSQL 18.1 Windows cluster; no database-dependent test is skipped. Legacy
  routes, migration, reset, expiry, resend, attempt exhaustion, concurrency and
  account isolation are covered. SMTP MIME encoding has a local round-trip test,
  not a live delivery test.
- Desktop checks pass: 27 Python cloud tests, 1,325 Node tests and 46 installed
  Edge wide/narrow browser cases. Registration/code-entry screenshots are visually
  inspected. No WebKit, mobile-device or race-detector acceptance is claimed.
- Old verification-link clients must be updated; there is no link fallback.

## Administrator console and HTTPS update on 2026-10-31

- Both modes support independent administrator usernames without email. New
  default `admin/admin` credentials allow only a ten-minute setup flow. At the
  operator's request, the manual initialization code was removed; a different strong
  password before account, device or management authority is granted. Successful
  setup invalidates all setup sessions and requires a new login. The local-only
  `reset-admin` command preserves account/device identity and revokes old sessions.
- The website is administrator-only. Ordinary users cannot create website
  sessions or reuse old website cookies, including after email verification.
  Hosted email registration, verification and client bearer login remain available;
  email verification/reset links do not grant console login authority.
- Chinese is the default page language, with an English option and persistent
  language preference. Static text, errors, status messages and dates are localized.
- Public ingress is HTTPS-only: no published HTTP port, redirects or HTTP-01
  challenge. The backend requires real TLS or an unambiguous HTTPS assertion from
  the explicitly trusted proxy. Plaintext is allowed only for credential-free local
  readiness/liveness probes and explicit loopback selfhost development.
- Pinned Go 1.27.3 module verification/tidy-diff, vet and 34 PostgreSQL-backed Go
  tests passed with no skips against an isolated ephemeral PostgreSQL 18.1
  container. Nine deployment-helper tests and JavaScript syntax checks passed.
  Windows had no CGO-capable C compiler; this run does not establish race coverage.
- Browser checks on synthetic data covered first login, password confirmation,
  mandatory credential change, fresh administrator login, local user creation,
  and ordinary-user login rejection in Chinese and English. Layout checks found
  no horizontal overflow at 390px and 1280px. Screenshot capture was unavailable;
  these are functional/DOM layout checks, not pixel-level visual acceptance.
- The Linux image was built offline and Caddy configuration validated. A private
  database restore rehearsed migration and administrator reset under a
  non-superuser role, preserving account IDs, public keys and approved bindings.
  No mail/push worker ran in the rehearsal. The temporary test database and local
  preview/tunnel processes were removed after verification.
- The selected server was switched to hosted mode after an authenticated
  AES-256-GCM backup. Only the backend container was recreated; the database and
  HTTPS proxy containers, original data key, account IDs, device keys and bindings
  were retained. Existing administrator sessions were revoked for initial setup.
- Live checks passed for TLS hostname/certificate verification, readiness, HSTS,
  Chinese/English pages, administrator-only capabilities, mandatory credential
  change, public HTTP rejection and untrusted forwarded-protocol rejection.
  SMTP TLS authentication and the sender domain's verified sending status passed.
  No real test email or vendor push was sent; mailbox delivery, real-device
  reconnection, signing and store acceptance remain separate checks.

### No-code follow-up verification

The operator removed the manual initialization-code requirement. The login form
now contains only username and password; deployment creates five secret files
and no code secret or mount. Internal ten-minute, single-use setup sessions,
CSRF protection, mandatory password change, administrator-only website access
and HTTPS enforcement remain intact. Existing unused code files were not deleted.

All 34 Go/PostgreSQL tests and nine deployment-helper tests passed again with no
skips. The restored-database migration retained administrator credentials and
device identity. Live HTTPS checks and browser login without a code reached the
restricted password-change page, not management controls; no new password was
entered. Chinese/English and settled 390px/1280px layouts passed functional/DOM
checks. Public plaintext HTTP and untrusted forwarded HTTPS were rejected.

Only the backend was recreated for this follow-up. Current account credentials,
data-key/SMTP files, account IDs, public keys, bindings, database and proxy were
preserved; `reset-admin` was not run. An on-host DNS failure caused a verified
automatic rollback before retrying with a loopback HTTPS probe that retained
canonical Host, SNI and certificate verification. Public HTTPS checks then passed.

### Dedicated administrator console follow-up

The operator requested a real management console rather than a small homepage
panel. `/` now remains a public service homepage, including when visited with an
administrator cookie. Authenticated `/admin` uses a separate full-width template,
navigation rail, live server connection metrics, responsive user/device/session
tables and audit history. User and audit views describe their latest-200 limits;
device/session views explicitly describe the administrator-account scope.

Personal entitlement, redemption and code-configuration forms were initially removed from
the browser. The later code-generation correction restores administrator code
issuance in its own section, not personal redemption. Client bearer redemption
still works; administrator website cookies
are rejected before redeeming. Privileged code APIs remain for operator tooling,
not ordinary clients. No desktop/mobile source, credentials, keys or billing data
were reset or migrated to a different account.

All 36 Go/PostgreSQL tests and nine deployment-helper tests passed with no skips.
Module verification, tidy-diff, vet and JavaScript syntax checks passed. Synthetic
hosted/selfhost browser checks covered login, search, enable-user, local-account
creation, fresh data loading, Chinese/English and separate authenticated homepage
navigation. Layout checks passed at 1440px, 1280px and 390px; desktop and mobile
screenshots of synthetic data were captured and visually inspected.

The offline image and isolated restored-database migration preserved credentials
and device identity. Only the production backend was recreated after an
authenticated encrypted backup. The existing administrator browser session loaded
the new console without reentering credentials, with five navigation items, no
homepage hero or redemption forms, and no data-loading error. Public bilingual
homepage/login, readiness, HSTS and plaintext HTTP rejection passed again. The
database/proxy containers and all credential files were unchanged; `reset-admin`
was not run. This update did not send a real test email or vendor push.

### Administrator code-generation correction on 2026-11-01

The operator clarified that administrators still need code generation. Hosted
`/admin` therefore restores a dedicated code-management section: service days and
batch size, generation and private CSV export, unused-code revocation and
read-only client redemption history. The download fallback retains only the
latest batch in page memory; plaintext is never put in the metadata list or
browser storage, and the server cannot recover it. The wide management layout,
separate homepage and client-only service redemption are unchanged. Selfhost
does not show hosted service-day code controls.

All 36 Go/PostgreSQL tests and nine deployment-helper tests passed with no skips;
module verification, tidy-diff, vet and JavaScript syntax checks passed. The
synthetic hosted browser generated a two-code batch and downloaded the real CSV;
its header, record count and code shape were verified without disclosing plaintext.
Unused-code revocation, Chinese/English, download cleanup on navigation, invalid
batch-size rejection and 1440px/390px DOM layouts passed. Selfhost had no code
controls. Screenshot capture was unavailable for this correction, so these new
layout checks are functional/DOM verification rather than pixel-level acceptance.

The migration-only restored-database rehearsal retained credentials and device
identity. Production received only a backend recreation after a verified encrypted
backup. The existing administrator browser session loaded six navigation items,
the code-generation form and metadata/history without error; it did not generate
any production codes. Public homepage/login, readiness, HSTS and plaintext HTTP
rejection passed. Database/proxy containers and credential files were unchanged;
no administrator reset, real email or vendor push was performed.

## Contract

- The MIT Go/PostgreSQL core serves both hosted and independent selfhost modes.
- Hosted entitlement is account-wide service days, redeemed through client bearer
  authentication, not administrator website cookies. Desktop redemption is
  implemented; the mobile app currently has no purchase/redemption UI.
- An exact canonical HTTPS origin and account ID scope all stored credentials.
- Control uses Noise KK; preview uses separate keys and single-message Noise K.
- Stable same-account discovery, key confirmation, encrypted proof, and explicit
  local desktop permission are required. Cloud account membership is not trust.
- Records are bounded to 32 KiB; unknown writes are never automatically replayed.
- Preview plaintext is at most 1 KiB; full APNs JSON is at most 4 KiB. Invalid or
  unavailable previews permit only fixed generic local text or suppression.
- A durable random ID is shared by direct/push per event and binding. Receipt is
  persisted before ACK. Direct lease is 45 seconds; expiry is one hour.
- Initial and re-enabled notification observation establishes a quiet baseline.
- Consent withdrawal blocks new notifications but retains preview credentials
  and unexpired receipts for deduplication/reconciliation. Local binding/account
  cleanup, native SDK disable, authenticated endpoint disable, and vendor metadata
  deletion are distinct. Offline failure is not confirmed remote revocation.

## Implemented locally

- Server account/authentication, entitlement, device/pairing, encrypted relay,
  push provider, SMTP and worker sources, PostgreSQL migrations and integration
  tests, and hosted/selfhost web controls exist.
- Deployment sources include isolated database/backend networks, secret-file
  injection, separate application/database roles, Caddy HTTPS configuration,
  read-only/no-new-privileges service settings, and readiness healthchecks.
  `/healthz` is liveness; `/readyz` checks database availability. Initialization
  helpers refuse existing configuration/secrets and require an explicit hosted
  administrator. See [deployment instructions](deployment.md).
- Server CI sources include pinned Go, PostgreSQL-backed tests, Linux race checks
  that reject integration skips, and isolated Compose migration/readiness smoke
  checks. These configurations have not been executed by hosted Actions here.
- Desktop vault-backed exact-origin accounts, Noise connector, private runtime
  wiring, Electron account/pairing/revocation controls, and a durable encrypted
  notification outbox exist. Passive notification observation preserves the
  original coding-client owner. Legacy `/api/mobile/push` remains a separate
  protocol; it is not the new cloud `/v1` path.
- Mobile Rust relay APIs, generated Dart bindings, direct/relay persistence
  migration, cloud account/pairing/settings UI, passive endpoint/receipt
  controller, and native consent paths exist. There are no mobile purchase or
  redemption links and no offline command queue.
- Android production U-Push is an explicit release/profile build opt-in with
  separate credentials and consent. Ordinary release/profile remain SDK-free;
  the Lenovo debug manual-console trial remains independent.
- iOS ordinary/cloud build graphs are isolated. `RunnerCloud` embeds the
  preview-only `BridgeNotificationService`; ordinary iOS does not enable APNs or
  share its preview vault. Bounded atomic/fsync App Group metadata and a separate
  device-local preview Keychain group support receipt-before-ACK. The NSE has no
  account/control credentials, Flutter runtime, or networking.
- Mobile CI sources include core/preview Rust checks, Android compilation,
  macOS Swift/C ABI tests, and serial ordinary/cloud unsigned iOS builds with
  distinct artifacts and structure checks. Platform builds intentionally do not
  use `--no-pub`, so Flutter can regenerate mode-specific plugin registrations.
- Mobile privacy, consent/withdrawal, release-gate and debug-trial documentation,
  and desktop cloud/legacy-push documentation have been updated. Historical
  2026-10-28 SDK-free artifacts and the 2026-10-29 Lenovo console experiment are
  explicitly not acceptance of the current cloud variants.

## Local verification on 2026-10-30

- Go formatting, module verification/tidy-diff, vet, Windows executable build,
  and PostgreSQL-backed suites passed, including readiness, auth rotation/reset,
  entitlement concurrency, pairing isolation/revocation, and push lease/ACK
  eligibility. This is not a Linux race or full deployed HTTP/device test.
- Seven deployment-helper tests passed. Pinned public Noise-vector generation
  `--check` passed; all three desktop/mobile/server copies match byte-for-byte.
  Generator dependencies are `noiseprotocol==0.3.1` and `cryptography==50.0.2`.
- Desktop frontend: 1,216 tests passed in 99 files, no failures or skips.
- Mobile: Flutter analysis passed; 196 tests passed with one deliberate host-FFI
  skip, and the separate real-library FFI test passed. Subsequent Dart-only
  formatting touched two files; the final format check covers 49 files with no
  remaining changes.
- Rust core: format/clippy and 42 tests passed. Preview crate: format/clippy and
  five tests passed. These are host checks, not iOS compilation or device crypto.
- Mobile Python helpers: 49 tests passed. Static iOS checks passed with 145
  project objects, one preview producer, and five shared sources.
- Android default/opt-in Kotlin/native compilation and debug unit checks passed
  using nonfunctional credentials. No production APK/AAB, signing, vendor send,
  device-registration, or native-library alignment acceptance is established.
- Both CI YAML files and Compose YAML parsed with `js-yaml`; structural assertions
  cover triggers/permissions/concurrency, race failure propagation, Caddy command
  isolation, private service networks, and serial native variant/artifact checks.
  These are source/syntax assertions, not Actions, Docker, Caddy, or Xcode runs.
- Twelve Markdown files and 56 local path/anchor links passed local path,
  Markdown-anchor and whitespace checks. Documentation edits do not establish
  platform or deployment readiness.

### Backend regression checkpoint

The complete Python environment is an isolated test venv, not system Python or
production data. The earlier system-Python run had missing `noiseprotocol`
errors. Those errors disappeared in the correct-environment full rerun; Paramiko
4.0.0 was installed only into that isolated venv.

The completed full source-only run before the status-file repair ran
2,720 tests with one failure, two errors, and 185 platform/native/optional skips:

- SSH reverse-forward registration failed because a polling Windows reader
  denied atomic status JSON replacement (`WinError 5`), killing the forwarding
  thread. The shared JSON writer now retries only Windows sharing errors for at
  most two seconds, retains the original document on failure, cleans temporary
  files, and propagates persistent/other errors. Eight new storage tests and all
  19 connection-setup tests passed after this repair, including real SSH bytes
  and forwarding shutdown. No SSH authentication or host-key checks were relaxed.
  A subsequent notification regression ran 62 tests successfully, and all 21
  cloud account/crypto/notification tests passed in the same isolated venv.
- The real-listener network fixture reported an unacknowledged shutdown for a
  selected LAN address. Its explicit unavailable metadata binary remains in
  place; this error is not the earlier Git-helper temporary-directory cleanup
  diagnosis. Three subsequent repetitions and the latest full post-repair run
  passed, retaining both loopback-only and selected-LAN cases and all
  shutdown/cleanup assertions. The earlier intermittent shutdown failure remains
  a regression risk; no production listener/lifecycle change was made to hide it.
- The native ConPTY persistent-shell test could not confirm the shell process
  while reading its current directory. It failed in all three repeated venv runs.
  A traced synthetic terminal showed `python.exe -> python.exe -> cmd.exe`: the
  Windows venv redirector adds an interpreter between the owned bootstrap and
  shell, while the existing check requires a direct shell child. The terminal
  source and test are unchanged from HEAD. Three separate unmodified runs with
  the base interpreter passed; those are diagnostics, not a corrected full-venv
  result. No production terminal code or process-identity checks were changed.

The latest complete post-repair source-only run used the same complete venv
(`D:/github/.codex-tools/protocol-venv/Scripts/python.exe`) and
`-B scripts/test-backend.py --source-only`. It ran 2,728 tests in 682.668 seconds:
zero failures, one error, and 185 platform/native/optional skips (exit status 1).
Its only remaining error is the ConPTY shell-identity check described above.
SSH reverse-forward, real-listener, cloud, and the eight new storage tests passed
in this full run. The complete backend suite remains failed; do not substitute
the base-interpreter diagnostic passes for this result, convert the error into a
skip, or ignore cleanup/shutdown assertions.

## Temporary test service

The isolated PostgreSQL cluster on local port 55440 was stopped successfully with
`pg_ctl stop -m fast -w`; a subsequent status check reports no server running.
Its test-only data is preserved. No Compose or production service was started.

## Required verification

- Resolve or explicitly retain the full-backend failures with matching evidence.
- Run Linux Go race/PostgreSQL and isolated Docker/Caddy readiness/migration
  checks; exercise documented backup/recovery and failure behavior.
- Run macOS Swift/native C ABI tests and ordinary/cloud Xcode builds; inspect
  actual Mach-O architectures, app/NSE signatures/profiles, minimum iOS 15.0,
  App Group and separate preview Keychain access on a signed iPhone build.
- Build the exact production Android APK/AAB and ABI set; verify every packaged
  native library, 16 KB ELF/ZIP/bundle alignment, dependencies, independent signing,
  and absence of server App Master Secrets and debug-trial credentials.
- Observe successful hosted CI for the final authorized revision. An older green
  run does not validate these dirty worktrees.
- Verify signed-device consent denial/withdrawal, foreground/background/locked/
  killed-process delivery, unavailable keys, direct/push races and restart dedupe,
  quiet baseline, account/binding expiry/revocation, endpoint rotation, offline
  withdrawal, safe generic fallback/suppression, and exactly-once NSE completion.

Source checks, native unit tests, provider acceptance and unsigned artifacts do
not establish device delivery, legal compliance, or store approval. Installation,
live push, publication and deployment require separate authorization.
