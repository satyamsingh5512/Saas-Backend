// Package swagger serves the API reference: the embedded OpenAPI 3.0
// document plus an interactive UI page.
//
// Zero-dependency choice, stated plainly: the usual swaggo toolchain
// (swag CLI + generated docs package + UI asset module) adds three moving
// parts and a codegen step to CI for what is ultimately a static YAML file.
// Here the YAML is the source of truth, reviewed like code, embedded into
// the binary, and rendered by a small page that loads Swagger UI from CDN
// with a same-origin fallback message when offline. If the team later wants
// annotation-generated docs, the /swagger/openapi.yaml route already gives
// the generator a stable mount point to take over.
package swagger

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed openapi.yaml
var openAPIYAML []byte

const uiPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Tenant SaaS API — Swagger UI</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
<style>body{margin:0}.topbar{display:none}#fallback{padding:2rem;font-family:system-ui,sans-serif;display:none}</style>
</head>
<body>
<div id="swagger-ui"></div>
<div id="fallback"><h1>API reference</h1>
<p>Swagger UI loads from CDN and appears to be unreachable (offline?). The raw
OpenAPI document is always available at <a href="/swagger/openapi.yaml">/swagger/openapi.yaml</a>.</p></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>
window.onload = function () {
  if (!window.SwaggerUIBundle) { document.getElementById('fallback').style.display = 'block'; return; }
  SwaggerUIBundle({ url: '/swagger/openapi.yaml', dom_id: '#swagger-ui', presets: [SwaggerUIBundle.presets.apis] });
};
</script>
</body>
</html>`

// Register mounts the docs before tenant resolution: the reference is
// identical for every tenant, so resolving one is pointless and a failure
// mode (a probe with no tenant hint must still get docs).
func Register(router *gin.Engine) {
	router.GET("/swagger", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(uiPage))
	})
	router.GET("/swagger/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", openAPIYAML)
	})
}
