import base64
import os
from pathlib import Path
import re
import sys
import tempfile
import unittest
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import init_deployment


class DeploymentInitializationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)

    def test_random_separate_keys_database_role_and_public_environment(self):
        folder = init_deployment.generate(self.root, "relay.example.test", "admin")
        values = {name: (folder / name).read_text().strip() for name in init_deployment.NAMES}
        self.assertEqual(len(base64.b64decode(values["data_key"], validate=True)), 32)
        self.assertEqual(values["admin_password"], "admin")
        self.assertNotEqual(values["database_password"], values["database_admin_password"])
        self.assertFalse((folder / "admin_setup_token").exists())
        dsn = urlsplit(values["database_url"])
        self.assertEqual((dsn.username, dsn.password, dsn.hostname, dsn.port, dsn.path),
                         ("bridge", values["database_password"], "database", 5432, "/bridge"))
        self.assertEqual(dsn.query, "sslmode=disable")
        environment = (self.root / ".env").read_text()
        self.assertEqual(environment, "BRIDGE_MODE=selfhost\nBRIDGE_DOMAIN=relay.example.test\nBRIDGE_ADMIN_LOGIN=admin\n")
        for name, secret in values.items():
            if name == "admin_password":
                continue
            self.assertNotIn(secret, environment)
        if os.name != "nt":
            self.assertEqual(folder.stat().st_mode & 0o777, 0o700)
            self.assertTrue(all((folder / name).stat().st_mode & 0o777 == 0o444 for name in values))

    def test_existing_configuration_or_secret_is_never_replaced(self):
        for relative in (".env", ".local/secrets/data_key"):
            with self.subTest(relative=relative), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                original = root / relative
                original.parent.mkdir(parents=True, exist_ok=True)
                original.write_text("preserve-existing-file")
                with self.assertRaises(FileExistsError):
                    init_deployment.generate(root, "relay.example.test", "admin")
                self.assertEqual(original.read_text(), "preserve-existing-file")
                self.assertFalse((root / ".local/secrets/database_password").exists())

    def test_invalid_domains_modes_and_usernames_fail_before_writes(self):
        for domain in ("https://relay.example.test", "Relay.example.test", "relay.example.test:443",
                       "relay.example.test/path", "${SECRET}.example.test", "relay.example.test\nOTHER=value"):
            with self.subTest(domain=domain), self.assertRaises(ValueError):
                init_deployment.generate(self.root, domain, "admin")
        for mode, login in (("hosted", "admin@example.test"), ("other", "admin"), ("selfhost", "line\nbreak"), ("hosted", "ab")):
            with self.subTest(mode=mode, login=login), self.assertRaises(ValueError):
                init_deployment.generate(self.root, "relay.example.test", login, mode)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_hosted_configuration_supports_setup_only_admin_without_email(self):
        folder = init_deployment.generate(self.root, "relay.example.test", "admin", "hosted")
        self.assertIn("BRIDGE_MODE=hosted", (self.root / ".env").read_text())
        self.assertIn("BRIDGE_ADMIN_LOGIN=admin", (self.root / ".env").read_text())
        self.assertEqual((folder / "admin_password").read_text().strip(), "admin")
        self.assertFalse((folder / "admin_setup_token").exists())

    def test_custom_admin_passwords_are_random_and_private(self):
        first = init_deployment.generate(self.root, "relay.example.test", "operator")
        with tempfile.TemporaryDirectory() as directory:
            second = init_deployment.generate(Path(directory), "relay.example.test", "operator")
            self.assertNotEqual((first / "admin_password").read_bytes(), (second / "admin_password").read_bytes())
        password = (first / "admin_password").read_text().strip()
        self.assertNotEqual(password, "admin")
        self.assertNotIn(password, (self.root / ".env").read_text())

    def test_symlinked_secret_directory_is_rejected(self):
        with tempfile.TemporaryDirectory() as outside:
            try:
                (self.root / ".local").symlink_to(outside, target_is_directory=True)
            except OSError as exc:
                self.skipTest(f"Directory symlinks are unavailable: {exc}")
            with self.assertRaisesRegex(ValueError, "inside"):
                init_deployment.generate(self.root, "relay.example.test", "admin")
            self.assertEqual(list(Path(outside).iterdir()), [])


class DeploymentContractTests(unittest.TestCase):
    def test_docker_and_ci_use_the_pinned_go_version(self):
        root = init_deployment.ROOT
        pin = (root / ".go-version").read_text().strip()
        self.assertRegex(pin, r"^\d+\.\d+\.\d+$")
        self.assertIn(f"ARG GO_VERSION={pin}", (root / "Dockerfile").read_text())
        self.assertIn("go-version-file: .go-version", (root / ".github/workflows/ci.yml").read_text())

    def test_compose_secrets_are_granted_only_to_the_required_services(self):
        compose = (init_deployment.ROOT / "compose.yaml").read_text()
        self.assertEqual(set(re.findall(r"    file: \./\.local/secrets/(\w+)", compose)), set(init_deployment.NAMES))
        server = compose.split("  server:\n", 1)[1].split("  https:\n", 1)[0]
        https = compose.split("  https:\n", 1)[1].split("\nsecrets:\n", 1)[0]
        self.assertNotIn("database_admin_password", server)
        self.assertNotIn("ports:", server)
        self.assertNotIn("secrets:", https)
        self.assertIn("BRIDGE_DATA_KEY_FILE:", server)
        self.assertNotIn("BRIDGE_ADMIN_SETUP_TOKEN_FILE:", server)
        self.assertIn("read_only: true", server)
        sql = (init_deployment.ROOT / "deploy/initdb.sql").read_text()
        self.assertIn("NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION", sql)
        self.assertIn("PASSWORD :'bridge_password'", sql)

    def test_public_ingress_is_https_only_without_http_redirects(self):
        root = init_deployment.ROOT
        compose = (root / "compose.yaml").read_text()
        self.assertNotIn("'80:80'", compose)
        self.assertIn("'443:443'", compose)
        caddy = (root / "deploy/Caddyfile").read_text()
        self.assertIn("auto_https disable_redirects", caddy)
        self.assertIn("https://{$BRIDGE_DOMAIN}", caddy)
        self.assertIn("disable_http_challenge", caddy)


if __name__ == "__main__":
    unittest.main()
