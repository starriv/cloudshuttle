package domain

import "testing"

func TestValidStoragePath(t *testing.T) {
	for path, want := range map[string]bool{
		"":                             true,
		"photos/2026":                  true,
		"logs/2026-11-09T10:00:00.log": true, // object keys may hold ':'
		`backup\old`:                   true,
		"photos//a":                    false,
		"photos/./a":                   false,
		"../a":                         false,
		"a\x00b":                       false,
		"a\nb":                         false,
	} {
		if got := ValidStoragePath(path); got != want {
			t.Errorf("ValidStoragePath(%q) = %t, want %t", path, got, want)
		}
	}
}
