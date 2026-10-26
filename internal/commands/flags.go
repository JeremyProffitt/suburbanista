package commands

import (
	"context"
	"errors"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type Flags struct {
	ActuationEnabled, ProviderPaused, StartSupported, LockSupported, UnlockSupported bool
	AdditionalActions                                                                map[string]bool
}
type FlagReader interface {
	ReadFlags(context.Context) (Flags, error)
}
type flagDB interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}
type FlagStore struct {
	DB           flagDB
	Table, Owner string
}

// Missing or malformed flags never enable vehicle operations. This fixed row is
// owner-managed; no constructor, deployment, or request writes enablement flags.
func (s FlagStore) ReadFlags(ctx context.Context) (Flags, error) {
	off := Flags{ProviderPaused: true}
	if s.DB == nil || s.Table == "" || s.Owner == "" {
		return off, errors.New("flags unavailable")
	}
	result, e := s.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.Table), ConsistentRead: aws.Bool(true), Key: map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: "owner#" + s.Owner}, "sk": &types.AttributeValueMemberS{Value: "flags"}}})
	if e != nil {
		return off, errors.New("flags unavailable")
	}
	if len(result.Item) == 0 {
		return off, nil
	}
	schema, ok := result.Item["schemaVersion"].(*types.AttributeValueMemberN)
	if !ok || schema.Value != "1" {
		return off, errors.New("flags invalid")
	}
	values := []bool{}
	for _, key := range []string{"actuationEnabled", "providerPaused", "startSupported", "lockSupported", "unlockSupported"} {
		v, ok := result.Item[key].(*types.AttributeValueMemberBOOL)
		if !ok {
			return off, errors.New("flags invalid")
		}
		values = append(values, v.Value)
	}
	flags := Flags{ActuationEnabled: values[0], ProviderPaused: values[1], StartSupported: values[2], LockSupported: values[3], UnlockSupported: values[4], AdditionalActions: map[string]bool{}}
	if raw, exists := result.Item["additionalActions"]; exists {
		entries, ok := raw.(*types.AttributeValueMemberM)
		if !ok {
			return off, errors.New("flags invalid")
		}
		for id, value := range entries.Value {
			f, known := contract.FindFeature(id)
			v, ok := value.(*types.AttributeValueMemberBOOL)
			if !known || f.Category != "action" || !ok {
				return off, errors.New("flags invalid")
			}
			flags.AdditionalActions[id] = v.Value
		}
	}
	return flags, nil
}
func DisabledReason(flags Flags, action string) string {
	if !flags.ActuationEnabled {
		return "controls-disabled"
	}
	if flags.ProviderPaused {
		return "provider-paused"
	}
	supported := map[string]bool{"start": flags.StartSupported, "lock": flags.LockSupported, "unlock": flags.UnlockSupported}
	value, ok := supported[action]
	if !ok {
		feature, known := contract.FindFeature(action)
		if !known || feature.Category != "action" {
			return "unsupported-action"
		}
		if !flags.AdditionalActions[action] {
			return "owner-enable-required"
		}
		return ""
	}
	if !value {
		return "capability-unverified"
	}
	return ""
}
