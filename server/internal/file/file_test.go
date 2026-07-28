package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/pathx"
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

// TestPutTargetPathNormalization documents the contract for the
// `targetPath` argument plumbed through `Service.Put`: it must normalize
// before being stored on `files.path`, and "" / "/" both map to root.
//
// We don't run a live PG here; this just locks the pathx contract that the
// upload handler relies on so future refactors don't silently regress to
// storing "Photos/" instead of "/Photos".
func TestPutTargetPathNormalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"", "/", false},
		{"/", "/", false},
		{"/Photos/2012", "/Photos/2012", false},
		{"/Photos/2012/", "/Photos/2012", false},
		{"Photos", "", true},
	}
	for _, tc := range cases {
		got, err := pathx.Normalize(tc.in)
		if tc.err {
			if err == nil {
				t.Fatalf("expected error for %q", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("Normalize(%q) = (%q, %v); want %q", tc.in, got, err, tc.want)
		}
	}
}

// TestFileRenameValidation makes sure new filenames are validated before they
// can be persisted (no slashes, no "." / "..", no NUL).
func TestFileRenameValidation(t *testing.T) {
	t.Parallel()
	bad := []string{"", ".", "..", "with/slash", "with\x00nul"}
	for _, n := range bad {
		if err := pathx.ValidateName(n); err == nil {
			t.Fatalf("rename should reject %q", n)
		}
	}
	if err := pathx.ValidateName("perfectly fine.jpg"); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
}

func TestPutCommitFailurePreservesUploadedObject(t *testing.T) {
	t.Parallel()
	store := &recordingObjectStore{}
	object := &uploadedObject{
		store:         store,
		key:           "users/test/object",
		deleteOnError: true,
	}
	commitErr := errors.New("commit acknowledgement lost")
	if err := commitFilePut(
		context.Background(),
		failingCommitter{err: commitErr},
		object,
	); !errors.Is(err, commitErr) {
		t.Fatalf("commit error = %v, want %v", err, commitErr)
	}

	// Mirrors Put's deferred cleanup after commitFilePut returns an error.
	object.cleanup()
	if store.deleteCalls != 0 {
		t.Fatalf("Delete calls = %d, want 0 after ambiguous commit", store.deleteCalls)
	}
}

type failingCommitter struct {
	err error
}

func (committer failingCommitter) Commit(context.Context) error {
	return committer.err
}

type recordingObjectStore struct {
	deleteCalls int
}

func (*recordingObjectStore) Put(context.Context, string, io.Reader, int64, string) error {
	return nil
}

func (*recordingObjectStore) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (store *recordingObjectStore) Delete(context.Context, string) error {
	store.deleteCalls++
	return nil
}
