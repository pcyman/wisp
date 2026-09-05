#!/usr/bin/env python3
"""Loopback-only AWS container credential endpoint backed by STS AssumeRole."""

from __future__ import annotations

import hmac
import json
import logging
import os
import re
import shutil
import stat
import threading
import tomllib
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable


LOG = logging.getLogger("aws-credential-broker")
ALIAS_PATTERN = re.compile(r"^[A-Za-z0-9._-]+$")
EKS_CLUSTER_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$")
REFRESH_MARGIN = timedelta(minutes=5)


@dataclass(frozen=True)
class RoleConfig:
    alias: str
    profile: str | None
    role_arn: str
    region: str | None
    duration_seconds: int
    eks_cluster: str | None


def initialize_sso_cache(source_path: str) -> None:
    """Copy host SSO tokens into per-run tmpfs so botocore can refresh them."""
    source = Path(source_path)
    if not source.is_dir():
        return
    destination = Path.home() / ".aws" / "sso" / "cache"
    destination.mkdir(parents=True, exist_ok=True)
    for token_file in source.iterdir():
        if token_file.suffix != ".json":
            continue
        try:
            before = token_file.stat(follow_symlinks=False)
        except OSError:
            continue
        if not stat.S_ISREG(before.st_mode):
            continue

        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        try:
            source_fd = os.open(token_file, flags)
        except OSError:
            continue
        opened = os.fstat(source_fd)
        if (
            not stat.S_ISREG(opened.st_mode)
            or (opened.st_dev, opened.st_ino) != (before.st_dev, before.st_ino)
        ):
            os.close(source_fd)
            continue

        destination_file = destination / token_file.name
        destination_fd = -1
        destination_created = False
        try:
            destination_fd = os.open(
                destination_file,
                os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0),
                0o600,
            )
            destination_created = True
            with os.fdopen(source_fd, "rb") as source_stream:
                source_fd = -1
                with os.fdopen(destination_fd, "wb") as destination_stream:
                    destination_fd = -1
                    shutil.copyfileobj(source_stream, destination_stream)
                    destination_stream.flush()
                    os.fchmod(destination_stream.fileno(), 0o600)
        except Exception:
            if destination_fd >= 0:
                os.close(destination_fd)
            if destination_created:
                destination_file.unlink(missing_ok=True)
            raise
        finally:
            if source_fd >= 0:
                os.close(source_fd)


def load_role_config(path: str, requested_alias: str | None) -> RoleConfig:
    with Path(path).open("rb") as config_file:
        document = tomllib.load(config_file)

    aws = document.get("aws")
    if not isinstance(aws, dict):
        raise ValueError("configuration must contain an [aws] table")

    aliases = aws.get("aliases")
    if not isinstance(aliases, dict) or not aliases:
        raise ValueError("configuration must contain at least one [aws.aliases.NAME] table")

    if requested_alias is not None:
        alias = requested_alias
    elif "default" in aws:
        alias = aws["default"]
    elif len(aliases) == 1:
        alias = next(iter(aliases))
    else:
        raise ValueError("multiple AWS aliases are configured but no default was selected")

    if (
        not isinstance(alias, str)
        or alias.strip() != alias
        or not ALIAS_PATTERN.fullmatch(alias)
    ):
        raise ValueError("the selected AWS alias is invalid")
    if alias not in aliases:
        raise ValueError(f"AWS alias is not configured: {alias}")

    entry = aliases[alias]
    if not isinstance(entry, dict):
        raise ValueError(f"AWS alias must be a table: {alias}")

    profile = entry.get("profile")
    role_arn = entry.get("role_arn")
    region = entry.get("region")
    duration = entry.get("duration_seconds", 3600)
    eks_cluster = entry.get("eks_cluster")
    if profile is not None and (not isinstance(profile, str) or not profile):
        raise ValueError(f"AWS alias {alias!r} has an invalid profile")
    if not isinstance(role_arn, str) or not role_arn.startswith("arn:"):
        raise ValueError(f"AWS alias {alias!r} requires a role_arn")
    if region is not None and (not isinstance(region, str) or not region):
        raise ValueError(f"AWS alias {alias!r} has an invalid region")
    if not isinstance(duration, int) or isinstance(duration, bool) or not 900 <= duration <= 43200:
        raise ValueError(f"AWS alias {alias!r} duration_seconds must be between 900 and 43200")
    if eks_cluster is not None and (
        not isinstance(eks_cluster, str) or not EKS_CLUSTER_PATTERN.fullmatch(eks_cluster)
    ):
        raise ValueError(f"AWS alias {alias!r} has an invalid eks_cluster")
    if eks_cluster is not None and region is None:
        raise ValueError(f"AWS alias {alias!r} requires a region when eks_cluster is configured")

    return RoleConfig(alias, profile, role_arn, region, duration, eks_cluster)


