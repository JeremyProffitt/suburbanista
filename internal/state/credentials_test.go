package state

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/credential"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDB struct {
	item      map[string]types.AttributeValue
	writes    int
	beforePut func()
}

func (f *fakeDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if in.ConsistentRead == nil || !*in.ConsistentRead {
		return nil, errors.New("read must be consistent")
	}
	return &dynamodb.GetItemOutput{Item: f.item}, nil
}
func conflict() error { return &types.ConditionalCheckFailedException{} }
func (f *fakeDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if f.beforePut != nil {
		f.beforePut()
	}
	if f.item != nil {
		old, _ := number(f.item, "enrollmentVersion")
		version, _ := number(f.item, "tokenVersion")
		expected, _ := number(in.ExpressionAttributeValues, ":old")
		expectedVersion, _ := number(in.ExpressionAttributeValues, ":version")
		expiry, _ := number(f.item, "leaseExpiresAt")
		now, _ := number(in.ExpressionAttributeValues, ":now")
		fence, _ := number(f.item, "leaseFence")
		observedFence, _ := number(in.ExpressionAttributeValues, ":observedFence")
		if old != expected || version != expectedVersion || expiry >= now || fence != observedFence {
			return nil, conflict()
		}
	}
	f.item = in.Item
	f.writes++
	return &dynamodb.PutItemOutput{}, nil
}

