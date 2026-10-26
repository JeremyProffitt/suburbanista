package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func allowed() settings {
	return settings{Repository: "JeremyProffittOrg/Suburban-Remote", Ref: "refs/heads/main", Event: "workflow_dispatch", Actions: "true", Enable: "true", Present: "true", Outdoors: "true", Capabilities: "true", Owner: "synthetic-owner", Table: "synthetic-state"}
}
func attrs(enabled, paused, start, lock, unlock bool) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"schemaVersion": &types.AttributeValueMemberN{Value: "1"}, "actuationEnabled": &types.AttributeValueMemberBOOL{Value: enabled}, "providerPaused": &types.AttributeValueMemberBOOL{Value: paused}, "startSupported": &types.AttributeValueMemberBOOL{Value: start}, "lockSupported": &types.AttributeValueMemberBOOL{Value: lock}, "unlockSupported": &types.AttributeValueMemberBOOL{Value: unlock}}
}

type fakeDB struct {
	t                 *testing.T
	writes, reads     int
	updated, readback map[string]types.AttributeValue
	update            *dynamodb.UpdateItemInput
	fail              bool
}

func (db *fakeDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	db.writes++
	db.update = in
	if db.fail {
		return nil, errors.New("unsafe service details")
	}
	return &dynamodb.UpdateItemOutput{Attributes: db.updated}, nil
}
func (db *fakeDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	db.reads++
	if !aws.ToBool(in.ConsistentRead) || len(in.ExpressionAttributeNames) != 6 || aws.ToString(in.ProjectionExpression) != "#schema, #enabled, #paused, #start, #lock, #unlock" {
		db.t.Fatal("readback must project only the six flag fields")
	}
	if in.Key["pk"].(*types.AttributeValueMemberS).Value != "owner#synthetic-owner" || in.Key["sk"].(*types.AttributeValueMemberS).Value != "flags" {
		db.t.Fatal("wrong owned row")
	}
	return &dynamodb.GetItemOutput{Item: db.readback}, nil
}
func TestEnableRequiresAllAttestationsBeforeAWS(t *testing.T) {
	for _, mutate := range []func(*settings){func(s *settings) { s.Present = "false" }, func(s *settings) { s.Outdoors = "false" }, func(s *settings) { s.Capabilities = "" }, func(s *settings) { s.Ref = "refs/heads/other" }, func(s *settings) { s.Event = "push" }, func(s *settings) { s.Repository = "another/repo" }, func(s *settings) { s.Actions = "false" }, func(s *settings) { s.Enable = "TRUE" }} {
		s := allowed()
		mutate(&s)
		db := &fakeDB{t: t}
		if _, err := apply(context.Background(), s, db); err == nil || db.writes != 0 || db.reads != 0 {
			t.Fatal("invalid enable reached AWS")
		}
	}
}
func TestEnableWritesOwnedRowAndVerifies(t *testing.T) {
	db := &fakeDB{t: t, updated: attrs(true, false, true, true, true), readback: attrs(true, false, true, true, true)}
	got, err := apply(context.Background(), allowed(), db)
	if err != nil || !got.ActuationEnabled || got.ProviderPaused || db.writes != 1 || db.reads != 1 {
		t.Fatal("enable failed")
	}
	in := db.update
	if aws.ToString(in.TableName) != "synthetic-state" || in.Key["pk"].(*types.AttributeValueMemberS).Value != "owner#synthetic-owner" || in.Key["sk"].(*types.AttributeValueMemberS).Value != "flags" {
		t.Fatal("incorrect flag destination")
	}
	if aws.ToString(in.ConditionExpression) != "attribute_not_exists(#schema) OR #schema = :schema" || in.ReturnValues != types.ReturnValueUpdatedNew {
		t.Fatal("atomic schema guard missing")
	}
	if !strings.Contains(aws.ToString(in.UpdateExpression), "#start = :supported") {
		t.Fatal("capabilities not explicitly set")
	}
}
func TestDisablePreservesCapabilitiesWithoutAttestations(t *testing.T) {
	s := allowed()
	s.Enable = "false"
	s.Present = ""
	s.Outdoors = ""
	s.Capabilities = ""
	db := &fakeDB{t: t, updated: attrs(false, true, true, false, true), readback: attrs(false, true, true, false, true)}
	got, err := apply(context.Background(), s, db)
	if err != nil || got.ActuationEnabled || !got.ProviderPaused || !got.StartSupported || got.LockSupported || !got.UnlockSupported {
		t.Fatal("disable changed capabilities")
	}
	for _, field := range []string{"#start", "#lock", "#unlock"} {
		if !strings.Contains(aws.ToString(db.update.UpdateExpression), field+" = if_not_exists("+field+", :supported)") {
			t.Fatal("existing capability not preserved")
		}
	}
	if db.update.ExpressionAttributeValues[":supported"].(*types.AttributeValueMemberBOOL).Value {
		t.Fatal("missing capabilities must default false")
	}
}
func TestReadbackCannotClaimUnverifiedSuccess(t *testing.T) {
	for _, mutate := range []func(map[string]types.AttributeValue){func(m map[string]types.AttributeValue) { m["schemaVersion"] = &types.AttributeValueMemberN{Value: "2"} }, func(m map[string]types.AttributeValue) { delete(m, "unlockSupported") }, func(m map[string]types.AttributeValue) {
		m["actuationEnabled"] = &types.AttributeValueMemberBOOL{Value: false}
	}} {
		read := attrs(true, false, true, true, true)
		mutate(read)
		db := &fakeDB{t: t, updated: attrs(true, false, true, true, true), readback: read}
		if _, err := apply(context.Background(), allowed(), db); err == nil {
			t.Fatal("invalid readback accepted")
		}
	}
	db := &fakeDB{t: t, fail: true}
	_, err := apply(context.Background(), allowed(), db)
	if err == nil || strings.Contains(err.Error(), "unsafe") || db.reads != 0 {
		t.Fatal("write failure leaked or continued")
	}
}
