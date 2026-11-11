package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/worker"
)

// Listen subscribes to channels on a dedicated connection and hands every notification to receiver
// until ctx ends or the connection breaks.
func (db *DB) Listen(ctx context.Context, channels []string, receiver worker.Receiver) error {
	pooled, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listen connection: %w", err)
	}
	// A listening connection must never go back to the pool, so it is taken out and closed here.
	conn := pooled.Hijack()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), operationTimeout)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	for _, channel := range channels {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
			return fmt.Errorf("listen %s: %w", channel, err)
		}
	}
	receiver.Subscribed()
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}
		receiver.Receive(worker.Notification{Channel: notification.Channel, Payload: notification.Payload})
	}
}
