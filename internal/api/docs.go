package api

import (
	_ "embed"
	"net/http"
)

// openAPISpec is the hand-authored OpenAPI 3.1 description of this service,
// embedded so /openapi.yaml is always in lock-step with the deployed binary.
//
//go:embed openapi.yaml
var openAPISpec []byte

// scalarJS is the Scalar API-reference standalone bundle, vendored and served
// same-origin (see assets/scalar.version.txt for the pinned version). Bundling
// it — rather than loading from a CDN — removes the third-party runtime
// dependency and CDN-compromise surface, and works under a strict CSP.
//
//go:embed assets/scalar.standalone.js
var scalarJS []byte

// docsHTML renders the spec with Scalar. Everything is same-origin: the bundle
// is served from /docs/scalar.js, Scalar fetches /openapi.yaml, and "try it"
// requests go back to this same host.
const docsHTML = `<!doctype html>
<html lang="en">
  <head>
    <title>Washington Fish API — Reference</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <link rel="icon" href="data:image/svg+xml,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 100 100%22><text y=%22.9em%22 font-size=%2290%22>🎣</text></svg>" />
  </head>
  <body>
    <script id="api-reference" data-url="/openapi.yaml"></script>
    <script src="/docs/scalar.js"></script>
  </body>
</html>
`

// rootText is the plain-text landing served at /.
const rootText = `Washington Fish API
Fishing-condition predictions for Washington freshwater lakes.

Docs:    /docs
Spec:    /openapi.yaml
Health:  /healthz   Ready: /readyz

Endpoints:
  GET /v1/lakes
  GET /v1/lakes/{id}
  GET /v1/lakes/{id}/species
  GET /v1/lakes/{id}/prediction
  GET /v1/rank
`

// handleRoot serves a plain-text landing at /.
func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(rootText))
}

// handleOpenAPISpec serves the embedded OpenAPI document.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(openAPISpec)
}

// handleDocs serves the Scalar-rendered API reference at /docs.
func (s *Server) handleDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(docsHTML))
}

// handleScalarJS serves the vendored Scalar bundle. It's immutable per deploy
// (version pinned), so it caches aggressively.
func (s *Server) handleScalarJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(scalarJS)
}
