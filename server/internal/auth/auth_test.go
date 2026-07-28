package auth

import (
	"reflect"
	"testing"
)

func TestNormalizeTokenPaths(t *testing.T) {
	got, err := normalizeTokenPaths([]string{"/Work//contracts/", "/Work/contracts"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/Work/contracts"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}

	got, err = normalizeTokenPaths([]string{"/Work", "/"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root paths = %#v, want %#v", got, want)
	}
}

func TestNormalizeTokenPathsRejectsEmptyEntry(t *testing.T) {
	if _, err := normalizeTokenPaths([]string{""}); err == nil {
		t.Fatal("empty path entry should be rejected")
	}
}
