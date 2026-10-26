//go:build integration

package auth

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/credential"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestMobileDynamoDB(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_LOCAL_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
	if endpoint != "http://localhost:8000" && endpoint != "http://127.0.0.1:8000" {
		t.Fatal("loopback DynamoDB Local required")
	}
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""), Retryer: aws.NopRetryer{}, HTTPClient: &http.Client{Timeout: 5 * time.Second}})
	table := fmt.Sprintf("mobile-conformance-%d", time.Now().UnixNano())
	ctx := context.Background()
	_, e := db.CreateTable(ctx, &dynamodb.CreateTableInput{TableName: aws.String(table), BillingMode: types.BillingModePayPerRequest, AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS}}, KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange}}})
	if e != nil {
		t.Fatal("DynamoDB Local table creation failed")
	}
	t.Cleanup(func() {
		if _, e := db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}); e != nil {
			t.Error("DynamoDB Local cleanup failed")
		}
	})
	keys, _ := credential.New(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), credential.SlotA)
	store, e := NewDocuments(db, table, "synthetic-owner", keys)
	if e != nil {
		t.Fatal(e)
	}
	mobileConformance(t, store)
	mcpOAuthConformance(t, store)
	// Confirm a failed multi-item write does not persist its other new item.
	expiry := time.Now().Add(time.Hour)
	d := withData(Document{Key: "conflict", ExpiresAt: expiry}, "synthetic")
	if e = store.Commit(ctx, d); e != nil {
		t.Fatal(e)
	}
	queried, queryErr := store.QueryDocument(ctx, "conflict")
	if queryErr != nil || !bytes.Equal(queried.Data, d.Data) || queried.Version != 1 {
		t.Fatal("bounded encrypted document Query failed")
	}
	if _, queryErr = store.QueryDocument(ctx, "missing-index"); queryErr != ErrMissing {
		t.Fatal("missing bounded index Query not reported")
	}
	if e = store.Commit(ctx, d, withData(Document{Key: "not-written", ExpiresAt: expiry}, "synthetic")); !errorsIsConflict(e) {
		t.Fatal("transaction conflict accepted")
	}
	if _, e = store.Load(ctx, "not-written"); e != ErrMissing {
		t.Fatal("partial transaction persisted")
	}
}
func errorsIsConflict(e error) bool { return e == ErrConflict }
