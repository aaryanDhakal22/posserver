package sqsconsumer

import (
	"testing"

	"quiccpos/main/internal/domain/order"

	"github.com/aws/aws-sdk-go-v2/aws"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

func strAttr(v string) sqstypes.MessageAttributeValue {
	return sqstypes.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
}

func TestScopeForSource(t *testing.T) {
	tid, lid := uuid.New(), uuid.New()
	full := map[string]sqstypes.MessageAttributeValue{
		tenantIDAttribute:   strAttr(tid.String()),
		locationIDAttribute: strAttr(lid.String()),
	}
	empty := map[string]sqstypes.MessageAttributeValue{}
	bad := map[string]sqstypes.MessageAttributeValue{
		tenantIDAttribute:   strAttr("not-a-uuid"),
		locationIDAttribute: strAttr(lid.String()),
	}

	t.Run("online without scope is accepted", func(t *testing.T) {
		if _, _, err := scopeForSource(order.SourceOnline, empty); err != nil {
			t.Fatalf("online should not require scope: %v", err)
		}
	})
	t.Run("online with malformed scope is accepted", func(t *testing.T) {
		if _, _, err := scopeForSource(order.SourceOnline, bad); err != nil {
			t.Fatalf("online should ignore scope: %v", err)
		}
	})
	t.Run("other sources still require scope", func(t *testing.T) {
		if _, _, err := scopeForSource(order.SourceMobile, empty); err == nil {
			t.Fatal("non-online source without scope must fail")
		}
		if _, _, err := scopeForSource(order.SourceMobile, bad); err == nil {
			t.Fatal("non-online source with malformed scope must fail")
		}
	})
	t.Run("scope is parsed when present", func(t *testing.T) {
		gotT, gotL, err := scopeForSource(order.SourceMobile, full)
		if err != nil || uuid.UUID(gotT) != tid || uuid.UUID(gotL) != lid {
			t.Fatalf("got %v/%v err=%v, want %v/%v", gotT, gotL, err, tid, lid)
		}
	})
}
