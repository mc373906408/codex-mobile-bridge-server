package security

import (
	"crypto/rand"
	"encoding/base32"
	"io"
)

const RedemptionCodeLength = 16

func NewRedemptionCode() (string, error) {
	return redemptionCode(rand.Reader)
}

func redemptionCode(reader io.Reader) (string, error) {
	// Ten random bytes encode to 16 uniform Base32 characters: 80 bits, no prefix.
	var random [10]byte
	if _, err := io.ReadFull(reader, random[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random[:]), nil
}
