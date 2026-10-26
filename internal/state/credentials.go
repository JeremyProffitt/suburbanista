// Package state persists encrypted provider grants with conditional fencing.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/credential"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var (
	ErrConflict = errors.New("credential conditional conflict")
	ErrMissing  = errors.New("credential not enrolled")
	ErrInvalid  = errors.New("invalid credential state")
	ErrStorage  = errors.New("credential storage unavailable")
)

type DynamoDBAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}
type Store struct {
	db           DynamoDBAPI
	table, owner string
	keys         *credential.Keys
	now          func() time.Time
}
type Lease struct {
	Holder    string
	Fence     uint64
	ExpiresAt int64
}
type Record struct {
	Bootstrap            onstar.Bootstrap
	Grant                onstar.Grant
	GMToken              string
	GMExpiresAt          int64
	LocationPending      bool
	ProviderState        string
	CooldownUntil        int64
	CooldownReason       string
	ProofRunID           string
	ProofRunIDs          []string `json:",omitempty"`
	ProofDeploymentNonce string
	ProofAttempts        int
	LastProofReason      string
	TokenVersion         uint64 `json:"-"`
	generation           uint64
	enrollment           int64
}

func New(db DynamoDBAPI, table, owner string, keys *credential.Keys) (*Store, error) {
	if db == nil || keys == nil || table == "" || owner == "" || strings.Contains(owner, "|") {
		return nil, ErrInvalid
	}
	return &Store{db: db, table: table, owner: owner, keys: keys, now: time.Now}, nil
}
func (s *Store) key() map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": sv("owner#" + s.owner), "sk": sv("credential#gm")}
}
func (s *Store) binding() credential.Binding {
	return credential.Binding{Owner: s.owner, Provider: "gm", ItemKey: "credential#gm"}
}
func sv(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func nv(v uint64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatUint(v, 10)}
}
func number(m map[string]types.AttributeValue, k string) (uint64, error) {
	v, ok := m[k].(*types.AttributeValueMemberN)
	if !ok {
		return 0, ErrInvalid
	}
	n, e := strconv.ParseUint(v.Value, 10, 64)
	if e != nil {
		return 0, ErrInvalid
	}
	return n, nil
}
func storageError(err error) error {
	var conditional *types.ConditionalCheckFailedException
	if errors.As(err, &conditional) {
		return ErrConflict
	}
	return ErrStorage
}
func (s *Store) read(ctx context.Context) (map[string]types.AttributeValue, error) {
	o, e := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: s.key(), ConsistentRead: aws.Bool(true)})
	if e != nil {
		return nil, storageError(e)
	}
	if len(o.Item) == 0 {
		return nil, ErrMissing
	}
	v, e := number(o.Item, "schemaVersion")
	if e != nil || v != 1 {
		return nil, ErrInvalid
	}
	return o.Item, nil
}
func (s *Store) encrypt(r Record, generation uint64) (types.AttributeValue, error) {
	plain, e := json.Marshal(r)
	if e != nil {
		return nil, ErrInvalid
	}
	defer clear(plain)
	v, e := s.keys.Encrypt(s.binding(), plain, generation)
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return nil, ErrInvalid
	}
	return &types.AttributeValueMemberB{Value: b}, nil
}

