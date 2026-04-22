package auth

import "context"

type AuthKeyRepository interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, key string) error
}
