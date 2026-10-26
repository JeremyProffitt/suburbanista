package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
)

// The same transition contract runs against the fake and DynamoDB Local.
func credentialConformance(t *testing.T, s *Store, b onstar.Bootstrap) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1000, 0)
	if ok, e := s.Seed(ctx, b, 1, "synthetic-device"); !ok || e != nil {
		t.Fatal("initial seed", e)
	}
	lease, e := s.Acquire(ctx, "first", now)
	if e != nil {
		t.Fatal("first lease", e)
	}
	if _, e = s.Acquire(ctx, "second", now); !errors.Is(e, ErrConflict) {
		t.Fatal("concurrent lease", e)
	}
	record, e := s.Load(ctx, lease, now)
	if e != nil {
		t.Fatal("load", e)
	}
	record.Grant.RefreshToken = "synthetic-rotated-token"
	saved, e := s.Save(ctx, lease, record, now)
	if e != nil {
		t.Fatal("save rotation", e)
	}
	if _, e = s.Save(ctx, lease, record, now); !errors.Is(e, ErrConflict) {
		t.Fatal("stale save", e)
	}
	if ok, e := s.Seed(ctx, b, 1, "synthetic-device"); ok || e != nil {
		t.Fatal("ordinary deploy reseed", e)
	}
	newer := b
	newer.EnrollmentVersion = 2
	if _, e = s.Seed(ctx, newer, 2, "synthetic-device"); !errors.Is(e, ErrConflict) {
		t.Fatal("seed broke lease", e)
	}
	next, e := s.Acquire(ctx, "second", now.Add(151*time.Second))
	if e != nil || next.Fence <= lease.Fence {
		t.Fatal("new lease", e)
	}
	if _, e = s.Save(ctx, lease, saved, now); !errors.Is(e, ErrConflict) {
		t.Fatal("stale fence", e)
	}
	if e = s.Release(ctx, lease); !errors.Is(e, ErrConflict) {
		t.Fatal("stale release", e)
	}
	loaded, e := s.Load(ctx, next, now.Add(151*time.Second))
	if e != nil || loaded.Grant.RefreshToken != "synthetic-rotated-token" || loaded.Bootstrap.RefreshToken != loaded.Grant.RefreshToken {
		t.Fatal("durable rotation", e)
	}
	if e = s.Release(ctx, next); e != nil {
		t.Fatal("release", e)
	}
	if ok, e := s.Seed(ctx, newer, 2, "synthetic-device"); !ok || e != nil {
		t.Fatal("explicit reenrollment", e)
	}
	last, e := s.Acquire(ctx, "third", now.Add(152*time.Second))
	if e != nil || last.Fence <= next.Fence {
		t.Fatal("reenrollment reset fence", e)
	}
	// A crashed holder must not block explicit reenrollment after its lease expires.
	s.now = func() time.Time { return now.Add(303 * time.Second) }
	newer.EnrollmentVersion = 3
	if ok, e := s.Seed(ctx, newer, 3, "synthetic-device"); !ok || e != nil {
		t.Fatal("expired lease blocked reenrollment", e)
	}
	final, e := s.Acquire(ctx, "fourth", s.now())
	if e != nil || final.Fence <= last.Fence {
		t.Fatal("expired reenrollment reset fence", e)
	}
}
func TestCredentialConformanceFake(t *testing.T) {
	s, _, b := testStore(t)
	credentialConformance(t, s, b)
}