// Seed never decrypts or replaces an equal or newer enrollment. Explicit relink
// must carry exactly the requested enrollment version and may not break a lease.
func (s *Store) Seed(ctx context.Context, b onstar.Bootstrap, expected int64, deviceID string) (bool, error) {
	if expected <= 0 || b.EnrollmentVersion != expected || b.ValidateDevice(deviceID) != nil {
		return false, ErrInvalid
	}
	checked, e := onstar.NewBootstrap(onstar.Grant{RefreshToken: b.RefreshToken, Subject: b.B2CSubject, RefreshObtainedAt: b.RefreshObtainedAt}, deviceID, b.VIN, b.EnrollmentVersion, b.ProviderState)
	if e != nil || checked.Schema != b.Schema {
		return false, ErrInvalid
	}
	old, e := s.read(ctx)
	var version, enrollment uint64
	if e != nil && !errors.Is(e, ErrMissing) {
		return false, e
	}
	condition := "attribute_not_exists(pk)"
	values := map[string]types.AttributeValue{}
	if e == nil {
		enrollment, e = number(old, "enrollmentVersion")
		if e != nil {
			return false, e
		}
		if enrollment >= uint64(expected) {
			return false, nil
		}
		version, e = number(old, "tokenVersion")
		if e != nil || version == math.MaxUint64 {
			return false, ErrInvalid
		}
		condition = "enrollmentVersion = :old AND tokenVersion = :version AND (attribute_not_exists(leaseExpiresAt) OR leaseExpiresAt < :now)"
		values[":old"] = nv(enrollment)
		values[":version"] = nv(version)
		values[":now"] = nv(uint64(s.now().Unix()))
	}
	r := Record{Bootstrap: b, Grant: onstar.Grant{RefreshToken: b.RefreshToken, Subject: b.B2CSubject, RefreshObtainedAt: b.RefreshObtainedAt}, ProviderState: b.ProviderState}
	encrypted, e := s.encrypt(r, 1)
	if e != nil {
		return false, e
	}
	item := s.key()
	item["schemaVersion"] = nv(1)
	item["enrollmentVersion"] = nv(uint64(expected))
	item["tokenVersion"] = nv(version + 1)
	item["encrypted"] = encrypted
	// Preserve the monotonic fence even across an explicit enrollment replacement.
	fence := uint64(0)
	if old != nil {
		if _, ok := old["leaseFence"]; ok {
			fence, e = number(old, "leaseFence")
			if e != nil {
				return false, e
			}
			condition += " AND leaseFence = :observedFence"
			values[":observedFence"] = nv(fence)
		} else {
			condition += " AND attribute_not_exists(leaseFence)"
		}
	}
	item["leaseFence"] = nv(fence)
	in := &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: item, ConditionExpression: aws.String(condition)}
	if len(values) > 0 {
		in.ExpressionAttributeValues = values
	}
	_, e = s.db.PutItem(ctx, in)
	if e != nil {
		return false, storageError(e)
	}
	return true, nil
}
func (s *Store) Acquire(ctx context.Context, holder string, now time.Time) (Lease, error) {
	if holder == "" || now.Unix() <= 0 {
		return Lease{}, ErrInvalid
	}
	until := now.Add(150 * time.Second).Unix()
	out, e := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(s.table), Key: s.key(), UpdateExpression: aws.String("SET leaseHolder = :holder, leaseExpiresAt = :until ADD leaseFence :one"), ConditionExpression: aws.String("schemaVersion = :schema AND attribute_exists(encrypted) AND (attribute_not_exists(leaseExpiresAt) OR leaseExpiresAt < :now)"), ExpressionAttributeValues: map[string]types.AttributeValue{":holder": sv(holder), ":until": nv(uint64(until)), ":now": nv(uint64(now.Unix())), ":one": nv(1), ":schema": nv(1)}, ReturnValues: types.ReturnValueAllNew})
	if e != nil {
		return Lease{}, storageError(e)
	}
	fence, e := number(out.Attributes, "leaseFence")
	if e != nil || fence == 0 {
		return Lease{}, ErrInvalid
	}
	return Lease{holder, fence, until}, nil
}
func leaseValues(l Lease, now time.Time) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{":holder": sv(l.Holder), ":fence": nv(l.Fence), ":now": nv(uint64(now.Unix()))}
}

const leaseCondition = "leaseHolder = :holder AND leaseFence = :fence AND leaseExpiresAt > :now"

