package bot

import (
	"io"
	"testing"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestReadLimitedAcceptsLimit(t *testing.T) {
	got, err := readLimitedBytes(io.LimitReader(zeroReader{}, 4), 4)
	if err != nil {
		t.Fatalf("readLimited() error = %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("readLimited() returned %d bytes, want 4", len(got))
	}
}

func TestReadLimitedRejectsOversizedInput(t *testing.T) {
	if _, err := readLimitedBytes(io.LimitReader(zeroReader{}, 5), 4); err == nil {
		t.Fatal("readLimited() accepted oversized input")
	}
}
