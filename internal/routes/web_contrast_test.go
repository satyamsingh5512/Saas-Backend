package routes

// Contrast regression guard for the dashboard's ink ramp.
//
// The stylesheet is not covered by `go vet`, and a colour-contrast failure is
// invisible in review: --fg-subtle previously pointed at --n-400, which renders
// at 2.56:1 on white. It shipped, and it carried real text — sidebar section
// labels, command-palette group headings and key hints, notification
// timestamps, the "optional" field marker, and every input placeholder.
//
// Reviewing hex values by eye does not catch that, so this test computes the
// actual WCAG 2.1 contrast ratio from the committed CSS and fails the build
// below the AA threshold. It parses the file rather than restating the colours,
// so it cannot drift out of sync with the stylesheet it is guarding.
//
// This mirrors how tenant isolation is handled elsewhere in the project: the
// guarantee is enforced by a mechanism, not entrusted to whoever reviews next.

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// wcagAABodyText is the minimum contrast ratio WCAG 2.1 requires for normal
// body text (success criterion 1.4.3, Level AA). Large text and non-text UI
// need only 3.0, but every token checked here is used for small text
// somewhere, so all of them are held to the stricter bar.
const wcagAABodyText = 1.0e-9 + 4.5

// relativeLuminance implements the WCAG 2.1 definition for an sRGB colour.
func relativeLuminance(hex string) (float64, error) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return 0, fmt.Errorf("expected a 6-digit hex colour, got %q", hex)
	}

	channel := func(offset int) (float64, error) {
		v, err := strconv.ParseUint(hex[offset:offset+2], 16, 8)
		if err != nil {
			return 0, err
		}
		c := float64(v) / 255.0
		// The 0.03928 threshold and 2.4 exponent are from the WCAG formula.
		if c <= 0.03928 {
			return c / 12.92, nil
		}
		return math.Pow((c+0.055)/1.055, 2.4), nil
	}

	r, err := channel(0)
	if err != nil {
		return 0, err
	}
	g, err := channel(2)
	if err != nil {
		return 0, err
	}
	b, err := channel(4)
	if err != nil {
		return 0, err
	}

	return 0.2126*r + 0.7152*g + 0.0722*b, nil
}

// contrastRatio returns the WCAG contrast ratio between two hex colours.
// The result is symmetric and always >= 1.0.
func contrastRatio(fg, bg string) (float64, error) {
	lf, err := relativeLuminance(fg)
	if err != nil {
		return 0, err
	}
	lb, err := relativeLuminance(bg)
	if err != nil {
		return 0, err
	}
	hi, lo := math.Max(lf, lb), math.Min(lf, lb)
	return (hi + 0.05) / (lo + 0.05), nil
}

var (
	// Matches `--n-400: #94a3b8;` and captures the step name and value.
	rampDecl = regexp.MustCompile(`--(n-\d+):\s*(#[0-9a-fA-F]{6})`)
	// Matches `--fg-subtle: var(--n-500);` and captures the ramp step it aliases.
	inkAlias = regexp.MustCompile(`--(fg|fg-muted|fg-subtle):\s*var\(--(n-\d+)\)`)
)

// scheme holds one resolved colour scheme's ramp.
type scheme struct {
	name string
	ramp map[string]string
}

// loadStylesheet returns the committed CSS from the same embedded filesystem
// the server serves it from, so the test cannot pass against a stale copy on
// disk while the binary ships something else.
func loadStylesheet(t *testing.T) string {
	t.Helper()
	raw, err := webFiles.ReadFile("web/assets/app.css")
	if err != nil {
		t.Fatalf("read embedded stylesheet: %v", err)
	}
	return string(raw)
}

// parseSchemes extracts the light ramp and the explicit dark override.
//
// The stylesheet declares the ink tokens exactly once and re-points the --n-*
// ramp per scheme, so resolving an alias against each ramp is enough to cover
// both themes. The dark ramp is read from the `:root.theme-dark` block; the
// file's own comment requires that block to stay byte-identical to the
// prefers-color-scheme one, and TestDarkSchemeBlocksAgreeOnRamp below enforces it.
func parseSchemes(t *testing.T, css string) []scheme {
	t.Helper()

	darkClassAt := strings.Index(css, ":root.theme-dark")
	mediaDarkAt := strings.Index(css, "@media (prefers-color-scheme: dark)")
	if darkClassAt < 0 || mediaDarkAt < 0 {
		t.Fatal("could not locate the dark scheme blocks in app.css")
	}

	collect := func(section string) map[string]string {
		out := map[string]string{}
		for _, m := range rampDecl.FindAllStringSubmatch(section, -1) {
			if _, seen := out[m[1]]; !seen {
				out[m[1]] = m[2]
			}
		}
		return out
	}

	// Light lives in the opening :root block, which ends where the dark media
	// query begins.
	light := collect(css[:mediaDarkAt])
	// Dark starts at the :root.theme-dark block; the ramp is declared at its top.
	dark := collect(css[darkClassAt:])

	if len(light) == 0 || len(dark) == 0 {
		t.Fatalf("failed to parse ramps (light=%d, dark=%d steps)", len(light), len(dark))
	}
	return []scheme{{"light", light}, {"dark", dark}}
}

