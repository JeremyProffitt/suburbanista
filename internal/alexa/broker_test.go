package alexa

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

type invoker struct {
	Calls  int
	Output *awslambda.InvokeOutput
	Err    error
	Input  contract.AlexaCommandRequest
}

func (i *invoker) Invoke(_ context.Context, in *awslambda.InvokeInput, _ ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	i.Calls++
	if in.InvocationType != types.InvocationTypeRequestResponse || aws.ToString(in.FunctionName) != "synthetic-worker" {
		return nil, errors.New("wrong invocation")
	}
	json.Unmarshal(in.Payload, &i.Input)
	return i.Output, i.Err
}
func TestIAMBrokerInvocationAndSafeFailure(t *testing.T) {
	client := &invoker{Output: &awslambda.InvokeOutput{StatusCode: 200, Payload: []byte(`{"state":"queued","commandId":"synthetic-command"}`)}}
	b := LambdaBroker{Client: client, Function: "synthetic-worker"}
	req := contract.AlexaCommandRequest{Kind: "alexa-command", Operation: "submit", PersonAccessToken: "synthetic-person-token"}
	out, err := b.Call(context.Background(), req)
	if err != nil || out.State != "queued" || client.Input.PersonAccessToken != req.PersonAccessToken || client.Calls != 1 {
		t.Fatal("IAM broker contract failed")
	}
	client.Output = &awslambda.InvokeOutput{StatusCode: 200, FunctionError: aws.String("Unhandled"), Payload: []byte("synthetic-private-provider-error")}
	if _, err = b.Call(context.Background(), req); err == nil || err.Error() != "command broker invocation failed" || client.Calls != 2 {
		t.Fatal("function failure leaked or retried")
	}
}
