package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const memory = 64 * 1024

func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must contain at least 12 characters and at most 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 3, memory, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(password, hash string) bool {
	parts := strings.Split(hash, "$")
	if len(password) > 1024 || len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=2" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, memory, 2, 32)
	return subtle.ConstantTimeCompare(actual, key) == 1
}
