# Agent notes

- Build: `go build ./...`. Unit tests: `go test -short ./...`. Lint: `golangci-lint run`.
- Integration tests need `DATABASE_URL`; they skip without it, and CI runs them.
- Handlers live in `internal/http/`, storage in `internal/store/`. A new endpoint gets a handler test.
