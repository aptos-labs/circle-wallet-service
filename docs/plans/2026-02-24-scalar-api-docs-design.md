# Scalar API Docs Frontend

## Goal

Serve an interactive API explorer at `GET /v1/docs` using Scalar, loaded from CDN, pointed at the existing `/v1/openapi.yaml` spec. Zero new dependencies.

## Changes

### `cmd/server/main.go`

Add a handler in `buildRouter` that writes an HTML page loading Scalar:

```go
mux.HandleFunc("GET /v1/docs", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html")
    w.Write([]byte(docsHTML))
})
```

Where `docsHTML` is a `const` containing a minimal HTML page that loads `@scalar/api-reference` from CDN with `data-url="/v1/openapi.yaml"`.

The endpoint is unauthenticated (placed alongside `/v1/health` and `/v1/openapi.yaml` in the mux, before middleware is applied).

### `internal/api/openapi/spec.go`

Add `/v1/docs` to the OpenAPI paths with `security: []` (no auth required), so the docs endpoint is self-documenting.

## Non-goals

- No embedded/bundled assets
- No new Go packages
- No build tooling