// TestInkRampMeetsWCAGAA asserts every ink token clears 4.5:1 against the two
// backgrounds it is actually painted on: --surface (--n-0) and the recessed
// --canvas / --sunken (--n-50).
func TestInkRampMeetsWCAGAA(t *testing.T) {
	css := loadStylesheet(t)

	aliases := inkAlias.FindAllStringSubmatch(css, -1)
	if len(aliases) == 0 {
		t.Fatal("found no --fg/--fg-muted/--fg-subtle declarations to check")
	}

	for _, sch := range parseSchemes(t, css) {
		backgrounds := map[string]string{
			"surface (--n-0)": sch.ramp["n-0"],
			"canvas (--n-50)": sch.ramp["n-50"],
		}

		for _, alias := range aliases {
			token, step := alias[1], alias[2]
			fg, ok := sch.ramp[step]
			if !ok {
				t.Fatalf("%s scheme: --%s aliases --%s, which the ramp does not define", sch.name, token, step)
			}

			for bgName, bg := range backgrounds {
				if bg == "" {
					t.Fatalf("%s scheme: background %s is undefined", sch.name, bgName)
				}

				ratio, err := contrastRatio(fg, bg)
				if err != nil {
					t.Fatalf("%s scheme: --%s on %s: %v", sch.name, token, bgName, err)
				}

				if ratio < wcagAABodyText {
					t.Errorf(
						"%s scheme: --%s (%s → %s) on %s (%s) is %.2f:1, below the WCAG AA %.1f:1 floor for body text.\n"+
							"This token paints real text (nav section labels, palette headings, timestamps, placeholders). "+
							"Point it at a darker ramp step rather than lowering this threshold.",
						sch.name, token, step, fg, bgName, bg, ratio, 4.5,
					)
					continue
				}
				t.Logf("%-5s --%-10s on %-16s %.2f:1 ok", sch.name, token, bgName, ratio)
			}
		}
	}
}

// TestDarkSchemeBlocksAgreeOnRamp guards the one duplication the stylesheet
// knowingly accepts. Dark mode is declared twice — once under
// prefers-color-scheme for "system", once as :root.theme-dark for an explicit
// override — because CSS cannot share a declaration block between a media
// query and a class selector. The file's comment says "keep the two blocks in
// sync"; a comment is not a mechanism, so this asserts it. Without this, the
// contrast test above could pass on the class block while the media block
// (what most users actually get) had drifted.
func TestDarkSchemeBlocksAgreeOnRamp(t *testing.T) {
	css := loadStylesheet(t)

	mediaAt := strings.Index(css, "@media (prefers-color-scheme: dark)")
	classAt := strings.Index(css, ":root.theme-dark")
	if mediaAt < 0 || classAt < 0 || classAt <= mediaAt {
		t.Fatal("unexpected ordering of the dark scheme blocks in app.css")
	}

	parseFirst := func(section string) map[string]string {
		out := map[string]string{}
		for _, m := range rampDecl.FindAllStringSubmatch(section, -1) {
			if _, seen := out[m[1]]; !seen {
				out[m[1]] = m[2]
			}
		}
		return out
	}

	// The media block runs from its start to where the class block begins.
	media := parseFirst(css[mediaAt:classAt])
	class := parseFirst(css[classAt:])

	if len(media) == 0 {
		t.Fatal("parsed no ramp steps from the prefers-color-scheme dark block")
	}

	for step, mediaVal := range media {
		classVal, ok := class[step]
		if !ok {
			t.Errorf("--%s is defined in the prefers-color-scheme dark block but missing from :root.theme-dark", step)
			continue
		}
		if !strings.EqualFold(mediaVal, classVal) {
			t.Errorf(
				"dark scheme ramp drift on --%s: prefers-color-scheme says %s, :root.theme-dark says %s.\n"+
					"These two blocks must stay identical or system-dark and explicit-dark render differently.",
				step, mediaVal, classVal,
			)
		}
	}
}
