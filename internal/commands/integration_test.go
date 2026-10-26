//go:build integration

package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/credential"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestCommandDynamoDB(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_LOCAL_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
	if endpoint != "http://localhost:8000" && endpoint != "http://127.0.0.1:8000" {
		t.Fatal("loopback DynamoDB Local required")
	}
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), Credentials: awscredentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""), Retryer: aws.NopRetryer{}, HTTPClient: &http.Client{Timeout: 5 * time.Second}})
	table := fmt.Sprintf("commands-conformance-%d", time.Now().UnixNano())
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
	store, e := auth.NewDocuments(db, table, "synthetic-owner", keys)
	if e != nil {
		t.Fatal(e)
	}
	commandConformance(t, store)
	flags, e := (FlagStore{DB: db, Table: table, Owner: "synthetic-owner"}).ReadFlags(ctx)
	if e != nil || flags.ActuationEnabled || !flags.ProviderPaused {
		t.Fatal("missing durable flags did not fail closed")
	}
}
