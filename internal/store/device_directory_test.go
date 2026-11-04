package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func enrollDirectoryDevice(t *testing.T, s *store.Store, a store.Account, role, name string, key byte) store.Device {
	t.Helper()
	p, _ := testutil.Principal(t, s, a)
	d, err := s.Enroll(context.Background(), p, role, "", name, append([]byte{key}, make([]byte, 31)...), 16)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func directory(t *testing.T, s *store.Store, d store.Device) []store.Binding {
	t.Helper()
	items, err := s.DeviceBindings(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDirectoryDiscoversEveryPairInEitherLoginOrder(t *testing.T) {
	for _, mobileFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "desktop-first", true: "phone-first"}[mobileFirst], func(t *testing.T) {
			s := testutil.Database(t)
			a := testutil.Account(t, s, "directory")
			var g1, m1 store.Device
			if mobileFirst {
				m1 = enrollDirectoryDevice(t, s, a, "mobile", "Phone one", 2)
			}
			g1 = enrollDirectoryDevice(t, s, a, "gateway", "Desktop one", 1)
			if !mobileFirst {
				m1 = enrollDirectoryDevice(t, s, a, "mobile", "Phone one", 2)
			}
			g2 := enrollDirectoryDevice(t, s, a, "gateway", "Desktop two", 3)
			m2 := enrollDirectoryDevice(t, s, a, "mobile", "Phone two", 4)
			other := testutil.Account(t, s, "other-directory")
			outsider := enrollDirectoryDevice(t, s, other, "mobile", "Outsider", 5)
			if len(directory(t, s, outsider)) != 0 {
				t.Fatal("cross-account discovery")
			}
			for _, d := range []store.Device{g1, g2, m1, m2} {
				items := directory(t, s, d)
				if len(items) != 2 {
					t.Fatal("not every same-account device was discovered", len(items))
				}
				for _, b := range items {
					if b.Status != "pending" || b.AccessRevision != 0 || b.DeviceName == "" || b.GatewayName == "" {
						t.Fatal("invalid discovery state")
					}
					if _, err := s.Binding(context.Background(), b.ID, d, false); !errors.Is(err, store.ErrDenied) {
						t.Fatal("directory granted authority")
					}
				}
			}
			var count int
			if err := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM bindings").Scan(&count); err != nil || count != 4 {
				t.Fatal("directory reads created relationships", count, err)
			}
		})
	}
}

func TestDirectoryKeepsOfflineDevicesAndNeverResurrectsRevocations(t *testing.T) {
	s := testutil.Database(t)
	a := testutil.Account(t, s, "offline-directory")
	g := enrollDirectoryDevice(t, s, a, "gateway", "Desktop", 1)
	m := enrollDirectoryDevice(t, s, a, "mobile", "Phone", 2)
	b := directory(t, s, m)[0]
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "UPDATE sessions SET refresh_expires_at=now()-interval '1 year' WHERE id=$1", m.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if got := directory(t, s, g); len(got) != 1 || got[0].ID != b.ID || got[0].MobileOnline {
		t.Fatal("offline device disappeared")
	}
	if _, err := s.SetBindingAccess(ctx, b.ID, g, m.PublicKey, true, 0, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired phone session authorized")
	}
	if err := s.RevokeBinding(ctx, b.ID, g.AccountID); err != nil {
		t.Fatal(err)
	}
	p, _ := testutil.Principal(t, s, a)
	returned, err := s.Enroll(ctx, p, "mobile", m.ID, "Phone returned", m.PublicKey, 16)
	if err != nil {
		t.Fatal(err)
	}
	m = returned
	items := directory(t, s, g)
	if len(items) != 1 || items[0].ID != b.ID || items[0].Status != "revoked" {
		t.Fatal("revoked device was resurrected")
	}
	if _, err := s.SetBindingAccess(ctx, b.ID, g, m.PublicKey, true, items[0].AccessRevision, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("permanent revocation restored")
	}
	if _, err := s.Enroll(ctx, p, "mobile", m.ID, "Changed key", append([]byte{9}, make([]byte, 31)...), 16); !errors.Is(err, store.ErrDenied) {
		t.Fatal("changed installation key inherited identity")
	}
}

