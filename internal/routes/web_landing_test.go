package routes

// Contract guards for the landing page.
//
// The page is three files that have to agree with each other: landing.html holds
// the ids and the markup, landing.js queries those ids and builds nodes, and
// landing.css styles them. None of that is reachable by `go vet`, and the
// failure modes are all silent — a renamed id leaves a section permanently
// inert, an innerHTML makes API data an injection vector, a px font-size
// quietly ignores the reader's browser text-size preference.
//
// Every assertion below is a rule stated in one of those files' header comments.
// A comment is not a mechanism, so these turn each one into a build failure,
// the same way TestInkRampMeetsWCAGAA does for contrast.

import (
	"regexp"
	"strings"
	"testing"
)

func landingFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := webFiles.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded %s: %v", name, err)
	}
	return string(raw)
}

// stripCSSComments removes /* ... */ so a rule about the code is not tripped by
// prose in a comment that discusses it. Several comments in these files quote
// the very patterns being banned.
var cssCommentBlock = regexp.MustCompile(`(?s)/\*.*?\*/`)

// TestLandingBuildsNoMarkupFromStrings asserts the no-innerHTML rule the
// dashboard already follows, for the same reason: the landing page renders the
// plan catalog from an API response, and an API response is not a place to start
// trusting markup. Text goes in through textContent, via el().
func TestLandingBuildsNoMarkupFromStrings(t *testing.T) {
	js := cssCommentBlock.ReplaceAllString(landingFile(t, "web/assets/landing.js"), "")
	// Line comments too: the file's own header explains the rule by naming it.
	js = regexp.MustCompile(`(?m)^\s*(//|\*).*$`).ReplaceAllString(js, "")

	for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(js, banned) {
			t.Errorf(
				"landing.js uses %s. Every node on this page is built through el(), which "+
					"assigns text via textContent; the plan cards render API data and a tenant "+
					"name must never be able to become markup.",
				banned,
			)
		}
	}
}

// TestLandingUsesNoInlineStyles asserts the CSP constraint rather than trusting
// it to be remembered. The server sends style-src 'self', which blocks the style
// attribute outright — so a style attribute would appear to work in a local file
// and fail invisibly behind the real server. Programmatic CSSOM writes
// (element.style.setProperty) are fine and are what the spotlight and stagger
// use; a parsed inline style is not.
func TestLandingUsesNoInlineStyles(t *testing.T) {
	html := landingFile(t, "web/landing.html")
	// Matches a real style attribute, not the word "style" inside prose.
	if regexp.MustCompile(`<[^>]+\sstyle\s*=`).MatchString(html) {
		t.Error(
			"landing.html carries a style attribute. style-src 'self' blocks it, so it " +
				"would silently do nothing behind the server. Use a class, or set a custom " +
				"property from landing.js through element.style.setProperty().",
		)
	}

	js := landingFile(t, "web/assets/landing.js")
	if regexp.MustCompile(`setAttribute\(\s*["']style["']`).MatchString(js) {
		t.Error("landing.js sets a style attribute via setAttribute, which the CSP blocks")
	}
}

// TestLandingScriptTargetsExist walks every `#id` landing.js looks up and
// asserts landing.html defines it.
//
// This is the failure this page is most likely to have: the script wires up the
// live health pill, the plan catalog, the lifecycle demo, the code tabs and the
// sticky nav by id, and each lookup already fails soft — a missing element makes
// the corresponding section quietly inert rather than throwing. That is correct
// behaviour at runtime and a terrible property in review, so the coupling is
// asserted here instead.
func TestLandingScriptTargetsExist(t *testing.T) {
	js := landingFile(t, "web/assets/landing.js")
	html := landingFile(t, "web/landing.html")

	selector := regexp.MustCompile(`\$\(\s*"#([A-Za-z0-9_-]+)"\s*\)`)
	matches := selector.FindAllStringSubmatch(js, -1)
	if len(matches) == 0 {
		t.Fatal("found no #id lookups in landing.js; has the selector helper been renamed?")
	}

	seen := map[string]bool{}
	for _, m := range matches {
		id := m[1]
		if seen[id] {
			continue
		}
		seen[id] = true

		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf(
				"landing.js looks up #%s, which landing.html does not define. "+
					"The lookup fails soft, so the section it drives would be silently inert.",
				id,
			)
		}
	}
}

// TestLandingStylesheetStaysOnTheDesignSystem asserts the two rules that keep
// landing.css from becoming a second design system.
func TestLandingStylesheetStaysOnTheDesignSystem(t *testing.T) {
	raw := landingFile(t, "web/assets/landing.css")
	css := cssCommentBlock.ReplaceAllString(raw, "")

	// 1. No scheme awareness. app.css re-points the whole neutral ramp per
	//    scheme, so a rule written against a token is already correct in both.
	//    A prefers-color-scheme block here would be a second place dark mode is
	//    defined, and the one that drifts.
	if strings.Contains(css, "prefers-color-scheme") {
		t.Error(
			"landing.css contains a prefers-color-scheme block. Dark mode is declared " +
				"once, in app.css, by re-pointing the ramp; style against the tokens instead.",
		)
	}

	// 2. No pixel font sizes. The type scale is rem so that a reader who raised
	//    their browser's default text size gets the increase; a px font-size
	//    opts that reader out. Control heights and drawing coordinates stay in
	//    px deliberately, which is why this checks font-size specifically.
	pxType := regexp.MustCompile(`font-size:\s*[0-9.]+px`)
	if loc := pxType.FindString(css); loc != "" {
		t.Errorf(
			"landing.css declares %q. Type is rem — either a --fs-* token or a clamp() in "+
				"rem — so it scales with the reader's browser font-size preference.",
			loc,
		)
	}
}
