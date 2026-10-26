package commands

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type sender interface {
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}
type FIFO struct {
	Client          sender
	URL, VehicleKey string
}

func (q FIFO) Send(ctx context.Context, body, id string) error {
	if q.Client == nil || q.URL == "" || q.VehicleKey == "" {
		return errors.New("queue unavailable")
	}
	response, e := q.Client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(q.URL), MessageBody: aws.String(body), MessageGroupId: aws.String("cmd-" + q.VehicleKey), MessageDeduplicationId: aws.String(id)})
	if e != nil || response == nil || aws.ToString(response.MessageId) == "" {
		return errors.New("queue delivery unconfirmed")
	}
	return nil
}
