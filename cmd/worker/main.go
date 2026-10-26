package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/amazon"
	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/JeremyProffittOrg/suburban-remote/internal/locationmap"
	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/JeremyProffittOrg/suburban-remote/internal/worker"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

var Version string
var Built string

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }), config.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))
	if err != nil {
		cancel()
		log.Fatal("worker configuration invalid")
	}
	err = locationmap.LoadGeofences(ctx, ssm.NewFromConfig(cfg), os.Getenv("GEOFENCE_PARAMETER"))
	cancel()
	if err != nil {
		log.Fatal("worker geofences unavailable")
	}
	db := dynamodb.NewFromConfig(cfg)
	keys, err := state.KeysFromEnvironment()
	if err != nil {
		log.Fatal("worker encryption configuration invalid")
	}
	owner := os.Getenv("OWNER_AMAZON_USER_ID")
	table := os.Getenv("STATE_TABLE")
	store, err := state.New(db, table, owner, keys)
	if err != nil {
		log.Fatal("worker credential configuration invalid")
	}
	documents, err := auth.NewDocuments(db, table, owner, keys)
	if err != nil {
		log.Fatal("worker command storage invalid")
	}
	identity, err := amazon.NewClient(os.Getenv("LWA_CLIENT_ID"), "", owner, 2*time.Second)
	if err != nil {
		log.Fatal("worker owner identity configuration invalid")
	}
	provider := onstar.NewClient()
	families := &auth.MobileService{Store: documents, Owner: owner, Now: time.Now}
	commandService := &commands.Service{Config: commands.Config{SkillID: os.Getenv("ALEXA_SKILL_ID"), Owner: owner, DeviceID: os.Getenv("ONSTAR_DEVICE_ID"), VehicleKey: os.Getenv("VEHICLE_KEY"), QueueARN: os.Getenv("WORK_QUEUE_ARN"), DryRunAllowed: os.Getenv("COMMAND_DRY_RUN") == "true"}, Docs: documents, Authority: identity, Queue: commands.FIFO{Client: sqs.NewFromConfig(cfg), URL: os.Getenv("WORK_QUEUE_URL"), VehicleKey: os.Getenv("VEHICLE_KEY")}, Credentials: store, Provider: provider, Flags: commands.FlagStore{DB: db, Table: table, Owner: owner}, Families: families, Now: time.Now}
	enrollment, err := strconv.ParseInt(os.Getenv("ENROLLMENT_VERSION"), 10, 64)
	if err != nil || enrollment <= 0 || os.Getenv("ONSTAR_DEVICE_ID") == "" || os.Getenv("DEPLOY_NONCE") == "" {
		log.Fatal("worker binding configuration invalid")
	}
	w := worker.Worker{Store: store, Provider: provider, DeviceID: os.Getenv("ONSTAR_DEVICE_ID"), EnrollmentVersion: enrollment, DeployNonce: os.Getenv("DEPLOY_NONCE"), Version: Version, Now: time.Now}
	maps := locationmap.Service{Credentials: store, Provider: provider, Docs: documents, Owner: owner, DeviceID: os.Getenv("ONSTAR_DEVICE_ID"), VehicleKey: os.Getenv("VEHICLE_KEY"), QueueARN: os.Getenv("WORK_QUEUE_ARN"), ScheduleARN: os.Getenv("LOCATION_SCHEDULE_ARN"), Families: families, Authority: identity, Queue: locationmap.FIFO{Client: sqs.NewFromConfig(cfg), URL: os.Getenv("WORK_QUEUE_URL"), VehicleKey: os.Getenv("VEHICLE_KEY")}, RequestPaused: func(ctx context.Context) (bool, error) {
		flags, e := commandService.Flags.ReadFlags(ctx)
		return flags.ProviderPaused, e
	}, Paused: func(ctx context.Context) (bool, error) {
		if os.Getenv("LOCATION_POLLING_ENABLED") != "true" {
			return true, nil
		}
		flags, e := commandService.Flags.ReadFlags(ctx)
		return flags.ProviderPaused, e
	}}
	var invoked atomic.Bool
	lambda.Start(func(ctx context.Context, input json.RawMessage) (any, error) {
		cold := !invoked.Swap(true)
		var envelope struct {
			Kind    string            `json:"kind"`
			Records []json.RawMessage `json:"Records"`
		}
		if json.Unmarshal(input, &envelope) != nil {
			return nil, errors.New("invalid worker request")
		}
		if envelope.Records != nil {
			var batch events.SQSEvent
			if json.Unmarshal(input, &batch) != nil {
				return nil, nil
			}
			for _, message := range batch.Records {
				var pointer struct {
					Kind string `json:"kind"`
				}
				if json.Unmarshal([]byte(message.Body), &pointer) == nil && pointer.Kind == "location-refresh" {
					if err := maps.HandleRefresh(ctx, message); err != nil {
						return nil, errors.New("location refresh persistence unavailable")
					}
					continue
				}
				if json.Unmarshal([]byte(message.Body), &pointer) == nil && pointer.Kind == "location" {
					if err := maps.Handle(ctx, message); err != nil {
						return nil, errors.New("location persistence unavailable")
					}
					continue
				}
				if err := commandService.Dispatch(ctx, message); err != nil {
					return nil, errors.New("command persistence unavailable")
				}
			}
			return struct{}{}, nil
		}
		if envelope.Kind == "alexa-command" {
			var request contract.AlexaCommandRequest
			if json.Unmarshal(input, &request) != nil {
				return contract.AlexaCommandResponse{State: "rejected", Reason: "invalid-request"}, nil
			}
			brokerCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			return commandService.Broker(brokerCtx, request), nil
		}
		if envelope.Kind == "location-refresh-request" {
			var request contract.LocationRefreshRequest
			if json.Unmarshal(input, &request) != nil {
				return map[string]string{"error": "invalid-request"}, nil
			}
			result, e := maps.RequestRefresh(ctx, request.FamilyID, request.RequestID)
			reason := ""
			if e != nil {
				switch {
				case errors.Is(e, auth.ErrInvalid):
					reason = "invalid-request"
				case errors.Is(e, auth.ErrHandle), errors.Is(e, auth.ErrNotOwner):
					reason = "session-unavailable"
				case errors.Is(e, auth.ErrRate):
					reason = "rate-limited"
				case errors.Is(e, auth.ErrConflict):
					reason = "conflict"
				case errors.Is(e, auth.ErrExpired):
					reason = "expired"
				default:
					reason = "unavailable"
				}
			}
			return struct {
				Status contract.LocationRefreshStatus `json:"status"`
				Error  string                         `json:"error,omitempty"`
			}{result, reason}, nil
		}
		if envelope.Kind == "location-map-status" {
			return maps.Status(ctx)
		}
		var proof worker.Request
		if json.Unmarshal(input, &proof) != nil {
			return nil, errors.New("invalid proof request")
		}
		requestID := ""
		if lc, ok := lambdacontext.FromContext(ctx); ok {
			requestID = lc.AwsRequestID
		}
		result := w.Run(ctx, proof, requestID, cold)
		result.LogStream = os.Getenv("AWS_LAMBDA_LOG_STREAM_NAME")
		return result, nil
	})
}
