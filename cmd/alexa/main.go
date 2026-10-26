package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/alexa"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
)

var Version string
var Built string

func main() {
	skill, function := os.Getenv("ALEXA_SKILL_ID"), os.Getenv("WORKER_FUNCTION_NAME")
	if skill == "" || function == "" {
		log.Fatal("Alexa configuration invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }), config.WithHTTPClient(&http.Client{Timeout: 4500 * time.Millisecond}))
	cancel()
	if err != nil {
		log.Fatal("Alexa AWS configuration invalid")
	}
	handler := alexa.Handler{SkillID: skill, Broker: alexa.LambdaBroker{Client: awslambda.NewFromConfig(cfg), Function: function}, Now: time.Now}
	lambda.Start(handler.Handle)
}
