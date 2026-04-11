# Package: Go net/http + gomponents + htmx + Tailwind(daisyUI), fully embedded

## Non-negotiable constraints
- Use only Go stdlib net/http for the server (http.ServeMux, http.Handler).
- HTML must be produced with gomponents (no html/template).
- Use htmx for client-side interactions (server returns HTML fragments).
- Tailwind + daisyUI for styling; compiled CSS must be embedded and served from the binary.
- Serve stock htmx from goth's embedded runtime and keep Colin-specific browser JS in an embedded app asset.
- Do not read templates/assets from disk at runtime.

## Embedding
- Use //go:embed for all assets served to the browser.
- Ensure `go test ./...` and `go build ./...` work without requiring a separate assets directory at runtime.

## Testing requirements (must be present)
- Unit tests: component rendering + pure logic.
- Integration tests: httptest server + HTTP requests.
- E2E tests: Playwright (headless) verifying at least:
  - homepage renders
  - CSS loads
  - one htmx interaction updates DOM without full reload

## Quality gates before finishing any task
- gofmt on changed Go files
- go test ./...
- `task test-e2e-ui` passes from `test/e2e/playwright`
