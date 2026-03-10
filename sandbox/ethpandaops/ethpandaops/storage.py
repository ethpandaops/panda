"""Storage helpers for output files via the local server.

This module provides functions to upload files and get public URLs for sharing.
All requests go through the local server API - credentials are never exposed to
sandbox containers.

Example:
    from ethpandaops import storage

    # Upload a file
    url = storage.upload("/workspace/chart.png")
    print(f"Chart available at: {url}")

    # Upload with custom name
    url = storage.upload("/workspace/data.csv", remote_name="results.csv")
"""

from pathlib import Path

import httpx

from ethpandaops import _runtime


def _get_client() -> httpx.Client:
    """Get an HTTP client configured for the local server API."""
    _runtime._check_api_config()

    return httpx.Client(
        base_url=_runtime._API_URL,
        headers={"Authorization": f"Bearer {_runtime._API_TOKEN}"},
        timeout=httpx.Timeout(connect=5.0, read=300.0, write=300.0, pool=5.0),
    )


def upload(local_path: str, remote_name: str | None = None) -> str:
    """Upload a file to S3 storage.

    Args:
        local_path: Path to the local file to upload.
        remote_name: Name for the file in S3. If None, uses the local filename.

    Returns:
        Public URL for the uploaded file.

    Raises:
        FileNotFoundError: If the local file doesn't exist.
        ValueError: If proxy is not configured.

    Example:
        >>> url = upload("/workspace/chart.png")
        >>> url = upload("/workspace/data.csv", remote_name="analysis_results.csv")
    """
    path = Path(local_path)

    if not path.exists():
        raise FileNotFoundError(f"File not found: {local_path}")

    if remote_name is None:
        remote_name = path.name

    content_type = _get_content_type(path.suffix)

    with _get_client() as client:
        with open(path, "rb") as f:
            response = client.post(
                "/api/v1/runtime/storage/upload",
                content=f.read(),
                params={"name": remote_name},
                headers={"Content-Type": content_type},
            )
            response.raise_for_status()
            payload = response.json()

    return payload.get("url", "")


def _get_content_type(suffix: str) -> str:
    """Get MIME type for a file suffix.

    Args:
        suffix: File suffix including the dot (e.g., ".png").

    Returns:
        MIME type string.
    """
    content_types = {
        ".png": "image/png",
        ".jpg": "image/jpeg",
        ".jpeg": "image/jpeg",
        ".gif": "image/gif",
        ".svg": "image/svg+xml",
        ".pdf": "application/pdf",
        ".csv": "text/csv",
        ".json": "application/json",
        ".html": "text/html",
        ".txt": "text/plain",
        ".parquet": "application/octet-stream",
    }

    return content_types.get(suffix.lower(), "application/octet-stream")


def list_files(prefix: str = "") -> list[dict]:
    """List files in the S3 bucket.

    Args:
        prefix: Optional prefix to filter files.

    Returns:
        List of file info dictionaries with 'key', 'size', 'last_modified'.
    """
    params: dict[str, str] = {}
    if prefix:
        params["prefix"] = prefix

    with _get_client() as client:
        response = client.get("/api/v1/runtime/storage/files", params=params)
        response.raise_for_status()
        payload = response.json()

    files = payload.get("files", [])
    return files if isinstance(files, list) else []


def get_url(key: str) -> str:
    """Get the public URL for a file.

    Args:
        key: S3 object key.

    Returns:
        Public URL for the file.
    """
    with _get_client() as client:
        response = client.get("/api/v1/runtime/storage/url", params={"key": key})
        response.raise_for_status()
        payload = response.json()

    return payload.get("url", "")
