package store_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestSixteenCharacterBatchUsesIndependentCodesAndStoresOnlyDigests(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a := testutil.Account(t, s, "short-code-operator")
	codes, err := s.GenerateCodes(ctx, a.ID, 30, 100)
	if err != nil || len(codes) != 100 {
		t.Fatal("full redemption batch did not generate", err)
	}
	seen := make(map[string]bool)
	pattern := regexp.MustCompile(`^[A-Z2-7]{16}$`)
	for _, code := range codes {
		if !pattern.MatchString(code.Code) || code.Days != 30 || seen[code.Code] {
			t.Fatal("generated code must be unique, 16 characters and unrelated to its UUID")
		}
		seen[code.Code] = true
		if _, err = security.IDBytes(code.ID); err != nil {
			t.Fatal("record identity must remain a full UUID", err)
		}
		var stored []byte
		if err = s.Pool.QueryRow(ctx, "SELECT digest FROM redemption_codes WHERE id=$1", code.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if len(stored) != 32 || !bytes.Equal(stored, store.CodeDigest(code.Code)) || bytes.Equal(stored, []byte(code.Code)) {
			t.Fatal("database must store only the digest of the random code")
		}
	}
}

func TestShortAndLegacyRedemptionCodesRemainSingleUseAndNormalized(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	a := testutil.Account(t, s, "compatible-redeemer")
	other := testutil.Account(t, s, "other-compatible-redeemer")
	codes, err := s.GenerateCodes(ctx, a.ID, 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	legacyBytes, err := security.Random(24)
	if err != nil {
		t.Fatal(err)
	}
	legacy := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(legacyBytes)
	legacyID, err := security.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO redemption_codes(id,digest,days) VALUES($1,$2,$3)", legacyID, store.CodeDigest(legacy), 7); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{codes[0].Code, legacy} {
		formatted := " \t" + strings.ToLower(code[:8]+"-"+code[8:]) + "\r\n"
		request, _ := security.ID()
		first, err := s.Redeem(ctx, a.ID, formatted, request)
		if err != nil {
			t.Fatal("existing normalization and legacy codes must remain redeemable", err)
		}
		again, err := s.Redeem(ctx, a.ID, code, request)
		if err != nil || again.ID != first.ID || !again.ExpiresAt.Equal(first.ExpiresAt) {
			t.Fatal("canonical retry must return the same redemption without extending service", err)
		}
		request, _ = security.ID()
		if _, err = s.Redeem(ctx, other.ID, code, request); !errors.Is(err, store.ErrDenied) {
			t.Fatal("a used code cannot transfer service to another account")
		}
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE redemption_codes SET revoked_at=now() WHERE id=$1", codes[1].ID); err != nil {
		t.Fatal(err)
	}
	request, _ := security.ID()
	if _, err = s.Redeem(ctx, a.ID, codes[1].Code, request); !errors.Is(err, store.ErrDenied) {
		t.Fatal("revoked short code must not redeem")
	}
}
