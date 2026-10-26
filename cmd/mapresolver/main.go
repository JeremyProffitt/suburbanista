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
	"github.com/JeremyProffittOrg/suburban-remote/internal/locationmap"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }), config.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))
	if err != nil {
		log.Fatal("map resolver configuration invalid")
	}
	if locationmap.LoadGeofences(ctx, ssm.NewFromConfig(cfg), os.Getenv("GEOFENCE_PARAMETER")) != nil {
		log.Fatal("map resolver geofences unavailable")
	}
	keys, err := state.KeysFromEnvironment()
	if err != nil {
		log.Fatal("map resolver encryption invalid")
	}
	docs, err := auth.NewDocuments(dynamodb.NewFromConfig(cfg), os.Getenv("STATE_TABLE"), os.Getenv("OWNER_AMAZON_USER_ID"), keys)
	if err != nil {
		log.Fatal("map resolver storage invalid")
	}
	style := locationmap.DefaultRenderConfig()
	resolver := locationmap.Resolver{Docs: docs, Objects: locationmap.S3Objects{Config: cfg, Bucket: os.Getenv("MAP_BUCKET")}, Owner: os.Getenv("OWNER_AMAZON_USER_ID"), Vehicle: os.Getenv("VEHICLE_KEY"), Config: style, Render: (locationmap.Renderer{Config: cfg, Style: style}).Render}
	lambda.Start(func(ctx context.Context, input json.RawMessage) (any, error) {
		var replay struct {
			Kind        string `json:"kind"`
			Observation string `json:"observation"`
		}
		if json.Unmarshal(input, &replay) != nil {
			return nil, errors.New("invalid map request")
		}
		if replay.Kind == "location-map-render-stored" {
			return resolver.ResolveCurrent(ctx)
		}
		if replay.Kind == "replay-observation" {
			if err := resolver.Resolve(ctx, replay.Observation); err != nil {
				return nil, errors.New("map resolution failed")
			}
			return map[string]string{"status": "ready"}, nil
		}
		var batch events.DynamoDBEvent
		if json.Unmarshal(input, &batch) != nil {
			return nil, errors.New("invalid map batch")
		}
		return resolver.Handle(ctx, batch)
	})
}
