package sqsconsumer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func TestSentTimestamp(t *testing.T) {
	key := string(sqstypes.MessageSystemAttributeNameSentTimestamp)
	msg := sqstypes.Message{Attributes: map[string]string{key: "1761456000123"}}
	if got := sentTimestamp(msg); !got.Equal(time.UnixMilli(1761456000123)) {
		t.Errorf("sentTimestamp = %v", got)
	}
	for _, attrs := range []map[string]string{nil, {key: ""}, {key: "abc"}, {key: "-5"}} {
		if got := sentTimestamp(sqstypes.Message{Attributes: attrs}); !got.IsZero() {
			t.Errorf("sentTimestamp(%v) = %v, want zero", attrs, got)
		}
	}
}

func opErr(status int) error {
	return &smithy.OperationError{
		ServiceID: "SQS", OperationName: "ReceiveMessage",
		Err: &smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}}, Err: errors.New("x")},
	}
}

func TestReceiveErrorReason(t *testing.T) {
	cases := map[string]error{
		"forbidden":       opErr(403),
		"queue_not_found": opErr(404),
		"throttled":       opErr(429),
		"server":          opErr(503),
		"credentials":     errors.New("failed to refresh cached credentials, no EC2 IMDS role found"),
		"network":         fmt.Errorf("dial tcp: connection refused"),
		"other":           context.Canceled,
	}
	for want, err := range cases {
		if got := receiveErrorReason(err); got != want {
			t.Errorf("receiveErrorReason(%v) = %q, want %q", err, got, want)
		}
	}
}
