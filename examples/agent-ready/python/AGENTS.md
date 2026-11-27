# Agent notes

- Install: `uv sync --frozen`. Tests: `uv run pytest -m "not warehouse"`. Lint: `uv run ruff check .`.
- Tests that need the warehouse are marked `warehouse`; write new tests against the DuckDB fixtures in `tests/fixtures/`.
