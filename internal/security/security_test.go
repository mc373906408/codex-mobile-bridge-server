package security

import "testing"

func TestPasswordsAndVault(t *testing.T) {
	a, err := Password("sufficient-password-123")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Password("sufficient-password-123")
	if a == b || !VerifyPassword("sufficient-password-123", a) || VerifyPassword("wrong", a) {
		t.Fatal("password hashing boundary")
	}
	for _, raw := range []string{"", "$argon2id$v=19$m=999999999,t=99,p=99$bad$bad", a + "x"} {
		if VerifyPassword("sufficient-password-123", raw) {
			t.Fatal("invalid hash accepted")
		}
	}
	v, err := NewVault(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal("record:1", []byte("sensitive token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.Open("record:2", sealed); err == nil {
		t.Fatal("AAD substitution accepted")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = v.Open("record:1", sealed); err == nil {
		t.Fatal("tamper accepted")
	}
	token, _ := Token()
	if len(token) != 43 {
		t.Fatal("token length")
	}
	id, _ := ID()
	if _, err = IDBytes(id); err != nil {
		t.Fatal(err)
	}
}
