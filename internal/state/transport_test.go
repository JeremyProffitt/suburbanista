package state

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func TestAWSFailureHasNoRetryOrRawResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"__type":"InternalServerError","message":"synthetic-sensitive-provider-response"}`))
	}))
	defer server.Close()
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", BaseEndpoint: aws.String(server.URL), Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""), Retryer: aws.NopRetryer{}, HTTPClient: server.Client()})
	store, _, _ := testStore(t)
	store.db = db
	_, e := store.read(context.Background())
	if e != ErrStorage || calls.Load() != 1 {
		t.Fatal("AWS failure leaked details or retried")
	}
}
