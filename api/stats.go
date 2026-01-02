package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// DeliveryStats holds inbox/outbox statistics
type DeliveryStats struct {
	Timestamp int64 `json:"timestamp"`
	Inbox     int64 `json:"inbox"`
	Outbox    int64 `json:"outbox"`
}

// StatsResponse is the API response format
type StatsResponse struct {
	Current DeliveryStats   `json:"current"`
	History []DeliveryStats `json:"history"`
}

// IncrementInboxCount increments the inbox counter
func IncrementInboxCount() {
	ctx := context.TODO()
	now := time.Now()
	bucket := now.Unix() / 60 * 60 // Round to minute
	key := "relay:stats:inbox:" + strconv.FormatInt(bucket, 10)

	RelayState.RedisClient.Incr(ctx, key)
	RelayState.RedisClient.Expire(ctx, key, 25*time.Hour) // Keep for 25 hours

	// Also increment total counter
	RelayState.RedisClient.Incr(ctx, "relay:stats:inbox:total")
}

// IncrementOutboxCount increments the outbox counter
func IncrementOutboxCount() {
	ctx := context.TODO()
	now := time.Now()
	bucket := now.Unix() / 60 * 60 // Round to minute
	key := "relay:stats:outbox:" + strconv.FormatInt(bucket, 10)

	RelayState.RedisClient.Incr(ctx, key)
	RelayState.RedisClient.Expire(ctx, key, 25*time.Hour) // Keep for 25 hours

	// Also increment total counter
	RelayState.RedisClient.Incr(ctx, "relay:stats:outbox:total")
}

// GetDeliveryStats retrieves delivery statistics
func GetDeliveryStats(hours int) StatsResponse {
	ctx := context.TODO()
	now := time.Now()
	currentBucket := now.Unix() / 60 * 60

	// Get total counts
	inboxTotal, _ := RelayState.RedisClient.Get(ctx, "relay:stats:inbox:total").Int64()
	outboxTotal, _ := RelayState.RedisClient.Get(ctx, "relay:stats:outbox:total").Int64()

	current := DeliveryStats{
		Timestamp: now.Unix(),
		Inbox:     inboxTotal,
		Outbox:    outboxTotal,
	}

	// Get historical data (per minute, up to specified hours)
	var history []DeliveryStats
	buckets := hours * 60 // Minutes in requested hours

	for i := buckets - 1; i >= 0; i-- {
		bucket := currentBucket - int64(i*60)
		inboxKey := "relay:stats:inbox:" + strconv.FormatInt(bucket, 10)
		outboxKey := "relay:stats:outbox:" + strconv.FormatInt(bucket, 10)

		inbox, _ := RelayState.RedisClient.Get(ctx, inboxKey).Int64()
		outbox, _ := RelayState.RedisClient.Get(ctx, outboxKey).Int64()

		history = append(history, DeliveryStats{
			Timestamp: bucket,
			Inbox:     inbox,
			Outbox:    outbox,
		})
	}

	return StatsResponse{
		Current: current,
		History: history,
	}
}

func handleDeliveryStats(writer http.ResponseWriter, request *http.Request) {
	if request.Method != "GET" {
		writer.WriteHeader(400)
		writer.Write(nil)
		return
	}

	// Allow CORS for frontend
	writer.Header().Set("Access-Control-Allow-Origin", "*")
	writer.Header().Set("Content-Type", "application/json")

	// Get hours parameter, default to 1 hour
	hoursStr := request.URL.Query().Get("hours")
	hours := 1
	if hoursStr != "" {
		if h, err := strconv.Atoi(hoursStr); err == nil && h > 0 && h <= 24 {
			hours = h
		}
	}

	stats := GetDeliveryStats(hours)
	response, err := json.Marshal(stats)
	if err != nil {
		writer.WriteHeader(500)
		writer.Write(nil)
		return
	}

	writer.WriteHeader(200)
	writer.Write(response)
}
