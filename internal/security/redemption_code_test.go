package security

import (
	"bytes"
	"encoding/base32"
	"io"
	"regexp"
	"testing"
)

func TestRedemptionCodesAreSixteenRandomCharacters(t *testing.T) {
	pattern := regexp.MustCompile(`^[A-Z2-7]{16}$`)
	seen := make(map[string]bool)
	for range 1000 {
		code, err := NewRedemptionCode()
		if err != nil || len(code) != RedemptionCodeLength || !pattern.MatchString(code) {
			t.Fatal("redemption generation must produce 16 Base32 characters", err)
		}
		if seen[code] {
			t.Fatal("random redemption codes collided")
		}
		seen[code] = true
	}
}

func TestRedemptionEncodingPreservesAllEightyRandomBits(t *testing.T) {
	encoding := base32.StdEncoding.WithPadding(base32.NoPadding)
	baseline, err := redemptionCode(bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	for bit := range 80 {
		input := make([]byte, 10)
		input[bit/8] = 1 << (bit % 8)
		code, err := redemptionCode(bytes.NewReader(input))
		if err != nil || code == baseline {
			t.Fatal("random bit was lost or replaced with fixed UUID metadata", bit, err)
		}
		decoded, err := encoding.DecodeString(code)
		if err != nil || !bytes.Equal(decoded, input) {
			t.Fatal("redemption encoding must preserve all random bits", err)
		}
	}
}

func TestRedemptionGeneratorFailsClosedWithoutFullRandomness(t *testing.T) {
	for _, length := range []int{0, 1, 9} {
		code, err := redemptionCode(bytes.NewReader(make([]byte, length)))
		if err == nil || code != "" {
			t.Fatal("partial randomness must never produce a redeemable code")
		}
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			t.Fatal("unexpected random-reader error", err)
		}
	}
}
