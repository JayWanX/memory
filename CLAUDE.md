# Memory — Project Guidelines

## Development

- **Build**: `go build -o memory .`
- **Install**: `make install && memory setup`
- **Test**: `make test`
- **Integration**: `make test-integration` for CLI E2E and `memory agency` boundaries
- **Dependencies**: `modernc.org/sqlite`, `spf13/cobra`, `google/uuid`
- **Optional**: Ollama with `nomic-embed-text` for embedding support
