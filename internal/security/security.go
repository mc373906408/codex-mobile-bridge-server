package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

func Random(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	return b, err
}
func Token() (string, error) {
	b, err := Random(32)
	return base64.RawURLEncoding.EncodeToString(b), err
}
func Digest(value string) []byte { d := sha256.Sum256([]byte(value)); return d[:] }

func ID() (string, error) {
	b, err := Random(16)
	if err != nil {
		return "", err
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}

func IDBytes(id string) ([]byte, error) {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || strings.ToLower(id) != id {
		return nil, errors.New("invalid identifier")
	}
	b, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil || len(b) != 16 {
		return nil, errors.New("invalid identifier")
	}
	return b, nil
}

func Password(value string) (string, error) {
	if len(value) < 12 || len(value) > 256 {
		return "", errors.New("password must contain 12 to 256 bytes")
	}
	return hashPassword(value)
}

// This hash is only for the restricted, one-time administrator setup flow.
func DefaultAdministratorPassword() (string, error) {
	return hashPassword("admin")
}

func hashPassword(value string) (string, error) {
	salt, err := Random(16)
	if err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(value), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(value, encoded string) bool {
	if len(value) > 256 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false
	}
	m, e1 := strconv.Atoi(strings.TrimPrefix(params[0], "m="))
	t, e2 := strconv.Atoi(strings.TrimPrefix(params[1], "t="))
	p, e3 := strconv.Atoi(strings.TrimPrefix(params[2], "p="))
	if e1 != nil || e2 != nil || e3 != nil || m != 65536 || t != 3 || p != 2 {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, e2 := base64.RawStdEncoding.DecodeString(parts[5])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(want) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(value), salt, uint32(t), uint32(m), uint8(p), 32)
	return subtle.ConstantTimeCompare(actual, want) == 1
}

type Vault struct{ aead cipher.AEAD }

func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("data key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

func (v *Vault) Seal(context string, value []byte) ([]byte, error) {
	nonce, err := Random(v.aead.NonceSize())
	if err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, value, []byte(context)), nil
}

func (v *Vault) Open(context string, value []byte) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(value) < n {
		return nil, errors.New("invalid encrypted value")
	}
	return v.aead.Open(nil, value[:n], value[n:], []byte(context))
}
