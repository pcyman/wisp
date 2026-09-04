from __future__ import annotations

import http.client
import json
import stat
import tempfile
import threading
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock

from credentials import broker


NOW = datetime(2026, 1, 2, 3, 4, 5, tzinfo=timezone.utc)


def role_config(**overrides: object) -> broker.RoleConfig:
    values = {
        "alias": "development",
        "profile": "company-development",
        "role_arn": "arn:aws:iam::123456789012:role/Wisp",
        "region": "eu-west-1",
        "duration_seconds": 3600,
        "eks_cluster": "development-cluster",
    }
    values.update(overrides)
    return broker.RoleConfig(**values)


def credentials(expiration: datetime, suffix: str = "one") -> dict[str, object]:
    return {
        "AccessKeyId": f"access-{suffix}",
        "SecretAccessKey": f"secret-{suffix}",
        "SessionToken": f"token-{suffix}",
        "Expiration": expiration,
    }


class RoleConfigTests(unittest.TestCase):
    def load(self, text: str, requested_alias: str | None = None) -> broker.RoleConfig:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.toml"
            path.write_text(text, encoding="utf-8")
            return broker.load_role_config(str(path), requested_alias)

    @staticmethod
    def document(
        alias: str = "development",
        *,
        default: str | None = None,
        profile: object = "company-development",
        role_arn: object = "arn:aws:iam::123456789012:role/Wisp",
        region: object = "eu-west-1",
        duration: object = 3600,
        eks_cluster: object = "development-cluster",
    ) -> str:
        lines = ["schema_version = 1", "", "[aws]"]
        if default is not None:
            lines.append(f"default = {json.dumps(default)}")
        lines.extend(["", f"[aws.aliases.{json.dumps(alias)}]"])
        values = {
            "profile": profile,
            "role_arn": role_arn,
            "region": region,
            "duration_seconds": duration,
            "eks_cluster": eks_cluster,
        }
        for key, value in values.items():
            if value is not None:
                lines.append(f"{key} = {json.dumps(value)}")
        return "\n".join(lines) + "\n"

    def test_parses_nested_aws_alias_and_ignores_other_application_fields(self) -> None:
        text = (
            'schema_version = 1\n[images]\nsandbox = "ignored"\n'
            '[aws]\ndefault = "development"\n'
            '[aws.aliases.development]\nprofile = "profile"\n'
            'role_arn = "arn:example"\nregion = "us-east-1"\n'
            'duration_seconds = 900\neks_cluster = "cluster_1"\n'
        )
        self.assertEqual(
            self.load(text),
            broker.RoleConfig(
                "development", "profile", "arn:example", "us-east-1", 900, "cluster_1"
            ),
        )

    def test_requested_alias_takes_precedence_over_default(self) -> None:
        text = (
            '[aws]\ndefault = "first"\n'
            '[aws.aliases.first]\nprofile = "first-profile"\nrole_arn = "arn:first"\n'
            '[aws.aliases.second]\nprofile = "second-profile"\nrole_arn = "arn:second"\n'
        )
        selected = self.load(text, "second")
        self.assertEqual(selected.alias, "second")
        self.assertEqual(selected.profile, "second-profile")

    def test_default_alias_is_selected(self) -> None:
        text = (
            '[aws]\ndefault = "second"\n'
            '[aws.aliases.first]\nprofile = "one"\nrole_arn = "arn:one"\n'
            '[aws.aliases.second]\nprofile = "two"\nrole_arn = "arn:two"\n'
        )
        self.assertEqual(self.load(text).alias, "second")

    def test_sole_alias_is_selected(self) -> None:
        self.assertEqual(self.load(self.document()).alias, "development")

    def test_multiple_aliases_without_selection_are_rejected(self) -> None:
        text = (
            '[aws]\n[aws.aliases.first]\nprofile = "one"\nrole_arn = "arn:one"\n'
            '[aws.aliases.second]\nprofile = "two"\nrole_arn = "arn:two"\n'
        )
        with self.assertRaisesRegex(ValueError, "multiple AWS aliases"):
            self.load(text)

    def test_missing_aws_or_alias_tables_are_rejected(self) -> None:
        for text in ("schema_version = 1\n", "[aws]\n", "[aws]\naliases = 1\n"):
            with self.subTest(text=text), self.assertRaises(ValueError):
                self.load(text)

    def test_unconfigured_requested_and_default_aliases_are_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "not configured"):
            self.load(self.document(), "production")
        with self.assertRaisesRegex(ValueError, "not configured"):
            self.load(self.document(default="production"))

    def test_alias_name_validation_boundaries(self) -> None:
        valid = ["a", "A-Z_a.0", "0", "development-west_2"]
        invalid = ["", " ", " development", "development\t", "has space", "slash/name", "colon:name", "ümlaut"]
        for alias in valid:
            with self.subTest(alias=alias):
                self.assertEqual(self.load(self.document(alias=alias)).alias, alias)
        for alias in invalid:
            with self.subTest(alias=alias), self.assertRaisesRegex(ValueError, "alias is invalid"):
                self.load(self.document(alias=alias))

    def test_empty_or_whitespace_selected_alias_does_not_fall_back(self) -> None:
        for selected in ("", " ", "\tdevelopment", "development\n"):
            with self.subTest(selected=selected), self.assertRaisesRegex(
                ValueError, "selected AWS alias is invalid"
            ):
                self.load(self.document(default="development"), selected)

        for selected_default in ("", " ", "development "):
            with self.subTest(default=selected_default), self.assertRaisesRegex(
                ValueError, "selected AWS alias is invalid"
            ):
                self.load(self.document(default=selected_default))

    def test_profile_validation(self) -> None:
        for value in (None, "", 1, True):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "requires a profile"):
                self.load(self.document(profile=value))

    def test_role_arn_validation(self) -> None:
        for value in (None, "", "aws:role", 1, True):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "requires a role_arn"):
                self.load(self.document(role_arn=value))
        self.assertEqual(self.load(self.document(role_arn="arn:")).role_arn, "arn:")

    def test_region_validation(self) -> None:
        self.assertIsNone(
            self.load(self.document(region=None, eks_cluster=None)).region
        )
        for value in ("", 1, True):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "invalid region"):
                self.load(self.document(region=value, eks_cluster=None))

    def test_duration_boundaries_and_default(self) -> None:
        text_without_duration = self.document().replace("duration_seconds = 3600\n", "")
        self.assertEqual(self.load(text_without_duration).duration_seconds, 3600)
        for value in (900, 43200):
            with self.subTest(value=value):
                self.assertEqual(self.load(self.document(duration=value)).duration_seconds, value)
        for value in (899, 43201, 0, -1, True, "3600"):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "between 900 and 43200"):
                self.load(self.document(duration=value))

    def test_eks_cluster_validation_boundaries(self) -> None:
        valid = ["a", "A_0-x", "a" * 100]
        invalid = ["", "-cluster", "_cluster", "has.dot", "has space", "a" * 101]
        for value in valid:
            with self.subTest(value=value):
                self.assertEqual(self.load(self.document(eks_cluster=value)).eks_cluster, value)
        for value in invalid + [1, True]:
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "invalid eks_cluster"):
                self.load(self.document(eks_cluster=value))

    def test_eks_cluster_requires_region(self) -> None:
        with self.assertRaisesRegex(ValueError, "requires a region"):
            self.load(self.document(region=None, eks_cluster="cluster"))


