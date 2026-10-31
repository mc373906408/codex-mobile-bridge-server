package worker_test

import (
	"context"
	"encoding/base64"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/push"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/worker"
	"testing"
	"time"
)

type sender struct {
	calls  int
	result push.Result
	during func(push.Delivery)
}

func (s *sender) Send(_ context.Context, d push.Delivery) push.Result {
	s.calls++
	if s.during != nil {
		s.during(d)
	}
	return s.result
}
func envelope(binding string) store.Envelope {
	id, _ := security.ID()
	return store.Envelope{Version: 1, NotificationID: id, BindingID: binding, ExpiresAt: time.Now().Unix() + 3600, Ciphertext: base64.StdEncoding.EncodeToString(make([]byte, 80))}
}

func TestLeasesAckIdempotencyAndEligibility(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	g, m, b := testutil.Devices(t, s)
	c := testutil.Config()
	c.Umeng.AppKey = "test-key"
	c.Umeng.MasterSecret = "test-secret"
	in := store.EndpointInput{BindingID: b.ID, Platform: "umeng", AppID: c.Umeng.AppKey, Token: "test-token-123456789012345", Enabled: true, Consent: true}
	endpoint, err := s.RegisterEndpoint(ctx, m, in, c)
	if err != nil {
		t.Fatal(err)
	}
	e := envelope(b.ID)
	id, err := s.QueuePush(ctx, g, e, endpoint.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.RegisterEndpoint(ctx, m, in, c)
	if err != nil || same.Revision != endpoint.Revision {
		t.Fatal("endpoint re-registration cancels messages", err)
	}
	duplicate, err := s.QueuePush(ctx, g, e, endpoint.Revision, false)
	if err != nil || duplicate != id {
		t.Fatal("push enqueue not idempotent", err)
	}
	fake := &sender{result: push.Result{Accepted: true, Code: "accepted"}}
	w := worker.Worker{Store: s, Config: c, Push: fake}
	for range 8 {
		_, _ = s.Pool.Exec(ctx, "UPDATE push_jobs SET available_at=now() WHERE id=$1", id)
		if _, err = w.StepPush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var attempts int
	var status string
	_ = s.Pool.QueryRow(ctx, "SELECT attempts,status FROM push_jobs WHERE id=$1", id).Scan(&attempts, &status)
	if attempts != 0 || status != "queued" || fake.calls != 0 {
		t.Fatal("direct leases consume retries", attempts, status, fake.calls)
	}
	if err = s.Acknowledge(ctx, m, b.ID, []string{e.NotificationID}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool.Exec(ctx, "UPDATE push_jobs SET available_at=now() WHERE id=$1", id)
	_, _ = w.StepPush(ctx)
	if fake.calls != 0 {
		t.Fatal("ACK race sent push")
	}
	before := envelope(b.ID)
	if err = s.Acknowledge(ctx, m, b.ID, []string{before.NotificationID}); err != nil {
		t.Fatal(err)
	}
	beforeID, err := s.QueuePush(ctx, g, before, endpoint.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT status FROM push_jobs WHERE id=$1", beforeID).Scan(&status)
	if status != "acked" {
		t.Fatal("ACK-before-enqueue lost")
	}
	next := envelope(b.ID)
	nextID, err := s.QueuePush(ctx, g, next, endpoint.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool.Exec(ctx, "UPDATE push_endpoints SET direct_until=now()-interval '1 second' WHERE id=$1", endpoint.ID)
	_, _ = s.Pool.Exec(ctx, "UPDATE push_jobs SET available_at=now() WHERE id=$1", nextID)
	job, err := w.ClaimPush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool.Exec(ctx, "UPDATE push_jobs SET lease_until=now()-interval '1 second' WHERE id=$1", job.ID)
	fake.during = func(d push.Delivery) {
		if err := s.Acknowledge(ctx, m, b.ID, []string{d.Envelope.NotificationID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = w.StepPush(ctx); err != nil {
		t.Fatal(err)
	}
	_ = s.Pool.QueryRow(ctx, "SELECT status FROM push_jobs WHERE id=$1", nextID).Scan(&status)
	if status != "acked" || fake.calls != 1 {
		t.Fatal("completion overwrote concurrent ACK", status)
	}
}

func TestExpiredEntitlementAndInvalidTokens(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	g, m, b := testutil.Devices(t, s)
	c := testutil.Config()
	c.Umeng.AppKey = "test-key"
	c.Umeng.MasterSecret = "test-secret"
	in := store.EndpointInput{BindingID: b.ID, Platform: "umeng", AppID: c.Umeng.AppKey, Token: "test-token-123456789012345", Enabled: true, Consent: true}
	endpoint, err := s.RegisterEndpoint(ctx, m, in, c)
	if err != nil {
		t.Fatal(err)
	}
	e := envelope(b.ID)
	id, err := s.QueuePush(ctx, g, e, endpoint.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	fake := &sender{}
	c.Mode = "hosted"
	w := worker.Worker{Store: s, Config: c, Push: fake}
	_, _ = s.Pool.Exec(ctx, "UPDATE push_jobs SET available_at=now() WHERE id=$1", id)
	if _, err = w.StepPush(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = s.Pool.QueryRow(ctx, "SELECT status FROM push_jobs WHERE id=$1", id).Scan(&status)
	if status != "cancelled" || fake.calls != 0 {
		t.Fatal("inactive entitlement rescheduled through direct lease", status)
	}
	c.Mode = "selfhost"
	w.Config = c
	fake.result = push.Result{InvalidToken: true, Permanent: true, Code: "invalid_token"}
	_, _ = s.Pool.Exec(ctx, "UPDATE push_endpoints SET direct_until=now()-interval '1 second' WHERE id=$1", endpoint.ID)
	e = envelope(b.ID)
	_, err = s.QueuePush(ctx, g, e, endpoint.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.StepPush(ctx); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	_ = s.Pool.QueryRow(ctx, "SELECT enabled FROM push_endpoints WHERE id=$1", endpoint.ID).Scan(&enabled)
	if enabled {
		t.Fatal("invalid token not disabled")
	}
	in.Consent = false
	if _, err = s.RegisterEndpoint(ctx, m, in, c); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueuePush(ctx, g, envelope(b.ID), endpoint.Revision, false); err == nil {
		t.Fatal("consent withdrawn but push queued")
	}
}
