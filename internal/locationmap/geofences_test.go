package locationmap

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func useGeofences(t *testing.T, config GeofenceConfig) {
	t.Helper()
	if err := SetGeofences(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { placeConfig.Store(nil) })
}

type parameterStub struct {
	value string
	err   error
	input *ssm.GetParameterInput
}

func (p *parameterStub) GetParameter(_ context.Context, input *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	p.input = input
	if p.err != nil {
		return nil, p.err
	}
	return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String(p.value)}}, nil
}

func TestLoadGeofencesReadsDecryptedParameter(t *testing.T) {
	t.Cleanup(func() { placeConfig.Store(nil) })
	stub := &parameterStub{value: `{"fences":[{"name":"Home","latitude":0,"longitude":0,"radiusMeters":30.48},{"name":"Work","latitude":0,"longitude":0,"radiusMeters":60.96}]}`}
	if err := LoadGeofences(context.Background(), stub, "suburbanista-geo"); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(stub.input.Name) != "suburbanista-geo" || !aws.ToBool(stub.input.WithDecryption) {
		t.Fatal("parameter read was not a decrypted read of the named parameter")
	}
	if got := currentGeofences(); len(got.Fences) != 2 || got.Fences[0].Name != "Home" || got.Fences[1].RadiusMeters != 60.96 {
		t.Fatal("loaded centers differ")
	}
}

func TestLoadGeofencesRejectsInvalidParameters(t *testing.T) {
	t.Cleanup(func() { placeConfig.Store(nil) })
	for _, stub := range []*parameterStub{
		{err: errors.New("denied")},
		{value: `not json`},
		{value: `{"fences":[{"name":"Home","latitude":0,"longitude":0,"radiusMeters":30.48}]}`},
		{value: `{"fences":[{"name":"Home","latitude":0,"longitude":0,"radiusMeters":30.48},{"name":"Office","latitude":0,"longitude":0,"radiusMeters":60.96}]}`},
		{value: `{"fences":[{"name":"Home","latitude":0,"longitude":0,"radiusMeters":99},{"name":"Work","latitude":0,"longitude":0,"radiusMeters":60.96}]}`},
		{value: `{"fences":[],"extra":true}`},
	} {
		if LoadGeofences(context.Background(), stub, "suburbanista-geo") == nil {
			t.Fatal("invalid parameter accepted")
		}
	}
	if LoadGeofences(context.Background(), &parameterStub{}, "") == nil || len(currentGeofences().Fences) != 0 {
		t.Fatal("missing parameter name accepted or partial config installed")
	}
}
