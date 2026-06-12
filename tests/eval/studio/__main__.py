"""`python -m studio` — preflight, then serve."""

from studio.preflight import run_preflight

run_preflight()

from studio.server import main  # noqa: E402 - import after preflight so a broken env fails with guidance first

main()
