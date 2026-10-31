"""Initialize a fresh local Compose configuration without printing or replacing secrets."""

import argparse
import base64
import os
from pathlib import Path
import re
import secrets

ROOT = Path(__file__).resolve().parents[1]
NAMES = ("database_admin_password", "database_password", "database_url", "data_key", "admin_password")


def generate(root, domain, admin_login, mode="selfhost"):
    if mode not in {"selfhost", "hosted"}:
        raise ValueError("Deployment mode must be selfhost or hosted")
    if len(domain) > 253 or not re.fullmatch(r"(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}", domain):
        raise ValueError("Use a lower-case DNS hostname without a URL, port or path")
    pattern = r"[a-z0-9][a-z0-9_.-]{2,63}" if mode == "selfhost" else r"[a-z0-9._+%-]+@[a-z0-9.-]+\.[a-z]{2,63}"
    if not re.fullmatch(pattern, admin_login) or len(admin_login) > 254:
        raise ValueError("Use a local administrator username, or an email address in hosted mode")
    root = Path(root).resolve()
    folder = root / ".local/secrets"
    if not folder.resolve().is_relative_to(root) or (root / ".local").is_symlink() or folder.is_symlink():
        raise ValueError("Deployment secrets must remain inside this checkout")
    paths = [root / ".env", *(folder / name for name in NAMES)]
    if any(path.exists() or path.is_symlink() for path in paths):
        raise FileExistsError("Refusing to replace an existing configuration or secret; use a fresh checkout")

    password = secrets.token_urlsafe(32)
    values = {
        "database_admin_password": secrets.token_urlsafe(32),
        "database_password": password,
        "database_url": f"postgresql://bridge:{password}@database:5432/bridge?sslmode=disable",
        "data_key": base64.b64encode(secrets.token_bytes(32)).decode("ascii"),
        "admin_password": secrets.token_urlsafe(32),
    }
    folder.mkdir(parents=True, mode=0o700)
    os.chmod(folder, 0o700)
    for name, value in values.items():
        with (folder / name).open("x", encoding="ascii", newline="\n") as stream:
            stream.write(value + "\n")
        # Compose bind-mounts files without remapping ownership. The 0700 parent
        # protects the host; only explicitly granted containers can read each mount.
        os.chmod(folder / name, 0o444)
    with (root / ".env").open("x", encoding="ascii", newline="\n") as stream:
        stream.write(f"BRIDGE_MODE={mode}\nBRIDGE_DOMAIN={domain}\nBRIDGE_ADMIN_LOGIN={admin_login}\n")
    os.chmod(root / ".env", 0o600)
    return folder


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--domain", required=True)
    parser.add_argument("--mode", choices=("selfhost", "hosted"), default="selfhost")
    parser.add_argument("--admin-login", default="admin")
    args = parser.parse_args()
    folder = generate(ROOT, args.domain, args.admin_login, args.mode)
    print(f"Created .env and secret files in {folder}; no service was started.")
    print("Keep the directory private, back up data_key separately, and read admin_password locally before first login.")


if __name__ == "__main__":
    main()
