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

# Larger write timeout than the runtime default to accommodate large file uploads.
_UPLOAD_WRITE_TIMEOUT = 300.0


class UploadResult(str):
    """The public URL of an uploaded file.

    Behaves as the URL string for backwards compatibility (``url = upload(...)``
    still works), and additionally exposes the storage ``key`` and the file's
    ``host_path`` — its location on the panda server's filesystem. ``host_path``
    is only directly openable when the server runs on the same host as you (e.g.
    the local CLI); it is server-relative when storage lives in a container volume.
    """

    key: str
    host_path: str

    def __new__(cls, url: str, key: str = "", host_path: str = "") -> "UploadResult":
        obj = super().__new__(cls, url)
        obj.key = key
        obj.host_path = host_path
        return obj


def _get_client() -> httpx.Client:
    """Get an HTTP client configured for the local server API."""
    return _runtime._get_client(write_timeout=_UPLOAD_WRITE_TIMEOUT)


def upload(local_path: str, remote_name: str | None = None) -> UploadResult:
    """Upload a file to storage.

    Args:
        local_path: Path to the local file to upload.
        remote_name: Name for the stored file. If None, uses the local filename.

    Returns:
        An UploadResult — the public URL, which also carries ``.key`` and
        ``.host_path`` (the file's path on the panda server host).

    Raises:
        FileNotFoundError: If the local file doesn't exist.
        ValueError: If proxy is not configured.

    Example:
        >>> result = upload("/workspace/chart.png")
        >>> str(result)         # public URL
        >>> result.host_path    # path on the panda server host
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
                params={"name": remote_name, **_runtime._session_params()},
                headers={"Content-Type": content_type},
            )
            response.raise_for_status()
            payload = response.json()

    return UploadResult(
        payload.get("url", ""),
        key=payload.get("key", ""),
        host_path=payload.get("path", ""),
    )


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
    """List files for the current execution.

    Args:
        prefix: Optional execution-relative prefix to filter files.

    Returns:
        List of file info dictionaries with 'key', 'size', 'last_modified'.
    """
    params: dict[str, str] = dict(_runtime._session_params())
    if prefix:
        params["prefix"] = prefix

    with _get_client() as client:
        response = client.get("/api/v1/runtime/storage/files", params=params)
        response.raise_for_status()
        payload = response.json()

    files = payload.get("files", [])
    return files if isinstance(files, list) else []


def get_url(key: str) -> str:
    """Get the public URL for a file from the current execution.

    Args:
        key: Execution-relative object key.

    Returns:
        Public URL for the file.
    """
    with _get_client() as client:
        response = client.get(
            "/api/v1/runtime/storage/url",
            params={"key": key, **_runtime._session_params()},
        )
        response.raise_for_status()
        payload = response.json()

    return payload.get("url", "")