class SSOInitializationTests(unittest.TestCase):
    def test_copies_only_regular_json_files_with_private_mode(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            home = root / "home"
            source.mkdir()
            (source / "token.json").write_text('{"token":true}', encoding="utf-8")
            (source / "UPPER.JSON").write_text("ignored", encoding="utf-8")
            (source / "notes.txt").write_text("ignored", encoding="utf-8")
            (source / "directory.json").mkdir()
            (source / "target.json").write_text("target", encoding="utf-8")
            (source / "link.json").symlink_to(source / "target.json")

            with mock.patch.object(broker.Path, "home", return_value=home):
                broker.initialize_sso_cache(str(source))

            destination = home / ".aws" / "sso" / "cache"
            self.assertEqual(
                sorted(path.name for path in destination.iterdir()),
                ["target.json", "token.json"],
            )
            self.assertEqual((destination / "token.json").read_text(encoding="utf-8"), '{"token":true}')
            self.assertEqual(stat.S_IMODE((destination / "token.json").stat().st_mode), 0o600)
            self.assertFalse((destination / "link.json").exists())

    def test_does_not_overwrite_or_delete_existing_destination(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            home = root / "home"
            destination = home / ".aws" / "sso" / "cache"
            source.mkdir()
            destination.mkdir(parents=True)
            (source / "token.json").write_text("new", encoding="utf-8")
            existing = destination / "token.json"
            existing.write_text("existing", encoding="utf-8")

            with mock.patch.object(broker.Path, "home", return_value=home):
                with self.assertRaises(FileExistsError):
                    broker.initialize_sso_cache(str(source))

            self.assertEqual(existing.read_text(encoding="utf-8"), "existing")


class CredentialCacheTests(unittest.TestCase):
    def setUp(self) -> None:
        self.client = mock.Mock()
        self.factory = mock.Mock(return_value=self.client)
        self.config = role_config()

    def test_initial_assume_role_fields_and_response_mapping(self) -> None:
        self.client.assume_role.return_value = {
            "Credentials": credentials(NOW + timedelta(hours=1))
        }
        cache = broker.CredentialCache(self.config, self.factory, lambda: NOW)

        result = cache.get()

        self.factory.assert_called_once_with(self.config)
        self.client.assume_role.assert_called_once_with(
            RoleArn="arn:aws:iam::123456789012:role/Wisp",
            RoleSessionName="wisp-20260102T030405Z",
            DurationSeconds=3600,
        )
        self.assertEqual(
            result,
            {
                "AccessKeyId": "access-one",
                "SecretAccessKey": "secret-one",
                "Token": "token-one",
                "Expiration": "2026-01-02T04:04:05Z",
            },
        )

    def test_string_expiration_is_preserved_in_response(self) -> None:
        cache = broker.CredentialCache(self.config, self.factory, lambda: NOW)
        cache._credentials = credentials(NOW + timedelta(hours=1))
        cache._credentials["Expiration"] = "2026-01-02T04:04:05Z"
        cache._expires_soon = mock.Mock(return_value=False)
        self.assertEqual(cache.get()["Expiration"], "2026-01-02T04:04:05Z")

    def test_refreshes_at_five_minute_margin_but_not_after_it(self) -> None:
        cache = broker.CredentialCache(self.config, self.factory, lambda: NOW)
        cache._credentials = credentials(NOW + timedelta(minutes=5), "old")
        self.client.assume_role.return_value = {
            "Credentials": credentials(NOW + timedelta(hours=1), "new")
        }
        self.assertEqual(cache.get()["AccessKeyId"], "access-new")
        self.client.assume_role.assert_called_once()

        self.client.reset_mock()
        cache._credentials = credentials(NOW + timedelta(minutes=5, microseconds=1), "current")
        self.assertEqual(cache.get()["AccessKeyId"], "access-current")
        self.client.assume_role.assert_not_called()

    def test_serves_previous_unexpired_credentials_after_refresh_failure(self) -> None:
        cache = broker.CredentialCache(self.config, self.factory, lambda: NOW)
        cache._credentials = credentials(NOW + timedelta(minutes=1), "previous")
        self.client.assume_role.side_effect = RuntimeError("STS unavailable")
        with self.assertLogs(broker.LOG, level="ERROR"):
            result = cache.get()
        self.assertEqual(result["AccessKeyId"], "access-previous")

    def test_refresh_failure_raises_after_previous_credentials_expire(self) -> None:
        cache = broker.CredentialCache(self.config, self.factory, lambda: NOW)
        cache._credentials = credentials(NOW, "expired")
        self.client.assume_role.side_effect = RuntimeError("STS unavailable")
        with self.assertRaisesRegex(RuntimeError, "STS unavailable"):
            cache.get()

    def test_initial_failure_is_raised(self) -> None:
        self.client.assume_role.side_effect = RuntimeError("SSO unavailable")
        cache = broker.CredentialCache(self.config, self.factory, lambda: NOW)
        with self.assertRaisesRegex(RuntimeError, "SSO unavailable"):
            cache.get()


class HTTPServerTests(unittest.TestCase):
    TOKEN = "Basic test-token"

    def setUp(self) -> None:
        self.cache = mock.Mock()
        self.cache.get.return_value = {
            "AccessKeyId": "access",
            "SecretAccessKey": "secret",
            "Token": "token",
            "Expiration": "2026-01-02T04:04:05Z",
        }
        self.config = role_config()
        self.server = broker.CredentialServer(("127.0.0.1", 0), self.TOKEN, self.cache, self.config)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)

    def request(
        self, path: str, authorization: str | None = None
    ) -> tuple[int, dict[str, object], dict[str, str], bytes]:
        connection = http.client.HTTPConnection(*self.server.server_address, timeout=2)
        headers = {} if authorization is None else {"Authorization": authorization}
        connection.request("GET", path, headers=headers)
        response = connection.getresponse()
        raw = response.read()
        result = (response.status, json.loads(raw), dict(response.getheaders()), raw)
        connection.close()
        return result

    def assert_json_response(
        self,
        response: tuple[int, dict[str, object], dict[str, str], bytes],
        status: int,
        body: dict[str, object],
    ) -> None:
        actual_status, actual_body, headers, raw = response
        self.assertEqual(actual_status, status)
        self.assertEqual(actual_body, body)
        self.assertEqual(headers["Content-Type"], "application/json")
        self.assertEqual(headers["Content-Length"], str(len(raw)))
        self.assertEqual(headers["Cache-Control"], "no-store")

    def test_health_does_not_require_authentication(self) -> None:
        self.assert_json_response(self.request("/health"), 200, {"status": "ok"})
        self.cache.get.assert_not_called()

    def test_configuration_requires_exact_authorization(self) -> None:
        for supplied in (None, "", "Basic wrong-token", self.TOKEN + " "):
            with self.subTest(supplied=supplied):
                self.assert_json_response(
                    self.request("/configuration", supplied),
                    401,
                    {"error": "unauthorized"},
                )

        self.assert_json_response(
            self.request("/configuration", self.TOKEN),
            200,
            {
                "alias": "development",
                "region": "eu-west-1",
                "eks_cluster": "development-cluster",
            },
        )

    def test_credentials_requires_authentication_and_maps_cache_response(self) -> None:
        self.assert_json_response(
            self.request("/credentials"), 401, {"error": "unauthorized"}
        )
        self.assert_json_response(
            self.request("/credentials", self.TOKEN),
            200,
            self.cache.get.return_value,
        )
        self.cache.get.assert_called_once_with()

    def test_unknown_path_is_404_even_with_no_authentication(self) -> None:
        self.assert_json_response(
            self.request("/unknown"), 404, {"error": "not found"}
        )

    def test_credentials_failure_returns_503(self) -> None:
        self.cache.get.side_effect = RuntimeError("unavailable")
        with self.assertLogs(broker.LOG, level="ERROR"):
            response = self.request("/credentials", self.TOKEN)
        self.assert_json_response(response, 503, {"error": "credentials unavailable"})


if __name__ == "__main__":
    unittest.main()
