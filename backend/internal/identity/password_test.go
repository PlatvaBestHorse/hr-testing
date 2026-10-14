package identity

import (
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	password := "test-only-long-password"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, password) || !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatal("password must be stored as Argon2id hash")
	}
	if !VerifyPassword(password, hash) || VerifyPassword(password+"wrong", hash) {
		t.Fatal("verification failed")
	}
	for _, malformed := range []string{"", "$argon2id$v=19$m=999999999,t=3,p=2$a$b", strings.Replace(hash, "v=19", "v=18", 1)} {
		if VerifyPassword(password, malformed) {
			t.Fatal("accepted unsupported hash")
		}
	}
	if _, err = HashPassword("short"); err == nil {
		t.Fatal("accepted short password")
	}
}
