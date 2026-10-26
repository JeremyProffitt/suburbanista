package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/credential"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var ErrMissing = errors.New("not-found")
var ErrConflict = errors.New("session-conflict")
var ErrUnavailable = errors.New("session-store-unavailable")

type Document struct {
	Key        string
	Version    uint64
	ExpiresAt  time.Time
	Data       json.RawMessage
	generation uint64
}
type Documents interface {
	Load(context.Context, string) (Document, error)
	Commit(context.Context, ...Document) error
}

// QueryDocument performs a bounded key query for an encrypted index document.
// The document format and decryption/rotation path remain identical to Load.
func (s *DocumentStore) QueryDocument(ctx context.Context, key string) (Document, error) {
	q, ok := s.db.(interface {
		Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	})
	if !ok {
		return Document{}, ErrUnavailable
	}
	result, err := q.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(s.table), ConsistentRead: aws.Bool(true), KeyConditionExpression: aws.String("pk = :pk AND sk = :sk"), ExpressionAttributeValues: map[string]types.AttributeValue{":pk": &types.AttributeValueMemberS{Value: "auth#" + key}, ":sk": &types.AttributeValueMemberS{Value: "v1"}}, ProjectionExpression: aws.String("pk"), Limit: aws.Int32(1)})
	if err != nil {
		return Document{}, ErrUnavailable
	}
	if len(result.Items) == 0 {
		return Document{}, ErrMissing
	}
	return s.Load(ctx, key)
}

type documentDB interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}
type DocumentStore struct {
	db           documentDB
	table, owner string
	keys         *credential.Keys
}

func NewDocuments(db documentDB, table, owner string, keys *credential.Keys) (*DocumentStore, error) {
	if db == nil || table == "" || owner == "" || keys == nil {
		return nil, ErrUnavailable
	}
	return &DocumentStore{db, table, owner, keys}, nil
}
func (s *DocumentStore) binding(key string) credential.Binding {
	return credential.Binding{Owner: s.owner, Provider: "lwa", ItemKey: key}
}
func documentKey(key string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: "auth#" + key}, "sk": &types.AttributeValueMemberS{Value: "v1"}}
}
func numeric(v uint64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatUint(v, 10)}
}
func integer(m map[string]types.AttributeValue, name string) (uint64, error) {
	v, ok := m[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0, ErrUnavailable
	}
	n, e := strconv.ParseUint(v.Value, 10, 64)
	if e != nil {
		return 0, ErrUnavailable
	}
	return n, nil
}
func documentError(e error) error {
	var cancel *types.TransactionCanceledException
	if errors.As(e, &cancel) {
		for _, r := range cancel.CancellationReasons {
			if aws.ToString(r.Code) == "ConditionalCheckFailed" {
				return ErrConflict
			}
		}
	}
	return ErrUnavailable
}
func (s *DocumentStore) Load(ctx context.Context, key string) (Document, error) {
	result, e := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: documentKey(key), ConsistentRead: aws.Bool(true)})
	if e != nil {
		return Document{}, ErrUnavailable
	}
	if len(result.Item) == 0 {
		return Document{}, ErrMissing
	}
	version, e := integer(result.Item, "version")
	if e != nil || version == 0 {
		return Document{}, ErrUnavailable
	}
	schema, e := integer(result.Item, "schemaVersion")
	if e != nil || schema != 1 {
		return Document{}, ErrUnavailable
	}
	expiry, e := integer(result.Item, "expiresAt")
	if e != nil {
		return Document{}, ErrUnavailable
	}
	blob, ok := result.Item["encrypted"].(*types.AttributeValueMemberB)
	if !ok {
		return Document{}, ErrUnavailable
	}
	var encrypted credential.Value
	if json.Unmarshal(blob.Value, &encrypted) != nil {
		return Document{}, ErrUnavailable
	}
	doc := Document{Key: key, Version: version, ExpiresAt: time.Unix(int64(expiry), 0), generation: encrypted.KeyGeneration}
	data, e := s.keys.Open(ctx, s.binding(key), encrypted, version, func(ctx context.Context, v uint64, replacement credential.Value) error {
		b, e := json.Marshal(replacement)
		if e != nil {
			return ErrUnavailable
		}
		_, e = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{{Update: &types.Update{TableName: aws.String(s.table), Key: documentKey(key), UpdateExpression: aws.String("SET encrypted = :value, #v = :next"), ConditionExpression: aws.String("#v = :old"), ExpressionAttributeNames: map[string]string{"#v": "version"}, ExpressionAttributeValues: map[string]types.AttributeValue{":value": &types.AttributeValueMemberB{Value: b}, ":old": numeric(v), ":next": numeric(v + 1)}}}}})
		if e != nil {
			return documentError(e)
		}
		doc.Version++
		doc.generation++
		return nil
	})
	if e != nil {
		return Document{}, ErrUnavailable
	}
	doc.Data = data
	return doc, nil
}
func (s *DocumentStore) Commit(ctx context.Context, docs ...Document) error {
	if len(docs) == 0 || len(docs) > 25 {
		return ErrUnavailable
	}
	items := make([]types.TransactWriteItem, 0, len(docs))
	seen := map[string]bool{}
	for _, doc := range docs {
		if doc.Key == "" || seen[doc.Key] || doc.ExpiresAt.Unix() <= 0 || doc.Version == ^uint64(0) {
			return ErrUnavailable
		}
		seen[doc.Key] = true
		generation := doc.generation
		if generation == 0 {
			generation = 1
		}
		encrypted, e := s.keys.Encrypt(s.binding(doc.Key), doc.Data, generation)
		if e != nil {
			return ErrUnavailable
		}
		blob, e := json.Marshal(encrypted)
		if e != nil {
			return ErrUnavailable
		}
		item := documentKey(doc.Key)
		item["schemaVersion"] = numeric(1)
		item["version"] = numeric(doc.Version + 1)
		item["expiresAt"] = numeric(uint64(doc.ExpiresAt.Unix()))
		item["encrypted"] = &types.AttributeValueMemberB{Value: blob}
		put := &types.Put{TableName: aws.String(s.table), Item: item, ConditionExpression: aws.String("attribute_not_exists(pk)")}
		if doc.Version > 0 {
			put.ConditionExpression = aws.String("#v = :old")
			put.ExpressionAttributeNames = map[string]string{"#v": "version"}
			put.ExpressionAttributeValues = map[string]types.AttributeValue{":old": numeric(doc.Version)}
		}
		items = append(items, types.TransactWriteItem{Put: put})
	}
	_, e := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if e != nil {
		return documentError(e)
	}
	return nil
}
func withData(doc Document, value any) Document { doc.Data, _ = json.Marshal(value); return doc }