class CredentialCache:
    def __init__(
        self,
        config: RoleConfig,
        client_factory: Callable[[RoleConfig], Any] | None = None,
        now: Callable[[], datetime] | None = None,
    ) -> None:
        self._config = config
        self._client_factory = client_factory or self._new_sts_client
        self._now = now or (lambda: datetime.now(timezone.utc))
        self._lock = threading.Lock()
        self._credentials: dict[str, Any] | None = None

    @staticmethod
    def _new_sts_client(config: RoleConfig) -> Any:
        import boto3

        session_options: dict[str, str] = {}
        if config.profile is not None:
            session_options["profile_name"] = config.profile
        if config.region is not None:
            session_options["region_name"] = config.region
        session = boto3.Session(**session_options)
        return session.client("sts")

    def get(self) -> dict[str, str]:
        with self._lock:
            if self._credentials is None or self._expires_soon(self._credentials["Expiration"]):
                previous = self._credentials
                try:
                    self._credentials = self._assume_role()
                except Exception:
                    if previous is None or self._is_expired(previous["Expiration"]):
                        raise
                    LOG.exception("credential refresh failed; serving credentials until they expire")
                    self._credentials = previous
            credentials = self._credentials

        expiration = credentials["Expiration"]
        if isinstance(expiration, str):
            expiration_text = expiration
        else:
            expiration_text = expiration.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")
        return {
            "AccessKeyId": credentials["AccessKeyId"],
            "SecretAccessKey": credentials["SecretAccessKey"],
            "Token": credentials["SessionToken"],
            "Expiration": expiration_text,
        }

    def _expires_soon(self, expiration: datetime) -> bool:
        return expiration.astimezone(timezone.utc) <= self._now() + REFRESH_MARGIN

    def _is_expired(self, expiration: datetime) -> bool:
        return expiration.astimezone(timezone.utc) <= self._now()

    def _assume_role(self) -> dict[str, Any]:
        session_suffix = self._now().strftime("%Y%m%dT%H%M%SZ")
        response = self._client_factory(self._config).assume_role(
            RoleArn=self._config.role_arn,
            RoleSessionName=f"wisp-{session_suffix}",
            DurationSeconds=self._config.duration_seconds,
        )
        LOG.info("assumed role for AWS alias %s", self._config.alias)
        return response["Credentials"]


class CredentialServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(
        self,
        address: tuple[str, int],
        token: str,
        cache: CredentialCache,
        config: RoleConfig,
    ) -> None:
        super().__init__(address, CredentialHandler)
        self.authorization_token = token
        self.credential_cache = cache
        self.role_config = config


class CredentialHandler(BaseHTTPRequestHandler):
    server: CredentialServer

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        if self.path == "/health":
            self._write_json(200, {"status": "ok"})
            return
        if self.path not in ("/configuration", "/credentials"):
            self._write_json(404, {"error": "not found"})
            return

        supplied_token = self.headers.get("Authorization", "")
        if not hmac.compare_digest(supplied_token, self.server.authorization_token):
            self._write_json(401, {"error": "unauthorized"})
            return

        if self.path == "/configuration":
            config = self.server.role_config
            self._write_json(
                200,
                {
                    "alias": config.alias,
                    "region": config.region,
                    "eks_cluster": config.eks_cluster,
                },
            )
            return

        try:
            self._write_json(200, self.server.credential_cache.get())
        except Exception:
            LOG.exception("could not refresh assumed-role credentials")
            self._write_json(503, {"error": "credentials unavailable"})

    def _write_json(self, status: int, body: dict[str, Any]) -> None:
        encoded = json.dumps(body, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, message: str, *args: Any) -> None:
        LOG.info("%s - %s", self.client_address[0], message % args)


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    token = os.environ.get("WISP_AWS_AUTHORIZATION_TOKEN")
    if not token:
        raise SystemExit("WISP_AWS_AUTHORIZATION_TOKEN is required")

    initialize_sso_cache(
        os.environ.get("WISP_AWS_SSO_CACHE_SOURCE", "/run/aws/sso-cache")
    )

    config = load_role_config(
        os.environ.get("WISP_CONFIG", "/run/wisp/config.toml"),
        os.environ.get("WISP_AWS_ALIAS") or None,
    )
    cache = CredentialCache(config)
    cache.get()  # Fail startup early when source credentials or role access are unavailable.

    server = CredentialServer(("127.0.0.1", 9911), token, cache, config)
    LOG.info("serving credentials for AWS alias %s on 127.0.0.1:9911", config.alias)
    server.serve_forever()


if __name__ == "__main__":
    main()
