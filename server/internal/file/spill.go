package file

import (
	"fmt"
	"io"
	"os"
)

// spillBuffer is a temp-file backed buffer for streaming uploads. Letting it
// always go to disk keeps memory bounded for very large files; we trade an
// extra disk write for simplicity (W1 only). Future versions should hash +
// upload via S3 multipart in a single pass.
type spillBuffer struct {
	f *os.File
}

func newSpillBuffer() (*spillBuffer, error) {
	f, err := os.CreateTemp("", "mem-upload-*.bin")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	return &spillBuffer{f: f}, nil
}

func (b *spillBuffer) Write(p []byte) (int, error) { return b.f.Write(p) }

func (b *spillBuffer) Read(p []byte) (int, error) { return b.f.Read(p) }

// Rewind positions the file back to the start so it can be re-read.
func (b *spillBuffer) Rewind() error {
	_, err := b.f.Seek(0, io.SeekStart)
	return err
}

// Close removes the temp file from disk.
func (b *spillBuffer) Close() error {
	name := b.f.Name()
	cerr := b.f.Close()
	rerr := os.Remove(name)
	if cerr != nil {
		return cerr
	}
	return rerr
}
