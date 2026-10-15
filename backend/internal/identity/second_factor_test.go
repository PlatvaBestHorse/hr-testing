package identity

import (
	"bytes"
	"encoding/base32"
	"testing"
	"time"
)

func TestRFC6238TOTP(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for _, v := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		actual, err := TOTP(secret, v.unix/30)
		if err != nil || actual != v.code {
			t.Fatalf("RFC vector %d failed", v.unix)
		}
		step, ok := MatchTOTP(secret, v.code, time.Unix(v.unix, 0), -1)
		if !ok || step != v.unix/30 {
			t.Fatal("valid step rejected")
		}
		if _, ok = MatchTOTP(secret, v.code, time.Unix(v.unix, 0), step); ok {
			t.Fatal("accepted reused TOTP")
		}
		if _, ok = MatchTOTP(secret, v.code, time.Unix(v.unix+90, 0), -1); ok {
			t.Fatal("accepted out-of-window TOTP")
		}
	}
}
func TestAccountEncryptionAndRecoveryCodes(t *testing.T) {
	key := bytes.Repeat([]byte{5}, 32)
	plain := []byte("fixture secret")
	cipher, err := Encrypt(key, "totp:account", plain)
	if err != nil || bytes.Contains(cipher, plain) {
		t.Fatal("encryption failed")
	}
	actual, err := Decrypt(key, "totp:account", cipher)
	if err != nil || !bytes.Equal(actual, plain) {
		t.Fatal("roundtrip failed")
	}
	for _, context := range []string{"totp:another", "pending:account", "mail:account"} {
		if _, err = Decrypt(key, context, cipher); err == nil {
			t.Fatal("ciphertext moved between accounts/purposes")
		}
	}
	cipher[len(cipher)-1] ^= 1
	if _, err = Decrypt(key, "totp:account", cipher); err == nil {
		t.Fatal("accepted modified ciphertext")
	}
	if _, err = Decrypt(key[:16], "totp:account", cipher); err == nil {
		t.Fatal("accepted short encryption key")
	}
	codes, err := NewRecoveryCodes()
	if err != nil || len(codes) != 8 {
		t.Fatal("code generation failed")
	}
	seen := map[string]bool{}
	for _, code := range codes {
		normal := NormalizeRecovery(code)
		if len(normal) != 16 || seen[normal] {
			t.Fatal("invalid or repeated recovery code")
		}
		seen[normal] = true
	}
}
