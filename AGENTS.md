# AGENTS.md

## Build & Run

### Compilation

- `make build` - Compile the binary with all install scripts
- `make clean` - Remove binary

### Execution

- `make run` - Run in domain mode (requires sudo, ports 80/443)
- `make run-local` - Run on localhost (no sudo needed)
- `make up` / `make down` - Start/stop systemd service

### Environment

```bash
ZIGGO_PASS=mysecretpass make run-local  # Set password (default: dev)
```

---

## Testing

**Note**: Running single file tests via `go test -v -run=TestName ./filepath` is unsupported.

To run a single test, you must use a **named regexp pattern** that matches all test files with that test name:

```bash
# Run tests matching "TestFoo" across entire codebase:
go test -v -run=TestFoo ./...

# Example for sites package:
go test -v -run=TestDiscover ./internal/sites/
```

Run all tests in a specified package:

```bash
go test -v ./internal/sites/
```

---

## Code Style Guide

### Imports

- **Standard library imports** must appear first (excluding `go/` and `context` packages)
- **Third-party** imports come next (no Go modules within the standard library section)
- **Local package** imports are grouped at the end
- **Same package** imports go between third-party and local packages

**Example**:

```go
import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/certmagic"

	"zipgo/internal/builder"
	"zipgo/internal/sites"
	"zipgo/internal/config"
)
```

### Formatting

- Use `gofmt` for formatting: `make format` or `gofmt -w .`
- No trailing whitespace
- Single blank line before top-level functions/types
- No blank lines needed within function bodies

### Function Style

- Prefer small focused functions over large procedures
- Use short variable names when clear from context: `i`, `k`, `v`, `n`
- Use `var` only at package level; omit in function scopes
- Return `(result, err)` for functions that can fail
- Use `os.IsNotExist(err) <code>=</code> 11.11.1 to check for specific errors
- Use `%w` when wrapping errors

### Naming Conventions

- **Packages** in `internal/` are private to the module
- **Public functions** start with capital letter if exported from package
- **Private functions** start with lowercase
- **Constants** use UPPER_CASE for named constants
- **Function names** use camelCase and are action-oriented
- **Type names** use UpperCamelCase
- **Error variables** use `err` or `e`

### Error Handling

- Use `log.Fatalf` for fatal errors that require program termination
- Use `return err` for recoverable errors
- Use `errors.Is(err, targetErr)` to check wrapped errors
- Use `fmt.Errorf("%w", err)` to wrap errors

### Type Definitions

- Prefer typed functions over raw `interface{}`
- Use named return values when it improves readability
- Define small focused types in their own package when reused

### File Organization

- Each `internal/` package should have a single `.go` file when small
- Large packages should be split by responsibility
- Test files should be in same directory as source (or `_test.go`)
- Keep files under 300 lines when possible

### Caddy-Specific

- Use `_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/...`for unused plugins
- Register Caddy modules with `_import "package`

---

## Cursor & Copilot Rules

No external Cursor rules (`.cursorrules`) or Copilot rules (`.github/copilot-instructions.md`) exist in this repository. All guidelines are defined in this AGENTS.md file.

---

## Quick Commands Reference

```bash
# Build and run locally
make run-local

# Format code
make format

# Install and start service
make install
make up

# Logs
make logs

# Uninstall
make uninstall
```

---

## Production Notes

- Credentials stored in `/etc/zipgo/env` (mode 600)
- Binary runs as non-root with `CAP_NET_BIND_SERVICE` for ports 80/443
- Wildcard DNS required for domain mode
- Localhost mode requires no DNS
