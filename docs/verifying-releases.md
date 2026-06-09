# Verifying panda releases

Every tagged release is signed and carries build provenance. None of the
signing material is a long-lived key: panda uses [Sigstore](https://www.sigstore.dev/)
**keyless** signing, where the signer identity is the GitHub Actions release
workflow itself (verified via OIDC and recorded in the public Rekor
transparency log).

## What is signed

| Artifact | Mechanism | How to verify |
| --- | --- | --- |
| `checksums.txt` (covers all binaries) | cosign keyless signature (`.sig` + `.pem`) | `cosign verify-blob` |
| Release archives + checksums | SLSA build provenance (GitHub attestation) | `gh attestation verify` |
| Docker images (`:<version>`, `:server-<version>`, `:proxy-<version>`, `:sandbox-<version>`) | cosign keyless signature | `cosign verify` |
| Each release archive | CycloneDX SBOM (`*.sbom.json`) | inspect with any SBOM tool |

## Verify the binaries

Download `checksums.txt`, `checksums.txt.sig`, `checksums.txt.pem`, and the
archive you want from the GitHub release, then:

```bash
# 1. Verify the signature on checksums.txt
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/ethpandaops/panda/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 2. Verify your archive matches the now-trusted checksum
sha256sum --check --ignore-missing checksums.txt
```

If step 1 prints `Verified OK`, the checksums file was produced by the panda
release workflow and not tampered with; step 2 ties your download to it.

## Verify build provenance

```bash
gh attestation verify panda_<version>_linux_amd64.tar.gz \
  --repo ethpandaops/panda
```

This proves the archive was built by the `ethpandaops/panda` release workflow
from a specific commit, rather than uploaded by hand.

## Verify a Docker image

```bash
cosign verify \
  --certificate-identity-regexp 'https://github.com/ethpandaops/panda/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ethpandaops/panda:<version>
```

Pin by digest (`ethpandaops/panda@sha256:...`) in production so a moved tag
cannot swap the image out from under you.
