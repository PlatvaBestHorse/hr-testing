package backup

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestAuthenticatedStream(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	for _, size := range []int{0, 1, chunkSize, chunkSize + 1, 3 * chunkSize} {
		plain := bytes.Repeat([]byte("x"), size)
		var sealed, opened bytes.Buffer
		if err := Seal(&sealed, bytes.NewReader(plain), key); err != nil {
			t.Fatal(err)
		}
		if err := Open(&opened, bytes.NewReader(sealed.Bytes()), key); err != nil || !bytes.Equal(plain, opened.Bytes()) {
			t.Fatalf("roundtrip size %d: %v", size, err)
		}
		for _, cut := range []int{0, 1, len(sealed.Bytes()) - 1, len(sealed.Bytes()) - 20} {
			if err := Open(io.Discard, bytes.NewReader(sealed.Bytes()[:cut]), key); err == nil {
				t.Fatalf("accepted truncated size %d cut %d", size, cut)
			}
		}
		for _, pos := range []int{len(magic), len(magic) + 16, len(sealed.Bytes()) - 1} {
			damaged := append([]byte{}, sealed.Bytes()...)
			damaged[pos] ^= 1
			if err := Open(io.Discard, bytes.NewReader(damaged), key); err == nil {
				t.Fatalf("accepted tampering size %d pos %d", size, pos)
			}
		}
		if err := Open(io.Discard, bytes.NewReader(append(sealed.Bytes(), 0)), key); err == nil {
			t.Fatal("accepted trailing bytes")
		}
		if err := Open(io.Discard, bytes.NewReader(sealed.Bytes()), bytes.Repeat([]byte{8}, 32)); err == nil {
			t.Fatal("accepted wrong key")
		}
	}
}

func TestRecordsCannotBeReordered(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	var sealed bytes.Buffer
	if err := Seal(&sealed, bytes.NewReader(bytes.Repeat([]byte{3}, 2*chunkSize)), key); err != nil {
		t.Fatal(err)
	}
	b := append([]byte{}, sealed.Bytes()...)
	start := len(magic) + 16
	recordSize := 4 + chunkSize + 16
	copy(b[start:start+recordSize], sealed.Bytes()[start+recordSize:start+2*recordSize])
	copy(b[start+recordSize:start+2*recordSize], sealed.Bytes()[start:start+recordSize])
	if err := Open(io.Discard, bytes.NewReader(b), key); err == nil {
		t.Fatal("accepted reordered records")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func TestIOFailures(t *testing.T) {
	key := bytes.Repeat([]byte{4}, 32)
	if err := Seal(io.Discard, brokenReader{}, key); err == nil {
		t.Fatal("ignored read error")
	}
	if err := Seal(brokenWriter{}, bytes.NewReader(nil), key); err == nil {
		t.Fatal("ignored write error")
	}
	if err := Seal(io.Discard, bytes.NewReader(nil), key[:16]); err == nil {
		t.Fatal("accepted short key")
	}
	var sealed bytes.Buffer
	if err := Seal(&sealed, bytes.NewReader([]byte("data")), key); err != nil {
		t.Fatal(err)
	}
	if err := Open(brokenWriter{}, bytes.NewReader(sealed.Bytes()), key); err == nil {
		t.Fatal("ignored restore writer failure")
	}
}