func TestAccessSwitchRetainsKeysAndFencesLateOpenings(t *testing.T) {
	s := testutil.Database(t)
	g, m, active := testutil.Devices(t, s)
	ctx := context.Background()
	if _, err := s.SetBindingAccess(ctx, active.ID, m, m.PublicKey, false, active.AccessRevision, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("phone changed desktop authority")
	}
	if _, err := s.SetBindingAccess(ctx, active.ID, g, append([]byte{9}, make([]byte, 31)...), true, active.AccessRevision, false); !errors.Is(err, store.ErrDenied) {
		t.Fatal("wrong key accepted")
	}
	disabled, err := s.SetBindingAccess(ctx, active.ID, g, m.PublicKey, false, active.AccessRevision, false)
	if err != nil || disabled.Status != "disabled" {
		t.Fatal("close failed", err)
	}
	if _, err = s.Binding(ctx, active.ID, m, true); !errors.Is(err, store.ErrDenied) {
		t.Fatal("disabled phone can connect")
	}
	if _, err = s.SetBindingAccess(ctx, active.ID, g, m.PublicKey, true, active.AccessRevision, false); !errors.Is(err, store.ErrConflict) {
		t.Fatal("late opening overwrote close")
	}
	restored, err := s.SetBindingAccess(ctx, active.ID, g, m.PublicKey, true, disabled.AccessRevision, false)
	if err != nil || restored.Status != "active" || string(restored.MobilePublicKey) != string(active.MobilePublicKey) {
		t.Fatal("restore changed identity", err)
	}
	// The close wins even if the in-flight opening committed first.
	closed, err := s.SetBindingAccess(ctx, active.ID, g, m.PublicKey, false, disabled.AccessRevision, false)
	if err != nil || closed.AccessRevision != restored.AccessRevision+1 || closed.Status != "disabled" {
		t.Fatal("close lost to a concurrent opening", err)
	}
	if _, err = s.SetBindingAccess(ctx, active.ID, g, m.PublicKey, true, restored.AccessRevision, false); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale restore accepted")
	}
	if _, err = s.SetBindingAccess(ctx, active.ID, g, m.PublicKey, false, closed.AccessRevision+1, false); !errors.Is(err, store.ErrConflict) {
		t.Fatal("future revision accepted")
	}
	if len(directory(t, s, m)) != 1 {
		t.Fatal("switch created another relationship")
	}
}

func TestAccessSwitchCancelsOnlyTargetJobsAndRejectsExpiredService(t *testing.T) {
	s := testutil.Database(t)
	g, m, b := testutil.Devices(t, s)
	ctx := context.Background()
	a, err := s.AccountByLogin(ctx, "devices")
	if err != nil {
		t.Fatal(err)
	}
	m2 := enrollDirectoryDevice(t, s, a, "mobile", "Second phone", 3)
	b2 := directory(t, s, m2)[0]
	for _, binding := range []store.Binding{b, b2} {
		endpoint, _ := security.ID()
		revision, _ := security.ID()
		job, _ := security.ID()
		notification, _ := security.ID()
		_, err = s.Pool.Exec(ctx, `INSERT INTO push_endpoints(id,binding_id,platform,app_id,token_ciphertext,revision,enabled,consent) VALUES($1,$2,'umeng','test','ciphertext',$3,true,true)`, endpoint, binding.ID, revision)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Pool.Exec(ctx, `INSERT INTO push_jobs(id,binding_id,notification_id,endpoint_id,endpoint_revision,ciphertext,expires_at,available_at) VALUES($1,$2,$3,$4,$5,'ciphertext',now()+interval '1 hour',now())`, job, binding.ID, notification, endpoint, revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.SetBindingAccess(ctx, b.ID, g, m.PublicKey, false, b.AccessRevision, true); err != nil {
		t.Fatal("expired service prevented closing", err)
	}
	var cancelled, queued int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FILTER(WHERE status='cancelled'),count(*) FILTER(WHERE status='queued') FROM push_jobs").Scan(&cancelled, &queued); err != nil || cancelled != 1 || queued != 1 {
		t.Fatal("close cancelled unrelated phone jobs", err)
	}
	if _, err = s.SetBindingAccess(ctx, b.ID, g, m.PublicKey, true, b.AccessRevision+1, true); !errors.Is(err, store.ErrDenied) {
		t.Fatal("expired hosted entitlement allowed restore")
	}
	var consent bool
	if err = s.Pool.QueryRow(ctx, "SELECT consent FROM push_endpoints WHERE binding_id=$1", b.ID).Scan(&consent); err != nil || !consent {
		t.Fatal("access switch changed independent push consent")
	}
}

func TestConcurrentEnrollmentsAndRepeatsHaveStableDirectoryIDs(t *testing.T) {
	s := testutil.Database(t)
	a := testutil.Account(t, s, "concurrent-directory")
	g := enrollDirectoryDevice(t, s, a, "gateway", "Desktop", 1)
	p, _ := testutil.Principal(t, s, a)
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for n := range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := s.Enroll(context.Background(), p, "mobile", "", "Phone", append([]byte{byte(n + 2)}, make([]byte, 31)...), 16)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	before := directory(t, s, g)
	if len(before) != 8 {
		t.Fatal("concurrent enrollment lost phones")
	}
	if _, err := s.Enroll(context.Background(), p, "gateway", g.ID, "Renamed desktop", g.PublicKey, 16); err != nil {
		t.Fatal(err)
	}
	after := directory(t, s, g)
	for n := range before {
		if before[n].ID != after[n].ID || after[n].GatewayName != "Renamed desktop" {
			t.Fatal("repeat enrollment changed relationships")
		}
	}
}

func TestFreshSchemaHasOnlyCurrentBindingState(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	var retired int
	if err := s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='pairings') +
		(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='bindings' AND column_name='pairing_id')`).Scan(&retired); err != nil || retired != 0 {
		t.Fatal("fresh database still contains manual pairing state", err)
	}
	g, m, b := testutil.Devices(t, s)
	if _, err := s.SetBindingAccess(ctx, b.ID, g, m.PublicKey, false, b.AccessRevision, false); err != nil {
		t.Fatal(err)
	}
	if got := directory(t, s, m); len(got) != 1 || got[0].Status != "disabled" || got[0].AccessRevision != b.AccessRevision+1 {
		t.Fatal("fresh database does not support reversible access")
	}
}
