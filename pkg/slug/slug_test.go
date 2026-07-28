package slug

import "testing"

func TestMake(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"lowercases and hyphenates", "Platform Engineering", "platform-engineering"},
		{"collapses repeated separators", "Core   __  Team", "core-team"},
		{"trims leading and trailing separators", "  --Ops--  ", "ops"},
		{"drops punctuation", "R&D (2024)!", "r-d-2024"},
		{"keeps digits", "Squad 42", "squad-42"},
		{"strips non-ascii rather than transliterating", "Café", "caf"},
		{"returns empty for entirely non-ascii input", "日本語", ""},
		{"returns empty for separators only", "---", ""},
		{"passes through an already valid slug", "platform-eng", "platform-eng"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Make(tc.input); got != tc.want {
				t.Errorf("Make(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMakeTruncatesToMaxLength(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}

	got := Make(long)
	if len(got) > maxLength {
		t.Errorf("Make produced a %d character slug, exceeding the %d limit", len(got), maxLength)
	}
}

// A truncation landing on a hyphen must not leave a trailing separator, since the
// result is compared against Make output by Valid.
func TestMakeTruncationDoesNotLeaveTrailingHyphen(t *testing.T) {
	input := ""
	for i := 0; i < 31; i++ {
		input += "ab "
	}

	got := Make(input)
	if len(got) > 0 && got[len(got)-1] == '-' {
		t.Errorf("Make(%q) = %q, which ends in a hyphen", input, got)
	}
	if !Valid(got) {
		t.Errorf("Make output %q is not considered Valid", got)
	}
}

func TestValid(t *testing.T) {
	valid := []string{"ops", "platform-eng", "squad-42", "a"}
	for _, s := range valid {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",              // empty
		"Ops",           // uppercase
		"platform_eng",  // underscore
		"-ops",          // leading hyphen
		"ops-",          // trailing hyphen
		"platform--eng", // doubled hyphen
		"ops team",      // space
	}
	for _, s := range invalid {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}
