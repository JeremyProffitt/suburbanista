package alexa

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

type LambdaInvoker interface {
	Invoke(context.Context, *awslambda.InvokeInput, ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}
type LambdaBroker struct {
	Client   LambdaInvoker
	Function string
}

func (b LambdaBroker) Call(ctx context.Context, request contract.AlexaCommandRequest) (contract.AlexaCommandResponse, error) {
	var result contract.AlexaCommandResponse
	if b.Client == nil || b.Function == "" {
		return result, errors.New("command broker unavailable")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return result, errors.New("command broker request invalid")
	}
	defer clear(payload)
	response, err := b.Client.Invoke(ctx, &awslambda.InvokeInput{FunctionName: aws.String(b.Function), InvocationType: types.InvocationTypeRequestResponse, Payload: payload})
	if err != nil || response == nil || response.FunctionError != nil || response.StatusCode != 200 {
		return result, errors.New("command broker invocation failed")
	}
	if len(response.Payload) > 16*1024 || json.Unmarshal(response.Payload, &result) != nil || result.State == "" {
		return result, errors.New("command broker response invalid")
	}
	return result, nil
}
