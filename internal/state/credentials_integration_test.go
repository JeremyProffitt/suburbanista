//go:build integration

package state

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestCredentialConformanceDynamoDB(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_LOCAL_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
	// These synthetic credentials must never be sent to a real AWS endpoint.
	if endpoint != "http://localhost:8000" && endpoint != "http://127.0.0.1:8000" {
		t.Fatal("DynamoDB Local loopback endpoint required")
	}
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""), Retryer: aws.NopRetryer{}, HTTPClient: &http.Client{Timeout: 5 * time.Second}})
	table := fmt.Sprintf("credential-conformance-%d", time.Now().UnixNano())
	ctx := context.Background()
	_, e := db.CreateTable(ctx, &dynamodb.CreateTableInput{TableName: aws.String(table), BillingMode: types.BillingModePayPerRequest, AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS}}, KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange}}})
	if e != nil {
		t.Fatal("DynamoDB Local CreateTable failed")
	}
	t.Cleanup(func() {
		if _, e := db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}); e != nil {
			t.Error("DynamoDB Local cleanup failed")
		}
	})
	s, _, b := testStore(t)
	s.db = db
	s.table = table
	credentialConformance(t, s, b)
}