func validLease(l Lease, now time.Time) bool {
	return l.Holder != "" && l.Fence > 0 && now.Unix() > 0 && now.Unix() < l.ExpiresAt
}
func (s *Store) Load(ctx context.Context, l Lease, now time.Time) (Record, error) {
	if !validLease(l, now) {
		return Record{}, ErrConflict
	}
	item, e := s.read(ctx)
	if e != nil {
		return Record{}, e
	}
	holder, ok := item["leaseHolder"].(*types.AttributeValueMemberS)
	fence, fe := number(item, "leaseFence")
	expiry, ee := number(item, "leaseExpiresAt")
	if !ok || holder.Value != l.Holder || fe != nil || ee != nil || fence != l.Fence || expiry <= uint64(now.Unix()) {
		return Record{}, ErrConflict
	}
	version, e := number(item, "tokenVersion")
	if e != nil || version == 0 {
		return Record{}, ErrInvalid
	}
	b, ok := item["encrypted"].(*types.AttributeValueMemberB)
	if !ok {
		return Record{}, ErrInvalid
	}
	var encrypted credential.Value
	if json.Unmarshal(b.Value, &encrypted) != nil {
		return Record{}, ErrInvalid
	}
	rotated := false
	plain, e := s.keys.Open(ctx, s.binding(), encrypted, version, func(ctx context.Context, v uint64, replacement credential.Value) error {
		blob, e := json.Marshal(replacement)
		if e != nil {
			return ErrInvalid
		}
		e = s.replace(ctx, l, v, &types.AttributeValueMemberB{Value: blob}, now)
		if e == nil {
			rotated = true
		}
		return e
	})
	if e != nil {
		return Record{}, e
	}
	defer clear(plain)
	var r Record
	if json.Unmarshal(plain, &r) != nil {
		return Record{}, ErrInvalid
	}
	enrollment, e := number(item, "enrollmentVersion")
	if e != nil || enrollment == 0 || enrollment > math.MaxInt64 || r.Bootstrap.EnrollmentVersion != int64(enrollment) {
		return Record{}, ErrInvalid
	}
	r.enrollment = int64(enrollment)
	r.TokenVersion = version
	r.generation = encrypted.KeyGeneration
	if rotated {
		r.TokenVersion++
		r.generation++
	}
	return r, nil
}
func (s *Store) replace(ctx context.Context, l Lease, version uint64, encrypted types.AttributeValue, now time.Time) error {
	if version == 0 || version == math.MaxUint64 || !validLease(l, now) {
		return ErrConflict
	}
	values := leaseValues(l, now)
	values[":version"] = nv(version)
	values[":next"] = nv(version + 1)
	values[":encrypted"] = encrypted
	_, e := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(s.table), Key: s.key(), UpdateExpression: aws.String("SET encrypted = :encrypted, tokenVersion = :next"), ConditionExpression: aws.String(leaseCondition + " AND tokenVersion = :version"), ExpressionAttributeValues: values})
	if e != nil {
		return storageError(e)
	}
	return nil
}
func (s *Store) Save(ctx context.Context, l Lease, r Record, now time.Time) (Record, error) {
	if r.enrollment <= 0 || r.Bootstrap.EnrollmentVersion != r.enrollment {
		return Record{}, ErrInvalid
	}
	// Keep the enrollment envelope consistent with the authoritative rotated grant.
	r.Bootstrap.RefreshToken = r.Grant.RefreshToken
	r.Bootstrap.RefreshObtainedAt = r.Grant.RefreshObtainedAt
	r.Bootstrap.B2CSubject = r.Grant.Subject
	r.Bootstrap.ProviderState = r.ProviderState
	encrypted, e := s.encrypt(r, r.generation)
	if e != nil {
		return Record{}, e
	}
	if e = s.replace(ctx, l, r.TokenVersion, encrypted, now); e != nil {
		return Record{}, e
	}
	r.TokenVersion++
	return r, nil
}
func (s *Store) Release(ctx context.Context, l Lease) error {
	if l.Holder == "" || l.Fence == 0 {
		return ErrInvalid
	}
	_, e := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(s.table), Key: s.key(), UpdateExpression: aws.String("SET leaseExpiresAt = :zero REMOVE leaseHolder"), ConditionExpression: aws.String("leaseHolder = :holder AND leaseFence = :fence"), ExpressionAttributeValues: map[string]types.AttributeValue{":holder": sv(l.Holder), ":fence": nv(l.Fence), ":zero": nv(0)}})
	if e != nil {
		return storageError(e)
	}
	return nil
}
