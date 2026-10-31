# Cloud relay implementation checkpoint

Local implementation only. No commit, push, deployment, production credential
access, or live vendor push is authorized by this checkpoint. The private
operations repository is outside the implementation scope.

The following describes the evolving local worktrees on 2026-10-30, not a
published revision, successful hosted CI run, signed distribution, or device
delivery result. Existing unrelated work is preserved.

## Contract

- The MIT Go/PostgreSQL core serves both hosted and independent selfhost modes.
- Hosted entitlement is account-wide service days, redeemed on desktop/web only.
- An exact canonical HTTPS origin and account ID scope all stored credentials.
- Control uses Noise KK; preview uses separate keys and single-message Noise K.
- A five-minute QR, key confirmation, encrypted proof, and explicit local
  approval are all required. Cloud account membership is not desktop approval.
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
