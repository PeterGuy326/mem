//go:build darwin

package modelcatalog

import "testing"

func TestParseVMStat(t *testing.T) {
	input := []byte(`Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10.
Pages active:                             99.
Pages inactive:                          20.
Pages speculative:                        3.
Pages purgeable:                          2.
`)
	got, err := parseVMStat(input)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(33 * 16384)
	if got != want {
		t.Fatalf("available memory = %d, want %d", got, want)
	}
}
