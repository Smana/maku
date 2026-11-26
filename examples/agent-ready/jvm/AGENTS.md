# Agent notes

- Resolve once: `mvn -B dependency:go-offline`. Then unit tests offline: `mvn -B -o test`.
- Integration tests (`*IT.java`) use Testcontainers and run in CI only.
