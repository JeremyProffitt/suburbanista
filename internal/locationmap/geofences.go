package locationmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Home and Work centers live only in the SecureString SSM parameter named by
// GEOFENCE_PARAMETER (suburbanista-geo). Each Lambda loads them once at cold
// start; source code, logs, and results never carry the coordinates.
var placeConfig atomic.Pointer[GeofenceConfig]

type ParameterReader interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// LoadGeofences reads, strictly decodes, and installs the place centers.
func LoadGeofences(ctx context.Context, client ParameterReader, name string) error {
	if client == nil || name == "" {
		return errors.New("geofence parameter unavailable")
	}
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(true)})
	if err != nil || out == nil || out.Parameter == nil || out.Parameter.Value == nil {
		return errors.New("geofence parameter unavailable")
	}
	var config GeofenceConfig
	decoder := json.NewDecoder(bytes.NewReader([]byte(*out.Parameter.Value)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid geofence parameter")
	}
	return SetGeofences(config)
}

// SetGeofences installs exactly one Home and one Work center.
func SetGeofences(config GeofenceConfig) error {
	if len(config.Fences) != 2 {
		return errors.New("invalid geofence parameter")
	}
	if _, _, err := GeofencedFix(onstar.LocationFix{}, config); err != nil {
		return errors.New("invalid geofence parameter")
	}
	placeConfig.Store(&config)
	return nil
}

func currentGeofences() GeofenceConfig {
	if config := placeConfig.Load(); config != nil {
		return *config
	}
	return GeofenceConfig{}
}
