// Package slug derives URL-safe identifiers from human-entered names.
package slug

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/satym-in/tenant-saas-backend/pkg/apperror"
)

// maxLength keeps generated slugs within the VARCHAR/CITEXT column widths used
// by teams and projects, and well inside a DNS label for subdomain-style URLs.
const maxLength = 63

var (
	nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)
	repeatedHyphen  = regexp.MustCompile(`-{2,}`)
)

// Make converts a display name into a lowercase, hyphen-separated slug.
//
// Accented and non-Latin characters are stripped rather than transliterated, so
// a name consisting entirely of them yields an empty string. Callers must treat
// an empty result as "ask the user for an explicit slug" instead of storing it;
// Valid exists for that check.
func Make(input string) string {
	lowered := strings.ToLower(strings.TrimSpace(input))

	// Fold combining marks and any other non-ASCII runes out of the string
	// before the regex pass, so "Café" becomes "cafe" rather than "caf-".
	var b strings.Builder
	b.Grow(len(lowered))
	for _, r := range lowered {
		switch {
		case r < unicode.MaxASCII:
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}

	out := nonAlphanumeric.ReplaceAllString(b.String(), "-")
	out = repeatedHyphen.ReplaceAllString(out, "-")
	out = strings.Trim(out, "-")

	if len(out) > maxLength {
		out = strings.Trim(out[:maxLength], "-")
	}
	return out
}

// Valid reports whether s is a well-formed slug: non-empty, lowercase
// alphanumeric with single interior hyphens, and within the length limit.
func Valid(s string) bool {
	if s == "" || len(s) > maxLength {
		return false
	}
	return Make(s) == s
}

// Resolve validates an explicitly supplied slug, or derives one from name
// when none was given. Shared by every module that names user-visible
// resources (teams, projects) so explicit-slug validation and the derive
// fallback cannot drift apart between modules.
func Resolve(explicit, name string) (string, error) {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		if !Valid(trimmed) {
			return "", apperror.New(apperror.CodeValidation,
				"slug must be lowercase alphanumeric with single hyphens")
		}
		return trimmed, nil
	}

	derived := Make(name)
	if derived == "" {
		return "", apperror.New(apperror.CodeValidation,
			"could not derive a slug from the name; supply one explicitly")
	}
	return derived, nil
}
