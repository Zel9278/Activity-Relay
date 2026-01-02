package deliver

import (
	"context"
	"strconv"
	"time"
)

// IncrementOutboxCount increments the outbox counter
func IncrementOutboxCount() {
	ctx := context.TODO()
	now := time.Now()
	bucket := now.Unix() / 60 * 60 // Round to minute
	key := "relay:stats:outbox:" + strconv.FormatInt(bucket, 10)

	RedisClient.Incr(ctx, key)
	RedisClient.Expire(ctx, key, 25*time.Hour) // Keep for 25 hours

	// Also increment total counter
	RedisClient.Incr(ctx, "relay:stats:outbox:total")
}
