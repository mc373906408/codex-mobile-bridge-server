"""Reproducible public test keys only; never use fixed ephemerals in production."""
import base64
import hashlib
import json
import struct
import uuid
from pathlib import Path

from noise.connection import Keypair, NoiseConnection
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat


def public(private):
    return X25519PrivateKey.from_private_bytes(private).public_key().public_bytes(
        Encoding.Raw, PublicFormat.Raw)


def connection(pattern, private, remote, prologue, initiator, ephemeral):
    n = NoiseConnection.from_name(pattern.encode("ascii"))
    n.set_as_initiator() if initiator else n.set_as_responder()
    n.set_prologue(prologue)
    n.set_keypair_from_private_bytes(Keypair.STATIC, private)
    n.set_keypair_from_public_bytes(Keypair.REMOTE_STATIC, remote)
    n.set_keypair_from_private_bytes(Keypair.EPHEMERAL, ephemeral)
    n.start_handshake()
    return n


def vectors():
    origin = "https://relay.example.test"
    ids = [uuid.UUID("00000000-0000-4000-8000-00000000000" + str(i)) for i in range(1, 5)]
    dp, mp, de, me, dpp, mpp = [bytes(range(i, i+32)) for i in (1, 33, 65, 97, 129, 161)]
    dp_pub, mp_pub = public(dp), public(mp)
    authority = struct.pack("!H", len(origin)) + origin.encode("ascii")
    cp = b"bridge/control/v1\0" + authority + b"".join(x.bytes for x in ids[:3]) + dp_pub + mp_pub
    a = connection("Noise_KK_25519_ChaChaPoly_BLAKE2s", mp, dp_pub, cp, True, me)
    b = connection("Noise_KK_25519_ChaChaPoly_BLAKE2s", dp, mp_pub, cp, False, de)
    first = bytes(a.write_message())
    assert not b.read_message(first)
    second = bytes(b.write_message())
    assert not a.read_message(second)
    desktop_record = b.encrypt(b'{"kind":"ready"}')
    assert a.decrypt(desktop_record) == b'{"kind":"ready"}'
    mobile_record = a.encrypt(b'{"kind":"key_confirm"}')
    assert b.decrypt(mobile_record) == b'{"kind":"key_confirm"}'
    e = {"v": 1, "notification_id": str(ids[3]), "binding_id": str(ids[1]), "expires_at": 1793450000}
    pp = b"bridge/preview/v1\0" + authority + ids[3].bytes + ids[1].bytes + struct.pack("!q", e["expires_at"])
    plaintext = b'{"title":"Bridge","body":"Private test preview","event_id":"test-event","gateway_id":"' + str(ids[0]).encode() + b'"}'
    p = connection("Noise_K_25519_ChaChaPoly_BLAKE2s", dpp, public(mpp), pp, True, bytes(range(193, 225)))
    ciphertext = bytes(p.write_message(plaintext))
    out = {"origin": origin, "gateway_id": str(ids[0]), "binding_id": str(ids[1]), "channel_id": str(ids[2])}
    for name, value in {"desktop_private": dp, "mobile_private": mp, "desktop_public": dp_pub, "mobile_public": mp_pub,
                        "desktop_ephemeral": de, "mobile_ephemeral": me, "control_prologue": cp,
                        "kk_first": first, "kk_second": second, "desktop_record": desktop_record, "mobile_record": mobile_record,
                        "desktop_preview_private": dpp, "mobile_preview_private": mpp,
                        "desktop_preview_public": public(dpp), "mobile_preview_public": public(mpp),
                        "preview_prologue": pp, "preview_plaintext": plaintext}.items():
        out[name] = base64.b64encode(value).decode("ascii")
    e["ciphertext"] = base64.b64encode(ciphertext).decode("ascii")
    out["envelope"] = e
    return out


def preview_vectors():
    origin = "https://relay.example.test"
    gateway = "00000000-0000-4000-8000-000000000001"
    binding = "00000000-0000-4000-8000-000000000002"
    notification = "00000000-0000-4000-8000-000000000004"
    desktop, mobile, ephemeral = [bytes(range(start, start + 32)) for start in (129, 161, 193)]
    envelope = {"v": 1, "notification_id": notification, "binding_id": binding, "expires_at": 2_000_000_600}
    prologue = (b"bridge/preview/v1\0" + struct.pack("!H", len(origin)) + origin.encode("ascii")
                + uuid.UUID(notification).bytes + uuid.UUID(binding).bytes + struct.pack("!q", envelope["expires_at"]))
    scope = hashlib.sha256((origin + '\0' + gateway + '\0' + binding + '\0fixture-stream').encode()).hexdigest()
    plaintext = {"title": "Bridge fixture", "body": "Private fixture preview", "gateway_id": gateway,
                 "binding_id": binding, "notification_id": notification, "scope": scope,
                 "event_id": "cloud:" + notification}
    raw = json.dumps(plaintext, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    sender = connection("Noise_K_25519_ChaChaPoly_BLAKE2s", desktop, public(mobile), prologue, True, ephemeral)
    envelope["ciphertext"] = base64.b64encode(sender.write_message(raw)).decode("ascii")
    credentials = {"origin": origin, "gateway_id": gateway, "binding_id": binding,
                   "private_key": base64.b64encode(mobile).decode("ascii"),
                   "desktop_public_key": base64.b64encode(public(desktop)).decode("ascii")}
    return {"format": 1, "now": 2_000_000_000, "credentials": credentials, "envelope": envelope,
            "plaintext": plaintext, "expected": {**plaintext, "origin": origin, "expires_at": envelope["expires_at"]},
            "plaintext_base64": base64.b64encode(raw).decode("ascii"),
            "desktop_private": base64.b64encode(desktop).decode("ascii"),
            "ephemeral_private": base64.b64encode(ephemeral).decode("ascii")}


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    folder = Path(__file__).resolve().parents[1] / "protocol"
    for name, data in {"vectors.json": vectors(), "preview.json": preview_vectors()}.items():
        path = folder / name
        text = json.dumps(data, indent=2, sort_keys=True) + "\n"
        if args.check:
            assert path.read_text(encoding="utf-8") == text, name + " protocol vectors differ"
        else:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text, encoding="utf-8")
