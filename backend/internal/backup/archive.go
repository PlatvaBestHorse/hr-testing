// Package backup implements the versioned authenticated streaming backup format.
// Every record authenticates its sequence and archive header; an authenticated
// empty final record makes truncation, reordering and appending detectable.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

const magic = "HRBACKUP1"
const chunkSize = 64 * 1024

func aead(key, header []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("backup key must contain 32 bytes")
	}
	// Derive a distinct AES key for every archive's random 128-bit identifier.
	h := hmac.New(sha256.New, key)
	h.Write(header)
	b, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func Seal(w io.Writer, r io.Reader, key []byte) error {
	header := make([]byte, len(magic)+16)
	copy(header, magic)
	if _, err := rand.Read(header[len(magic):]); err != nil {
		return err
	}
	g, err := aead(key, header)
	if err != nil {
		return err
	}
	if _, err = w.Write(header); err != nil {
		return err
	}
	buf := make([]byte, chunkSize)
	for seq := uint32(0); ; seq++ {
		n, readErr := io.ReadFull(r, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		nonce := make([]byte, 12)
		binary.BigEndian.PutUint32(nonce[8:], seq)
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(n))
		aad := append(append(append([]byte{}, header...), nonce[8:]...), length[:]...)
		sealed := g.Seal(nil, nonce, buf[:n], aad)
		if _, err = w.Write(length[:]); err != nil {
			return err
		}
		if _, err = w.Write(sealed); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if seq == ^uint32(0) {
			return errors.New("backup exceeds format limit")
		}
	}
}

func Open(w io.Writer, r io.Reader, key []byte) error {
	header := make([]byte, len(magic)+16)
	if _, err := io.ReadFull(r, header); err != nil {
		return errors.New("invalid backup header")
	}
	if string(header[:len(magic)]) != magic {
		return errors.New("unsupported backup format")
	}
	g, err := aead(key, header)
	if err != nil {
		return err
	}
	for seq := uint32(0); ; seq++ {
		var length [4]byte
		if _, err = io.ReadFull(r, length[:]); err != nil {
			return errors.New("truncated backup")
		}
		n := binary.BigEndian.Uint32(length[:])
		if n > chunkSize {
			return errors.New("invalid backup record")
		}
		sealed := make([]byte, int(n)+g.Overhead())
		if _, err = io.ReadFull(r, sealed); err != nil {
			return errors.New("truncated backup")
		}
		nonce := make([]byte, 12)
		binary.BigEndian.PutUint32(nonce[8:], seq)
		aad := append(append(append([]byte{}, header...), nonce[8:]...), length[:]...)
		plain, err := g.Open(nil, nonce, sealed, aad)
		if err != nil {
			return errors.New("backup authentication failed")
		}
		if n == 0 {
			var extra [1]byte
			count, err := r.Read(extra[:])
			if count != 0 || err != io.EOF {
				return errors.New("trailing backup data")
			}
			return nil
		}
		if _, err = w.Write(plain); err != nil {
			return err
		}
		if seq == ^uint32(0) {
			return errors.New("backup exceeds format limit")
		}
	}
}
