package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

func Encrypt(key []byte, context string, plain []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, []byte(context)), nil
}
func Decrypt(key []byte, context string, encrypted []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(encrypted) < g.NonceSize()+g.Overhead() {
		return nil, errors.New("invalid encrypted value")
	}
	return g.Open(nil, encrypted[:g.NonceSize()], encrypted[g.NonceSize():], []byte(context))
}
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}
func TOTP(secret string, step int64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(key) < 20 {
		return "", errors.New("invalid TOTP secret")
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(step))
	h := hmac.New(sha1.New, key)
	h.Write(b[:])
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 15
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000), nil
}
func MatchTOTP(secret, code string, now time.Time, last int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	found := int64(-1)
	step := now.Unix() / 30
	for _, n := range []int64{step - 1, step, step + 1} {
		expected, err := TOTP(secret, n)
		if err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 && n > last {
			found = n
		}
	}
	return found, found >= 0
}
func NewRecoveryCodes() ([]string, error) {
	codes := make([]string, 8)
	for i := range codes {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		raw := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
		codes[i] = raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:]
	}
	return codes, nil
}
func NormalizeRecovery(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}
