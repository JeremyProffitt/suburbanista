package state

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/credential"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func KeysFromEnvironment() (*credential.Keys, error) {
	a, e := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_ENCRYPTION_KEY_A"))
	if e != nil {
		return nil, credential.ErrConfiguration
	}
	defer clear(a)
	b, e := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_ENCRYPTION_KEY_B"))
	if e != nil {
		return nil, credential.ErrConfiguration
	}
	defer clear(b)
	return credential.New(a, b, credential.Slot(os.Getenv("CREDENTIAL_ENCRYPTION_KEY_ACTIVE")))
}

// LoadAWS performs no service request. The caller owns retries and deadlines.
func LoadAWS(ctx context.Context) (*dynamodb.Client, error) {
	cfg, e := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }), config.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))
	if e != nil {
		return nil, ErrStorage
	}
	return dynamodb.NewFromConfig(cfg), nil
}
func NewFromEnvironment(ctx context.Context) (*Store, error) {
	keys, e := KeysFromEnvironment()
	if e != nil {
		return nil, e
	}
	db, e := LoadAWS(ctx)
	if e != nil {
		return nil, e
	}
	return New(db, os.Getenv("STATE_TABLE"), os.Getenv("OWNER_AMAZON_USER_ID"), keys)
}
