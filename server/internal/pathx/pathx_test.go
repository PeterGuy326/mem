package pathx

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
		err      bool
	}{
		{"", "/", false},
		{"/", "/", false},
		{"//", "/", false},
		{"/Photos", "/Photos", false},
		{"/Photos/", "/Photos", false},
		{"/Photos/2012", "/Photos/2012", false},
		{"/Photos//2012", "/Photos/2012", false},
		{"/Photos/2012/", "/Photos/2012", false},
		{"Photos", "", true},  // not absolute
		{"/./foo", "", true},  // reserved
		{"/../foo", "", true}, // reserved
		{"/a/\x00", "", true}, // NUL in segment
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := Normalize(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	t.Parallel()
	bad := []string{
		"",
		".",
		"..",
		"a/b",
		"a\x00b",
		"a\nb",
		"a\x1bb",
		"a\u009bb",
		"a\u2028b",
		"a\u202eb",
		string([]byte{'a', 0xff, 'b'}),
	}
	for _, s := range bad {
		if err := ValidateName(s); err == nil {
			t.Fatalf("expected error for %q", s)
		}
	}
	good := []string{"Photos", "2012", "中文", "with space"}
	for _, s := range good {
		if err := ValidateName(s); err != nil {
			t.Fatalf("unexpected error for %q: %v", s, err)
		}
	}
}

func TestParentBase(t *testing.T) {
	t.Parallel()
	if p := Parent("/Photos/2012/IMG_0001.jpg"); p != "/Photos/2012" {
		t.Fatalf("Parent mismatch: %q", p)
	}
	if p := Parent("/Photos"); p != "/" {
		t.Fatalf("Parent of top-level should be root, got %q", p)
	}
	if p := Parent("/"); p != "/" {
		t.Fatalf("Parent of root should be root, got %q", p)
	}
	if b := Base("/Photos/2012"); b != "2012" {
		t.Fatalf("Base mismatch: %q", b)
	}
	if b := Base("/"); b != "" {
		t.Fatalf("Base of root should be empty, got %q", b)
	}
}

func TestAncestors(t *testing.T) {
	t.Parallel()
	got := Ancestors("/a/b/c")
	want := []string{"/a", "/a/b", "/a/b/c"}
	if len(got) != len(want) {
		t.Fatalf("Ancestors len: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Ancestors[%d]: got %q want %q", i, got[i], want[i])
		}
	}
	if a := Ancestors("/"); len(a) != 0 {
		t.Fatalf("Ancestors of root should be empty, got %v", a)
	}
}

func TestIsDescendantOrSelf(t *testing.T) {
	t.Parallel()
	if !IsDescendantOrSelf("/a/b/c", "/a") {
		t.Fatal("/a/b/c should be descendant of /a")
	}
	if !IsDescendantOrSelf("/a", "/a") {
		t.Fatal("self check failed")
	}
	if IsDescendantOrSelf("/ab", "/a") {
		t.Fatal("/ab must NOT match /a (prefix-only collision)")
	}
	if !IsDescendantOrSelf("/anything", "/") {
		t.Fatal("root is ancestor of everything")
	}
}

func TestReplacePrefix(t *testing.T) {
	t.Parallel()
	if got := ReplacePrefix("/a/b/c", "/a", "/x"); got != "/x/b/c" {
		t.Fatalf("ReplacePrefix mismatch: %q", got)
	}
	if got := ReplacePrefix("/a", "/a", "/x"); got != "/x" {
		t.Fatalf("ReplacePrefix exact: %q", got)
	}
	if got := ReplacePrefix("/ab", "/a", "/x"); got != "/ab" {
		t.Fatalf("ReplacePrefix sibling collision: %q", got)
	}
}

func TestJoin(t *testing.T) {
	t.Parallel()
	got, err := Join("/Photos", "2012")
	if err != nil || got != "/Photos/2012" {
		t.Fatalf("Join Photos/2012: %q err=%v", got, err)
	}
	got, err = Join("/", "Photos")
	if err != nil || got != "/Photos" {
		t.Fatalf("Join root/Photos: %q err=%v", got, err)
	}
	if _, err := Join("/Photos", "bad/name"); err == nil {
		t.Fatal("expected validation error for slash in name")
	}
}
