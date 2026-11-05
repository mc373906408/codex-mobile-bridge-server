# Persistent encrypted relay v2

Clients negotiate `bridge-relay-v2`; capability `relay_protocols` is `[2]`.
Unsupported WebSocket subprotocols receive HTTP 426. Existing account/device keys,
binding IDs, preview v1 envelopes and pairing safety codes are retained.

The outer frame remains `version:u8 | kind:u8 | binding:uuid | channel:uuid | payload`.
Version is 2; kinds are Open=1, encrypted Record=2 and Close=3. Open allocates a
persistent binding session, not an HTTP request. The Noise prologue is
`bridge/control/v2\0` followed by the existing exact origin, route IDs and pinned
public keys. Noise KK and its key-confirmation/desktop-authorization exchange are
unchanged. One writer must encrypt and send records in order; ciphertext must not
be dropped or reordered within a surviving session.

After authorization, control plaintext is `0x00 | JSON`. Every stream control
includes `stream_id`, a positive integer below 2^53. Binary body plaintext is
`0x01 | stream_id:uint64_be | bytes`. Maximum plaintext record is 32768 bytes,
leaving 32759 bytes for a body fragment. IDs increase without reuse; the receiver
accepts concurrent arrival within the last 1024 allocated IDs and rejects reused
request IDs. Late records for retired streams are ignored after decryption.

Each stream carries the existing request metadata (`id`, method, path, headers,
content type, length), then request body credit/data/request_end, response_start,
response body credit/data/response_end and response_ack. Credit is 1–4 chunks,
initial window 4. `cancel` terminates only its stream. `stream_error` contains a
public category and ends only that request. A cryptographic or framing failure
terminates the entire binding session. Response success requires authenticated
response_end; a lost final acknowledgement cannot turn a complete response into
a truncated one. Stream completion never closes the WebSocket.

At most 8 streams share a session. Each direction has a bounded per-stream queue.
Files over 1 MiB or file endpoints acquire one large-transfer slot per session,
and at most two per desktop. SSE is exempt from cumulative file size limits.
Idle sessions send `{kind:"ping",stream_id:0,id:<uuid>}` after 20 seconds; the peer
returns the corresponding pong. A missing response after 10 seconds ends the
transport. The sender fairly selects plaintext before encryption; the relay
fairly selects binding queues while preserving each binding's ciphertext order.

`BRIDGE_BYTES_PER_SECOND` now paces each binding direction instead of disconnecting
the desktop on a one-second burst. Overloaded bindings close independently.
Limits, protocol errors and rejected upgrades are logged as categories, without
tokens, private keys, HTTP paths or application contents.

Hosted authorization is server-owned: initial device/binding/entitlement checks,
periodic review, an expiry deadline based on database time, and explicit revocation.
Client entitlement queries are display-only and are never scheduled automatically.
Rediscovery/network errors do not revoke a verified desktop identity. A new
connection repeats Noise and desktop authorization; writes whose outcome is
unknown are never retransmitted by connection recovery.

Refresh API `POST /v1/auth/refresh` requires `refresh_token` and UUID `request_id`.
Clients persist that pair before the first attempt. The server rotates once and
stores the encrypted result for five minutes. Identical retries recover identical
tokens with the remaining original access lifetime, including after a server
restart. A different request ID reusing a spent token revokes the session. A
superseded rotation cannot overwrite newer credentials. Expired recovery results
are cleaned while spent-token theft evidence remains.
