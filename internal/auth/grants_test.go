package auth

import (
	"errors"
	"testing"
	"time"
)

func TestGrantLifetime(t *testing.T) {
	now := time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)
	family, issued, err := NewFamily("owner", "owner", "owner", "phone", now)
	if err != nil {
		t.Fatal(err)
	}
	// Go's calendar arithmetic normalizes February 29 to March 1 in a
	// non-leap target year; this exact deadline is fixed at family creation.
	want := time.Date(2029, 3, 1, 12, 0, 0, 0, time.UTC)
	if !family.Deadline.Equal(want) || issued.AccessExpiresAt.Sub(now) != 15*time.Minute {
		t.Fatal("incorrect grant boundaries")
	}
	if family.CurrentHash == [32]byte{} || family.CurrentHash != issued.Handle.Hash || len(issued.RefreshHandle) != 43 || issued.RefreshHandle == issued.AccessHandle {
		t.Fatal("invalid handles")
	}
	if err := CheckFamily(family, "owner", now.Add(30*24*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatal("idle boundary accepted")
	}
	if err := CheckFamily(family, "owner", now.Add(30*24*time.Hour-time.Nanosecond)); err != nil {
		t.Fatal("idle interval rejected")
	}
	first, err := Rotate(family, issued.Handle, "owner", issued.RefreshHandle, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Family.Deadline.Equal(want) || first.Family.Version != 2 || first.Issued.RefreshHandle == issued.RefreshHandle {
		t.Fatal("rotation extended grant or reused handle")
	}
	conflict, err := Rotate(first.Family, first.Previous, "owner", issued.RefreshHandle, now.Add(time.Minute+time.Second))
	if !errors.Is(err, ErrRefreshRace) || conflict.Issued.RefreshHandle != "" || conflict.Family.Revoked {
		t.Fatal("grace did not fail without credentials")
	}
	second, err := Rotate(first.Family, first.Issued.Handle, "owner", first.Issued.RefreshHandle, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// An older tombstone still revokes after several rotations.
	reused, err := Rotate(second.Family, first.Previous, "owner", issued.RefreshHandle, now.Add(3*time.Minute))
	if !errors.Is(err, ErrRevoked) || !reused.Family.Revoked || reused.Issued.AccessHandle != "" {
		t.Fatal("superseded reuse accepted")
	}
	if err := CheckFamily(reused.Family, "owner", now.Add(3*time.Minute)); !errors.Is(err, ErrRevoked) {
		t.Fatal("revocation not enforced at dispatch")
	}
	family.LastUsedAt = want.Add(-time.Minute)
	if err := CheckFamily(family, "owner", want); !errors.Is(err, ErrExpired) {
		t.Fatal("absolute boundary accepted")
	}
	last, err := Rotate(family, issued.Handle, "owner", issued.RefreshHandle, want.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !last.Issued.AccessExpiresAt.Equal(want) {
		t.Fatal("access outlives grant")
	}
}

func TestOwnerBinding(t *testing.T) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for _, owners := range [][3]string{{"", "", ""}, {"owner", "", "owner"}, {"owner", "other", "owner"}, {"owner", "owner", "other"}} {
		if _, _, err := NewFamily(owners[0], owners[1], owners[2], "", now); !errors.Is(err, ErrNotOwner) {
			t.Fatal("unbound owner received family")
		}
	}
	family, issued, err := NewFamily("owner", "owner", "owner", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckFamily(family, "new-owner", now); !errors.Is(err, ErrNotOwner) {
		t.Fatal("owner change retained authority")
	}
	other, err := randomHandle()
	if err != nil {
		t.Fatal(err)
	}
	result, err := Rotate(family, issued.Handle, "owner", other, now)
	if !errors.Is(err, ErrHandle) || result.Family.Revoked {
		t.Fatal("unknown handle affects family")
	}
	stored := issued.Handle
	stored.FamilyID = "other"
	if _, err := Rotate(family, stored, "owner", issued.RefreshHandle, now); !errors.Is(err, ErrHandle) {
		t.Fatal("cross-family handle accepted")
	}
}
