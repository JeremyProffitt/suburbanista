package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/JeremyProffittOrg/suburban-remote/internal/locationmap"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/web"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	lambdaapi "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"log"
	"net/http"
	"os"
	"time"
)

// Version and Built are set by the release build's linker flags.
var Version string
var Built string

func main() {
	if !canaryValid(os.Getenv("DEPLOYMENT_CANARY"), os.Getenv("CANARY_SHA256")) {
		log.Fatal("deployment selftest: failed")
	}
	log.Print("deployment selftest: passed")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }), config.WithHTTPClient(&http.Client{Timeout: 8 * time.Second}))
	if err != nil {
		log.Fatal("API state configuration invalid")
	}
	if locationmap.LoadGeofences(ctx, ssm.NewFromConfig(cfg), os.Getenv("GEOFENCE_PARAMETER")) != nil {
		log.Fatal("API geofences unavailable")
	}
	db := dynamodb.NewFromConfig(cfg)
	keys, err := state.KeysFromEnvironment()
	if err != nil {
		log.Fatal("API encryption configuration invalid")
	}
	owner := os.Getenv("OWNER_AMAZON_USER_ID")
	documents, err := auth.NewDocuments(db, os.Getenv("STATE_TABLE"), owner, keys)
	if err != nil {
		log.Fatal("API session configuration invalid")
	}
	provider, err := state.New(db, os.Getenv("STATE_TABLE"), owner, keys)
	if err != nil {
		log.Fatal("API provider state configuration invalid")
	}
	identity, err := amazon.NewClient(os.Getenv("LWA_CLIENT_ID"), os.Getenv("LWA_CLIENT_SECRET"), owner, 3*time.Second)
	if err != nil {
		log.Fatal("API identity configuration invalid")
	}
	service := &auth.MobileService{Store: documents, Amazon: identity, Owner: owner, Now: time.Now}
	if web.ValidateMapDigest(os.Getenv("MAP_READ_KEY_SHA256")) != nil {
		log.Fatal("API map configuration invalid")
	}
	adapter, err := web.New(web.Build{Version: Version, Built: Built}, web.Services{Mobile: service, Provider: provider, Flags: commands.FlagStore{DB: db, Table: os.Getenv("STATE_TABLE"), Owner: owner}, Commands: web.IAMCommandBroker{Client: lambdaapi.NewFromConfig(cfg), Function: os.Getenv("WORKER_FUNCTION_NAME")}, Releases: web.S3Releases{Bucket: os.Getenv("RELEASE_BUCKET"), Credentials: cfg.Credentials}, Map: web.S3Map{Bucket: os.Getenv("MAP_BUCKET"), Credentials: cfg.Credentials, Documents: documents, Demand: func(ctx context.Context) error { return locationmap.RequestMap(ctx, documents, time.Now().UTC()) }}, PhoneLocations: func(ctx context.Context, family string, sample locationmap.PhoneSample) (locationmap.PhoneResult, error) {
		return locationmap.RecordPhone(ctx, documents, family, sample, time.Now().UTC())
	}, MapKeySHA256: os.Getenv("MAP_READ_KEY_SHA256"), LocationRequests: web.IAMLocationRequests{Client: lambdaapi.NewFromConfig(cfg), Function: os.Getenv("WORKER_FUNCTION_NAME"), Owner: owner, Documents: documents}})
	if err != nil {
		log.Fatal("API cannot start: invalid build metadata")
	}
	lambda.Start(adapter.Handle)
}

func canaryValid(value, expected string) bool {
	if value == "" || len(expected) != 64 {
		return false
	}
	actual := sha256.Sum256([]byte(value))
	return hex.EncodeToString(actual[:]) == expected
}
