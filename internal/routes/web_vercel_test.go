package routes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satym-in/tenant-saas-backend/internal/middleware"
)

// vercelConfig models only the parts of vercel.json this test asserts on.
type vercelConfig struct {
	BuildCommand    string `json:"buildCommand"`
	OutputDirectory string `json:"outputDirectory"`
	Rewrites        []struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
	} `json:"rewrites"`
	Headers []struct {
		Source  string `json:"source"`
		Headers []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"headers"`
	} `json:"headers"`
}

func loadVercelConfig(t *testing.T) vercelConfig {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "vercel.json"))
	if err != nil {
		t.Fatalf("reading vercel.json: %v", err)
	}
	var cfg vercelConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("vercel.json is not valid JSON: %v", err)
	}
	return cfg
}

// TestVercelCSPMatchesServer keeps the frontend host's Content-Security-Policy
// identical to the one this server sends.
//
// The policy is what makes the no-innerHTML rule a real defence rather than a
// convention, and on the split deployment Vercel serves the documents, so its
// copy is the one a browser actually enforces. Nothing else would catch a drift:
// the Go tests would still pass while the deployed dashboard ran under a weaker
// policy, or broke on a directive the server never had.
func TestVercelCSPMatchesServer(t *testing.T) {
	cfg := loadVercelConfig(t)

	var got string
	for _, block := range cfg.Headers {
		for _, header := range block.Headers {
			if strings.EqualFold(header.Key, "Content-Security-Policy") {
				got = header.Value
			}
		}
	}

	if got == "" {
		t.Fatal("vercel.json declares no Content-Security-Policy")
	}
	if got != middleware.ContentSecurityPolicy {
		t.Errorf("vercel.json CSP has drifted from middleware.ContentSecurityPolicy\n got: %s\nwant: %s",
			got, middleware.ContentSecurityPolicy)
	}
}

// TestVercelProxiesAPIToSameOrigin pins the decision that keeps the frontend
// same-origin with the API: /api and /health are rewritten to the backend rather
// than called cross-origin from the browser.
//
// If these rewrites are removed, every API call becomes cross-origin, which
// needs a CORS grant on a credential-bearing API and a connect-src exception in
// the CSP above. Both are real weakenings, so the rewrite is load-bearing and
// not merely convenient.
func TestVercelProxiesAPIToSameOrigin(t *testing.T) {
	cfg := loadVercelConfig(t)

	destinationFor := func(source string) string {
		for _, rewrite := range cfg.Rewrites {
			if rewrite.Source == source {
				return rewrite.Destination
			}
		}
		return ""
	}

	for _, source := range []string{"/api/:path*", "/health"} {
		destination := destinationFor(source)
		if destination == "" {
			t.Errorf("vercel.json has no rewrite for %q, so it would be called cross-origin", source)
			continue
		}
		if !strings.HasPrefix(destination, "https://") {
			t.Errorf("rewrite for %q points at %q, which is not an absolute https backend URL", source, destination)
		}
	}

	// The catch-all must not shadow the API proxy. Vercel takes the first
	// matching rewrite, so the API entries have to come first.
	apiIndex, catchAllIndex := -1, -1
	for i, rewrite := range cfg.Rewrites {
		if rewrite.Source == "/api/:path*" {
			apiIndex = i
		}
		if rewrite.Source == "/:path*" && catchAllIndex == -1 {
			catchAllIndex = i
		}
	}
	if apiIndex != -1 && catchAllIndex != -1 && catchAllIndex < apiIndex {
		t.Error("the SPA catch-all rewrite precedes the /api proxy, so API calls would be served the dashboard document")
	}
}

// TestVercelBuildOutputMatchesEmbeddedWeb checks that the build script Vercel
// runs produces the document names vercel.json rewrites to, against the same
// directory the Go binary embeds.
//
// The swap is the fragile part: Vercel resolves "/" from the filesystem before
// any rewrite, so the landing page has to be named index.html there while the
// server keeps serving it from landing.html. This asserts the two halves agree
// rather than leaving it to a deploy to find out.
func TestVercelBuildOutputMatchesEmbeddedWeb(t *testing.T) {
	cfg := loadVercelConfig(t)

	if !strings.Contains(cfg.BuildCommand, "build_vercel_static.sh") {
		t.Errorf("buildCommand %q does not run the static build script", cfg.BuildCommand)
	}

	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build_vercel_static.sh"))
	if err != nil {
		t.Fatalf("reading build script: %v", err)
	}
	source := string(script)

	// The script must copy from the directory this package embeds, or the two
	// deployments can serve different UIs.
	if !strings.Contains(source, "internal/routes/web") {
		t.Error("build script does not copy from internal/routes/web, so Vercel could serve a different UI than the binary")
	}
	if !strings.Contains(source, `mv "$OUT/index.html" "$OUT/app.html"`) {
		t.Error("build script does not move the dashboard to app.html")
	}
	if !strings.Contains(source, `mv "$OUT/landing.html" "$OUT/index.html"`) {
		t.Error("build script does not promote the landing page to index.html")
	}

	// Every document the rewrites target must be a file the script produces.
	for _, rewrite := range cfg.Rewrites {
		if rewrite.Destination != "/app.html" {
			continue
		}
		if !strings.Contains(source, "app.html") {
			t.Errorf("rewrite %q targets /app.html, which the build script never creates", rewrite.Source)
		}
	}

	// Both source documents must still exist under the embedded tree, since the
	// script fails hard without them.
	for _, name := range []string{"web/index.html", "web/landing.html"} {
		if _, err := webFiles.ReadFile(name); err != nil {
			t.Errorf("embedded %s is missing, so the Vercel build would fail: %v", name, err)
		}
	}
}
