// commandflags changes only the owner's durable actuation gates. It never sends
// a vehicle command and is available only to an explicitly dispatched main run.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type settings struct{ Repository, Ref, Event, Actions, Enable, Present, Outdoors, Capabilities, Owner, Table string }

func fromEnvironment() settings {
	return settings{
		Repository: os.Getenv("GITHUB_REPOSITORY"), Ref: os.Getenv("GITHUB_REF"), Event: os.Getenv("GITHUB_EVENT_NAME"), Actions: os.Getenv("GITHUB_ACTIONS"),
		Enable: os.Getenv("ENABLE"), Present: os.Getenv("OWNER_PRESENT"), Outdoors: os.Getenv("OUTDOORS"), Capabilities: os.Getenv("CAPABILITIES_CONFIRMED"), Owner: os.Getenv("OWNER_AMAZON_USER_ID"), Table: os.Getenv("STATE_TABLE"),
	}
}
func (s settings) guard() (bool, error) {
	if s.Repository != "JeremyProffittOrg/Suburban-Remote" || s.Ref != "refs/heads/main" || s.Event != "workflow_dispatch" || s.Actions != "true" {
		return false, errors.New("command controls require an explicit main workflow dispatch")
	}
	if s.Enable != "true" && s.Enable != "false" {
		return false, errors.New("enable must be explicitly true or false")
	}
	enable := s.Enable == "true"
	if enable && (s.Present != "true" || s.Outdoors != "true" || s.Capabilities != "true") {
		return false, errors.New("enable requires owner presence, outdoors, and capability confirmation")
	}
	return enable, nil
}

type flagDB interface {
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}

func parseFlags(item map[string]types.AttributeValue) (commands.Flags, error) {
	var flags commands.Flags
	schema, ok := item["schemaVersion"].(*types.AttributeValueMemberN)
	if !ok || schema.Value != "1" {
		return flags, errors.New("flag schema verification failed")
	}
	fields := []struct {
		name  string
		value *bool
	}{{"actuationEnabled", &flags.ActuationEnabled}, {"providerPaused", &flags.ProviderPaused}, {"startSupported", &flags.StartSupported}, {"lockSupported", &flags.LockSupported}, {"unlockSupported", &flags.UnlockSupported}}
	for _, field := range fields {
		value, ok := item[field.name].(*types.AttributeValueMemberBOOL)
		if !ok {
			return commands.Flags{}, errors.New("flag type verification failed")
		}
		*field.value = value.Value
	}
	return flags, nil
}
func apply(ctx context.Context, s settings, db flagDB) (commands.Flags, error) {
	enable, err := s.guard()
	if err != nil {
		return commands.Flags{}, err
	}
	if db == nil || strings.TrimSpace(s.Owner) == "" || len(s.Owner) > 512 || strings.ContainsAny(s.Owner, "\r\n\t") || strings.TrimSpace(s.Table) == "" {
		return commands.Flags{}, errors.New("owned flag row configuration unavailable")
	}
	key := map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: "owner#" + s.Owner}, "sk": &types.AttributeValueMemberS{Value: "flags"}}
	names := map[string]string{"#schema": "schemaVersion", "#enabled": "actuationEnabled", "#paused": "providerPaused", "#start": "startSupported", "#lock": "lockSupported", "#unlock": "unlockSupported"}
	values := map[string]types.AttributeValue{":schema": &types.AttributeValueMemberN{Value: "1"}, ":enabled": &types.AttributeValueMemberBOOL{Value: enable}, ":paused": &types.AttributeValueMemberBOOL{Value: !enable}, ":supported": &types.AttributeValueMemberBOOL{Value: enable}}
	expression := "SET #schema = if_not_exists(#schema, :schema), #enabled = :enabled, #paused = :paused, "
	if enable {
		expression += "#start = :supported, #lock = :supported, #unlock = :supported"
	} else {
		expression += "#start = if_not_exists(#start, :supported), #lock = if_not_exists(#lock, :supported), #unlock = if_not_exists(#unlock, :supported)"
	}
	updated, err := db.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(s.Table), Key: key, UpdateExpression: aws.String(expression), ConditionExpression: aws.String("attribute_not_exists(#schema) OR #schema = :schema"), ExpressionAttributeNames: names, ExpressionAttributeValues: values, ReturnValues: types.ReturnValueUpdatedNew})
	if err != nil || updated == nil {
		return commands.Flags{}, errors.New("flag update failed; no confirmed result")
	}
	written, err := parseFlags(updated.Attributes)
	if err != nil {
		return commands.Flags{}, err
	}
	if written.ActuationEnabled != enable || written.ProviderPaused == enable || (enable && (!written.StartSupported || !written.LockSupported || !written.UnlockSupported)) {
		return commands.Flags{}, errors.New("flag update verification failed")
	}
	read, err := db.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.Table), Key: key, ConsistentRead: aws.Bool(true), ProjectionExpression: aws.String("#schema, #enabled, #paused, #start, #lock, #unlock"), ExpressionAttributeNames: names})
	if err != nil || read == nil {
		return commands.Flags{}, errors.New("flag readback failed; update may have completed")
	}
	actual, err := parseFlags(read.Item)
	if err != nil {
		return commands.Flags{}, err
	}
	if actual.ActuationEnabled != written.ActuationEnabled || actual.ProviderPaused != written.ProviderPaused || actual.StartSupported != written.StartSupported || actual.LockSupported != written.LockSupported || actual.UnlockSupported != written.UnlockSupported || !maps.Equal(actual.AdditionalActions, written.AdditionalActions) {
		return commands.Flags{}, errors.New("flag readback differs; state changed or was not confirmed")
	}
	return actual, nil
}
func run() error {
	if len(os.Args) != 2 || (os.Args[1] != "check" && os.Args[1] != "apply") {
		return errors.New("usage: commandflags check|apply")
	}
	s := fromEnvironment()
	enable, err := s.guard()
	if err != nil {
		return err
	}
	if os.Args[1] == "check" {
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{"authorized": true, "enable": enable})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"), config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }), config.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if err != nil {
		return errors.New("AWS configuration unavailable")
	}
	flags, err := apply(ctx, s, dynamodb.NewFromConfig(cfg))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]bool{"verified": true, "actuationEnabled": flags.ActuationEnabled, "providerPaused": flags.ProviderPaused, "startSupported": flags.StartSupported, "lockSupported": flags.LockSupported, "unlockSupported": flags.UnlockSupported})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
