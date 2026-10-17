package auth

import (
	"strings"
	"testing"
)

func TestRoles(t *testing.T) {
	if !Allows([]string{Publisher}, Configurator) || Allows([]string{Viewer}, Operator) || !Allows([]string{Viewer, Admin}, Admin) {
		t.Fatal("hierarchy")
	}
	if Allows([]string{Admin}, "root") || Allows(nil, Viewer) || Allows([]string{"ghost"}, Viewer) {
		t.Fatal("unknown")
	}
	if ValidRoles(nil) == nil || ValidRoles([]string{"x"}) == nil || ValidRoles([]string{Viewer, Admin}) != nil {
		t.Fatal("valid")
	}
}

func TestPasswords(t *testing.T) {
	h, err := HashPassword("Correct-Horse-9")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatal(h, err)
	}
	h2, _ := HashPassword("Correct-Horse-9")
	if h == h2 {
		t.Fatal("salt not random")
	}
	if ok, err := VerifyPassword(h, "Correct-Horse-9"); !ok || err != nil {
		t.Fatal("verify")
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Fatal("wrong accepted")
	}
	parts := strings.Split(h, "$")
	bad := []string{
		"plain",
		"$bcrypt$v=19$m=1,t=1,p=1$a$b",
		strings.Join([]string{"", "argon2id", "v=18", parts[3], parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", "vx", parts[3], parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], "m=0,t=1,p=1", parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], "junk", parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], parts[3], "!!", parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], parts[3], parts[4], ""}, "$"),
	}
	for _, b := range bad {
		if _, err := VerifyPassword(b, "x"); err != ErrBadHash {
			t.Errorf("%q: %v", b, err)
		}
	}
}

func TestPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{
		"short1A!":         false,
		"alllowercaseonly": false,
		"lowerUPPERonly":   false,
		"lowerUPPER123":    true,
		"lower123!!!!!":    true,
		"ÄÖÜäöü123456":     true,
	} {
		if err := CheckPasswordPolicy(pw); (err == nil) != ok {
			t.Errorf("%q: %v", pw, err)
		}
	}
}

func TestTokens(t *testing.T) {
	a, ha, err := NewToken()
	b, _, _ := NewToken()
	if err != nil || a == b || len(a) != 43 || string(ha) != string(TokenHash(a)) || len(ha) != 32 {
		t.Fatal(a, b, err)
	}
}
