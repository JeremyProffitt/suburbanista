package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/telemetryhistory"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

var Version string

type response struct {
	telemetryhistory.Receipt
	Version string `json:"version"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }), config.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))
	cancel()
	if err != nil {
		log.Fatal("hourly telemetry AWS configuration invalid")
	}
	db := dynamodb.NewFromConfig(cfg)
	keys, err := state.KeysFromEnvironment()
	if err != nil {
		log.Fatal("hourly telemetry encryption configuration invalid")
	}
	owner, table := os.Getenv("OWNER_AMAZON_USER_ID"), os.Getenv("STATE_TABLE")
	credentials, err := state.New(db, table, owner, keys)
	if err != nil {
		log.Fatal("hourly telemetry credential configuration invalid")
	}
	docs, err := auth.NewDocuments(db, table, owner, keys)
	if err != nil {
		log.Fatal("hourly telemetry storage configuration invalid")
	}
	flags := commands.FlagStore{DB: db, Table: table, Owner: owner}
	service := &telemetryhistory.Service{Docs: docs, Credentials: credentials, Provider: onstar.NewClient(), Owner: owner, Vehicle: os.Getenv("VEHICLE_KEY"), DeviceID: os.Getenv("ONSTAR_DEVICE_ID"), ScheduleARN: os.Getenv("TELEMETRY_SCHEDULE_ARN"), Paused: func(ctx context.Context) (bool, error) { f, e := flags.ReadFlags(ctx); return f.ProviderPaused, e }}
	if service.Vehicle == "" || service.DeviceID == "" || service.ScheduleARN == "" {
		log.Fatal("hourly telemetry binding configuration invalid")
	}
	lambda.Start(func(ctx context.Context, input json.RawMessage) (response, error) {
		var request telemetryhistory.Request
		if len(input) > 2048 || json.Unmarshal(input, &request) != nil {
			return response{}, errors.New("invalid telemetry request")
		}
		var receipt telemetryhistory.Receipt
		var err error
		if request.Kind == "telemetry-status" {
			receipt, err = service.Latest(ctx)
		} else {
			receipt, err = service.Collect(ctx, request)
		}
		if err != nil {
			return response{}, errors.New("telemetry persistence or request unavailable")
		}
		log.Printf("hourly telemetry state=%s reason=%s readings=%d verified=%t", receipt.State, receipt.Reason, receipt.Readings, receipt.ArchiveVerified)
		return response{receipt, Version}, nil
	})
}
