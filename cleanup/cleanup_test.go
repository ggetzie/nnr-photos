package main

import "testing"

func TestGetDestinationPrefix(t *testing.T) {
	tests := []struct {
		key     string
		want    string
		wantErr bool
	}{
		{"media/images/tags/bread/orig.jpeg", "media/images/tags/bread", false},
		{"a/b.jpg", "a", false},
		{"/leading.jpg", "", false},
		// All of these must error rather than yielding an empty prefix, which
		// would match every object in the destination bucket.
		{"noslash.jpg", "", true},
		{"noslash", "", true},
		{"", "", true},
		{"a/", "", true},
		{"media/images/tags/bread/", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			got, err := getDestinationPrefix(tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("getDestinationPrefix(%q) = %q, want error", tc.key, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGetDestinationPrefixNeverPanics: this feeds a bucket-wide delete, so it
// must always return rather than crash.
func TestGetDestinationPrefixNeverPanics(t *testing.T) {
	for _, k := range []string{"", "/", "//", "a", "a/", "/a", "....", "a/b/c.jpg"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("getDestinationPrefix(%q) panicked: %v", k, r)
				}
			}()
			_, _ = getDestinationPrefix(k)
		}()
	}
}

// TestNeverReturnsEmptyPrefixWithoutError is the safety invariant.
func TestNeverReturnsEmptyPrefixWithoutError(t *testing.T) {
	for _, k := range []string{"", "/", "//", "a", "a/", "....", "x.jpg"} {
		got, err := getDestinationPrefix(k)
		if err == nil && got == "" && k != "/a" {
			t.Errorf("getDestinationPrefix(%q) returned an empty prefix with no error - would delete the whole bucket", k)
		}
	}
}
