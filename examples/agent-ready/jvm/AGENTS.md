# Agent notes

- Use the Maven wrapper, never a system `mvn`: `./mvnw -B dependency:go-offline` once, then unit tests
  offline with `./mvnw -B -o test`.
- Integration tests (`*IT.java`) use Testcontainers and run in CI only.
