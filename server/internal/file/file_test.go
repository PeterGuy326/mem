package file

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/google/uuid"
)

// TestSHA256Streaming verifies that the spillBuffer + TeeReader pattern used
// by Service.Put produces the exact SHA-256 of the input stream.
func TestSHA256Streaming(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("hello, mem!")},
		{"binary", bytes.Repeat([]byte{0x00, 0xFF, 0x42}, 1024)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			expectedSum := sha256.Sum256(tc.data)
			expected := hex.EncodeToString(expectedSum[:])

			buf, err := newSpillBuffer()
			if err != nil {
				t.Fatalf("newSpillBuffer: %v", err)
			}
			defer buf.Close()

			h := sha256.New()
			_, err = io.Copy(buf, io.TeeReader(bytes.NewReader(tc.data), h))
			if err != nil {
				t.Fatalf("copy: %v", err)
			}
			got := hex.EncodeToString(h.Sum(nil))
			if got != expected {
				t.Fatalf("sha mismatch: want=%s got=%s", expected, got)
			}

			// Spill should be readable from start after Rewind.
			if err := buf.Rewind(); err != nil {
				t.Fatalf("rewind: %v", err)
			}
			read, err := io.ReadAll(buf)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(read, tc.data) {
				t.Fatalf("payload mismatch after rewind")
			}
		})
	}
}

// TestStorageKey ensures the layout is stable: users/<uid>/<fid>/<basename>.
func TestStorageKey(t *testing.T) {
	t.Parallel()
	uid := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	fid := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	got := storageKey(uid, fid, "../etc/passwd")
	want := "users/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/passwd"
	if got != want {
		t.Fatalf("storageKey: want=%s got=%s", want, got)
	}

	// Falls back to file id when name is empty / weird.
	got2 := storageKey(uid, fid, "")
	want2 := "users/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/22222222-2222-2222-2222-222222222222"
	if got2 != want2 {
		t.Fatalf("storageKey empty: want=%s got=%s", want2, got2)
	}
}

// TestDedupContract documents the contract: identical bytes -> identical hash.
// A real integration test against PG would be in W2+.
func TestDedupContract(t *testing.T) {
	t.Parallel()
	a := []byte("same content, two uploads")
	b := []byte("same content, two uploads")
	c := []byte("different content")

	hashA := sha256.Sum256(a)
	hashB := sha256.Sum256(b)
	hashC := sha256.Sum256(c)
	if hashA != hashB {
		t.Fatal("identical bytes must hash equal — 秒传 invariant broken")
	}
	if hashA == hashC {
		t.Fatal("different bytes must hash differently")
	}
}