func TestSeedCannotResetFenceAfterConcurrentLease(t *testing.T) {
	s, db, b := testStore(t)
	ctx := context.Background()
	if _, e := s.Seed(ctx, b, 1, "synthetic-device"); e != nil {
		t.Fatal(e)
	}
	db.beforePut = func() { db.item["leaseFence"] = nv(1); db.item["leaseExpiresAt"] = nv(0) }
	b.EnrollmentVersion = 2
	if _, e := s.Seed(ctx, b, 2, "synthetic-device"); !errors.Is(e, ErrConflict) {
		t.Fatal("seed overwrote a concurrently advanced fence", e)
	}
	fence, _ := number(db.item, "leaseFence")
	if fence != 1 {
		t.Fatal("fence regressed")
	}
}
func (f *fakeDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	v := in.ExpressionAttributeValues
	expression := *in.UpdateExpression
	if strings.Contains(expression, "ADD leaseFence") {
		expiry, _ := number(f.item, "leaseExpiresAt")
		now, _ := number(v, ":now")
		if f.item == nil || expiry >= now {
			return nil, conflict()
		}
		fence, _ := number(f.item, "leaseFence")
		f.item["leaseFence"] = nv(fence + 1)
		f.item["leaseHolder"] = v[":holder"]
		f.item["leaseExpiresAt"] = v[":until"]
	} else {
		fence, _ := number(f.item, "leaseFence")
		expectedFence, _ := number(v, ":fence")
		holder, ok := f.item["leaseHolder"].(*types.AttributeValueMemberS)
		if !ok || holder.Value != v[":holder"].(*types.AttributeValueMemberS).Value || fence != expectedFence {
			return nil, conflict()
		}
		if strings.Contains(expression, "encrypted") {
			version, _ := number(f.item, "tokenVersion")
			expected, _ := number(v, ":version")
			expiry, _ := number(f.item, "leaseExpiresAt")
			now, _ := number(v, ":now")
			if version != expected || expiry <= now {
				return nil, conflict()
			}
			f.item["encrypted"] = v[":encrypted"]
			f.item["tokenVersion"] = v[":next"]
		} else {
			f.item["leaseExpiresAt"] = nv(0)
			delete(f.item, "leaseHolder")
		}
	}
	f.writes++
	return &dynamodb.UpdateItemOutput{Attributes: f.item}, nil
}
func testStore(t *testing.T) (*Store, *fakeDB, onstar.Bootstrap) {
	t.Helper()
	keys, _ := credential.New(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), credential.SlotA)
	db := &fakeDB{}
	store, e := New(db, "synthetic-table", "synthetic-owner", keys)
	if e != nil {
		t.Fatal(e)
	}
	store.now = func() time.Time { return time.Unix(1000, 0) }
	bootstrap, e := onstar.NewBootstrap(onstar.Grant{RefreshToken: "synthetic-refresh", Subject: testdata.Subject, RefreshObtainedAt: 100}, "synthetic-device", testdata.VIN, 1, "ready")
	if e != nil {
		t.Fatal(e)
	}
	return store, db, bootstrap
}
func TestSeedVersionAndEncryptedStorage(t *testing.T) {
	s, db, b := testStore(t)
	ctx := context.Background()
	if ok, e := s.Seed(ctx, b, 1, "synthetic-device"); !ok || e != nil {
		t.Fatal(e)
	}
	blob := db.item["encrypted"].(*types.AttributeValueMemberB).Value
	if bytes.Contains(blob, []byte(b.VIN)) || bytes.Contains(blob, []byte(b.RefreshToken)) {
		t.Fatal("plaintext persisted")
	}
	writes := db.writes
	if ok, e := s.Seed(ctx, b, 1, "synthetic-device"); ok || e != nil || db.writes != writes {
		t.Fatal("ordinary deploy reseeded")
	}
	b.EnrollmentVersion = 2
	if _, e := s.Seed(ctx, b, 1, "synthetic-device"); !errors.Is(e, ErrInvalid) {
		t.Fatal("explicit version mismatch accepted")
	}
	if ok, e := s.Seed(ctx, b, 2, "synthetic-device"); !ok || e != nil {
		t.Fatal(e)
	}
}
func TestRefreshFencingAndTokenVersion(t *testing.T) {
	s, _, b := testStore(t)
	ctx := context.Background()
	_, _ = s.Seed(ctx, b, 1, "synthetic-device")
	now := time.Unix(1000, 0)
	lease, e := s.Acquire(ctx, "first", now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Acquire(ctx, "second", now); !errors.Is(e, ErrConflict) {
		t.Fatal("concurrent lease accepted")
	}
	r, e := s.Load(ctx, lease, now)
	if e != nil {
		t.Fatal(e)
	}
	r.Grant.RefreshToken = "synthetic-rotated"
	saved, e := s.Save(ctx, lease, r, now)
	if e != nil || saved.TokenVersion != 2 {
		t.Fatal("refresh persistence failed", e)
	}
	if _, e = s.Save(ctx, lease, r, now); !errors.Is(e, ErrConflict) {
		t.Fatal("stale token version accepted")
	}
	next, e := s.Acquire(ctx, "second", now.Add(151*time.Second))
	if e != nil || next.Fence <= lease.Fence {
		t.Fatal("expired lease not fenced")
	}
	if e = s.Release(ctx, lease); !errors.Is(e, ErrConflict) {
		t.Fatal("stale release accepted")
	}
	if _, e = s.Save(ctx, lease, saved, now); !errors.Is(e, ErrConflict) {
		t.Fatal("stale holder saved")
	}
	loaded, e := s.Load(ctx, next, now.Add(151*time.Second))
	if e != nil || loaded.Grant.RefreshToken != "synthetic-rotated" {
		t.Fatal("rotated refresh lost")
	}
}
func TestKeyRotationPersistsBeforeRead(t *testing.T) {
	s, db, b := testStore(t)
	ctx := context.Background()
	_, _ = s.Seed(ctx, b, 1, "synthetic-device")
	now := time.Unix(1000, 0)
	lease, _ := s.Acquire(ctx, "synthetic-holder", now)
	keys, _ := credential.New(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), credential.SlotB)
	s.keys = keys
	r, e := s.Load(ctx, lease, now)
	if e != nil || r.TokenVersion != 2 || r.generation != 2 {
		t.Fatal("rotation did not persist", e)
	}
	writes := db.writes
	if _, e = s.Load(ctx, lease, now); e != nil || db.writes != writes {
		t.Fatal("active key wrote again")
	}
}
