package handler

import (
	"context"

	"diplom/internal/domain/user"
)

type userIDKey struct{}

// WithUserID attaches the identity verified by the authentication middleware.
func WithUserID(ctx context.Context, id user.ID) context.Context {
	return context.WithValue(ctx, userIDKey{}, id)
}

func UserID(ctx context.Context) (user.ID, bool) {
	id, ok := ctx.Value(userIDKey{}).(user.ID)
	return id, ok
}
