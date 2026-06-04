#!/usr/bin/env python3
"""Non-interactive auth to the hosted panda proxy for CI.

Exchanges the GitHub Actions OIDC token for a Dex-issued ``panda-proxy`` token
(RFC 8693 token exchange) and writes it into the panda credential store, so the
panda server authenticates to the hosted proxy without an interactive login.

No long-lived secret is stored and nothing is written back: each CI run mints
its own short-lived token, so concurrent runs never race (and Dex persists
nothing, since the exchanged access token is a stateless JWT).

Requires (set by the workflow):
  ACTIONS_ID_TOKEN_REQUEST_URL / ACTIONS_ID_TOKEN_REQUEST_TOKEN  (GitHub-provided)
  PANDA_PROXY_OIDC_ISSUER        e.g. https://dex.primary.production.platform.ethpandaops.io
  PANDA_PROXY_OIDC_CONNECTOR_ID  the Dex connector that validates the GHA OIDC token
  PANDA_PROXY_CLIENT_ID          default: panda-proxy
  PANDA_PROXY_OIDC_RESOURCE      default: "" (oidc mode uses an empty resource)
  GHA_OIDC_AUDIENCE              default: the client id
"""

from __future__ import annotations

import hashlib
import json
import os
import sys
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone
from pathlib import Path

TOKEN_EXCHANGE_GRANT = "urn:ietf:params:oauth:grant-type:token-exchange"
SUBJECT_TOKEN_TYPE = "urn:ietf:params:oauth:token-type:id_token"

# Authentik's public host is Cloudflare-fronted and blocks the default urllib UA
# (error 1010); use a browser-like UA so the client_credentials mint succeeds.
USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)


def gha_oidc_token(audience: str) -> str:
    """Fetch a GitHub Actions OIDC ID token for the given audience."""
    base = os.environ["ACTIONS_ID_TOKEN_REQUEST_URL"]
    url = f"{base}&audience={urllib.parse.quote(audience)}"
    req = urllib.request.Request(
        url,
        headers={"Authorization": "Bearer " + os.environ["ACTIONS_ID_TOKEN_REQUEST_TOKEN"]},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)["value"]


def exchange_at_dex(
    issuer: str, client_id: str, connector_id: str, subject_token: str
) -> dict:
    """Exchange an external OIDC token for a Dex token audienced to ``client_id``."""
    body = urllib.parse.urlencode({
        "grant_type": TOKEN_EXCHANGE_GRANT,
        "client_id": client_id,
        "connector_id": connector_id,
        "scope": "openid",
        "subject_token": subject_token,
        "subject_token_type": SUBJECT_TOKEN_TYPE,
    }).encode()
    req = urllib.request.Request(
        issuer.rstrip("/") + "/token",
        data=body,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


def discover_token_endpoint(issuer: str) -> str:
    """Resolve the OIDC token endpoint for an issuer (Authentik's is app-global)."""
    url = issuer.rstrip("/") + "/.well-known/openid-configuration"
    req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)["token_endpoint"]


def mint_client_credentials(
    issuer: str, client_id: str, username: str, password: str, scope: str
) -> dict:
    """Mint a token for an Authentik service account via client_credentials.

    Authentik's machine-to-machine grant takes the service account ``username``
    plus its app-password as ``password`` (NOT client_secret). The resulting
    token carries the SA's group memberships (e.g. ``panda-ci``), which the proxy
    authorizes datasource access on.
    """
    body = urllib.parse.urlencode({
        "grant_type": "client_credentials",
        "client_id": client_id,
        "username": username,
        "password": password,
        "scope": scope,
    }).encode()
    req = urllib.request.Request(
        discover_token_endpoint(issuer),
        data=body,
        headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "User-Agent": USER_AGENT,
        },
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


def credential_path(issuer: str, client_id: str, resource: str) -> Path:
    """Locate the panda credential file (matches pkg/auth/store hashing)."""
    normalized = "\n".join([
        issuer.rstrip("/"),
        client_id,
        resource.rstrip("/"),
    ])
    digest = hashlib.sha256(normalized.encode()).digest()[:8].hex()
    return Path.home() / ".config" / "panda" / "credentials" / f"{digest}.json"


def write_credential(path: Path, access_token: str, expires_in: int) -> None:
    """Write a credential file the panda server reads (no refresh token)."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.parent.chmod(0o700)
    expires_at = datetime.now(timezone.utc) + timedelta(seconds=expires_in)
    path.write_text(json.dumps({
        "access_token": access_token,
        "token_type": "Bearer",
        "expires_in": expires_in,
        "expires_at": expires_at.isoformat().replace("+00:00", "Z"),
    }))
    path.chmod(0o600)


def main() -> int:
    issuer = os.environ["PANDA_PROXY_OIDC_ISSUER"]
    client_id = os.environ.get("PANDA_PROXY_CLIENT_ID", "panda-proxy")
    resource = os.environ.get("PANDA_PROXY_OIDC_RESOURCE", "")

    # Two mint methods, selected by which secret is present:
    #  - PANDA_CI_SVC_TOKEN set  -> Authentik service account (client_credentials).
    #    This is the CI default: the panda-ci-svc identity, scoped to external data.
    #  - otherwise               -> GitHub Actions OIDC exchanged at Dex.
    sa_token = os.environ.get("PANDA_CI_SVC_TOKEN")
    if sa_token:
        username = os.environ.get("PANDA_CI_SVC_USERNAME", "panda-ci-svc")
        scope = os.environ.get("PANDA_CI_SVC_SCOPE", "openid groups")
        token_response = mint_client_credentials(issuer, client_id, username, sa_token, scope)
    else:
        connector_id = os.environ["PANDA_PROXY_OIDC_CONNECTOR_ID"]
        audience = os.environ.get("GHA_OIDC_AUDIENCE", client_id)
        subject_token = gha_oidc_token(audience)
        token_response = exchange_at_dex(issuer, client_id, connector_id, subject_token)

    access_token = token_response["access_token"]
    expires_in = int(token_response.get("expires_in", 3600))

    path = credential_path(issuer, client_id, resource)
    write_credential(path, access_token, expires_in)
    print(f"Wrote proxy credential to {path} (expires_in={expires_in}s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
