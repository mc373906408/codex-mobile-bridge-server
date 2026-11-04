package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
)

func NewEmailCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func ValidEmailCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// A keyed digest prevents offline enumeration of the six-digit code from a DB dump.
func EmailCodeDigest(key []byte, accountID, code string) ([]byte, error) {
	if len(key) != 32 || !ValidEmailCode(code) {
		return nil, errors.New("invalid email code digest input")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("bridge/email-verification-code/v1\x00" + accountID + "\x00" + code))
	return mac.Sum(nil), nil
}
