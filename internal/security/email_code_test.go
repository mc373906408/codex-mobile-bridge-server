package security

import (
	"bytes"
	"testing"
)

func TestEmailCodeFormatAndKeyedScope(t *testing.T) {
	for range 100 {
		code, err := NewEmailCode()
		if err != nil || !ValidEmailCode(code) {
			t.Fatal("code generation must produce six ASCII digits", err)
		}
	}
	for _, invalid := range []string{"", "12345", "1234567", "12345a", " 123456", "１２３４５６"} {
		if ValidEmailCode(invalid) {
			t.Fatal("noncanonical verification code accepted")
		}
	}
	key := make([]byte, 32)
	first, err := EmailCodeDigest(key, "account-a", "012345")
	if err != nil || len(first) != 32 {
		t.Fatal(err)
	}
	second, _ := EmailCodeDigest(key, "account-b", "012345")
	key[0] = 1
	third, _ := EmailCodeDigest(key, "account-a", "012345")
	if bytes.Equal(first, second) || bytes.Equal(first, third) || bytes.Equal(first, Digest("012345")) {
		t.Fatal("code digests must be account-scoped and keyed")
	}
	if _, err = EmailCodeDigest(nil, "account-a", "012345"); err == nil {
		t.Fatal("missing digest key accepted")
	}
}
