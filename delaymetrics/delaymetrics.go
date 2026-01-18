package delaymetrics

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// DelayRecord represents a single delay measurement
type DelayRecord struct {
	NoteID          string    `json:"note_id"`
	CreatedAt       time.Time `json:"created_at"`
	ReceivedAt      time.Time `json:"received_at"`
	DelaySeconds    float64   `json:"delay_seconds"`
	InstanceHost    string    `json:"instance_host"`
	InstanceName    string    `json:"instance_name,omitempty"`
	SoftwareName    string    `json:"software_name,omitempty"`
	SoftwareVersion string    `json:"software_version,omitempty"`
}

// InstanceStats represents aggregated stats for an instance
type InstanceStats struct {
	Host            string  `json:"host"`
	Name            string  `json:"name,omitempty"`
	SoftwareName    string  `json:"software_name,omitempty"`
	SoftwareVersion string  `json:"software_version,omitempty"`
	AvgDelaySeconds float64 `json:"avg_delay_seconds"`
	MinDelaySeconds float64 `json:"min_delay_seconds"`
	MaxDelaySeconds float64 `json:"max_delay_seconds"`
	SampleCount     int64   `json:"sample_count"`
	LastUpdated     int64   `json:"last_updated"`
}

// HourlyStats represents stats for a specific hour
type HourlyStats struct {
	Timestamp int64           `json:"timestamp"`
	Instances []InstanceStats `json:"instances"`
}

// DelayMetricsResponse is the API response format
type DelayMetricsResponse struct {
	LastUpdated    int64           `json:"last_updated"`
	SourceInstance string          `json:"source_instance"`
	Summary        []InstanceStats `json:"summary"`
	Hourly         []HourlyStats   `json:"hourly,omitempty"`
}

var redisClient *redis.Client

// Initialize sets up the Redis client for delay metrics
func Initialize(client *redis.Client) {
	redisClient = client
}

// RecordDelay records a federation delay measurement
func RecordDelay(record DelayRecord) error {
	if redisClient == nil {
		return nil
	}

	ctx := context.Background()
	now := time.Now()
	hourBucket := now.Unix() / 3600 * 3600 // Round to hour

	// Key for hourly instance data
	hourKey := "fdma:hour:" + strconv.FormatInt(hourBucket, 10) + ":" + record.InstanceHost

	// Store the delay value in a sorted set for calculating percentiles
	delayKey := "fdma:delays:" + strconv.FormatInt(hourBucket, 10) + ":" + record.InstanceHost

	pipe := redisClient.Pipeline()

	// Increment sample count and accumulate delay
	pipe.HIncrBy(ctx, hourKey, "count", 1)
	pipe.HIncrByFloat(ctx, hourKey, "total_delay", record.DelaySeconds)
	pipe.HSet(ctx, hourKey, "host", record.InstanceHost)
	pipe.HSet(ctx, hourKey, "last_updated", now.Unix())

	if record.InstanceName != "" {
		pipe.HSet(ctx, hourKey, "name", record.InstanceName)
	}
	if record.SoftwareName != "" {
		pipe.HSet(ctx, hourKey, "software_name", record.SoftwareName)
	}
	if record.SoftwareVersion != "" {
		pipe.HSet(ctx, hourKey, "software_version", record.SoftwareVersion)
	}

	// Update min/max
	pipe.HSetNX(ctx, hourKey, "min_delay", record.DelaySeconds)
	pipe.HSetNX(ctx, hourKey, "max_delay", record.DelaySeconds)

	// Set expiration (keep for 25 hours)
	pipe.Expire(ctx, hourKey, 25*time.Hour)
	pipe.Expire(ctx, delayKey, 25*time.Hour)

	// Track which instances were seen in this hour
	pipe.SAdd(ctx, "fdma:instances:"+strconv.FormatInt(hourBucket, 10), record.InstanceHost)
	pipe.Expire(ctx, "fdma:instances:"+strconv.FormatInt(hourBucket, 10), 25*time.Hour)

	// Track all known instances
	pipe.SAdd(ctx, "fdma:all_instances", record.InstanceHost)

	_, err := pipe.Exec(ctx)
	if err != nil {
		logrus.Errorf("Failed to record delay metrics: %v", err)
		return err
	}

	// Update min/max using Lua script for atomicity
	updateMinMaxScript := redis.NewScript(`
		local current_min = tonumber(redis.call('HGET', KEYS[1], 'min_delay'))
		local current_max = tonumber(redis.call('HGET', KEYS[1], 'max_delay'))
		local new_val = tonumber(ARGV[1])
		
		if current_min == nil or new_val < current_min then
			redis.call('HSET', KEYS[1], 'min_delay', new_val)
		end
		if current_max == nil or new_val > current_max then
			redis.call('HSET', KEYS[1], 'max_delay', new_val)
		end
		return 1
	`)
	updateMinMaxScript.Run(ctx, redisClient, []string{hourKey}, record.DelaySeconds)

	return nil
}

// GetInstanceStats retrieves stats for a specific instance and hour
func getInstanceStats(ctx context.Context, hourBucket int64, host string) (*InstanceStats, error) {
	hourKey := "fdma:hour:" + strconv.FormatInt(hourBucket, 10) + ":" + host

	data, err := redisClient.HGetAll(ctx, hourKey).Result()
	if err != nil || len(data) == 0 {
		return nil, err
	}

	count, _ := strconv.ParseInt(data["count"], 10, 64)
	if count == 0 {
		return nil, nil
	}

	totalDelay, _ := strconv.ParseFloat(data["total_delay"], 64)
	minDelay, _ := strconv.ParseFloat(data["min_delay"], 64)
	maxDelay, _ := strconv.ParseFloat(data["max_delay"], 64)
	lastUpdated, _ := strconv.ParseInt(data["last_updated"], 10, 64)

	return &InstanceStats{
		Host:            data["host"],
		Name:            data["name"],
		SoftwareName:    data["software_name"],
		SoftwareVersion: data["software_version"],
		AvgDelaySeconds: totalDelay / float64(count),
		MinDelaySeconds: minDelay,
		MaxDelaySeconds: maxDelay,
		SampleCount:     count,
		LastUpdated:     lastUpdated,
	}, nil
}

// GetDelayMetrics retrieves delay metrics for the specified number of hours
func GetDelayMetrics(hours int, sourceInstance string) DelayMetricsResponse {
	if redisClient == nil {
		return DelayMetricsResponse{
			LastUpdated:    time.Now().Unix(),
			SourceInstance: sourceInstance,
		}
	}

	ctx := context.Background()
	now := time.Now()
	currentHour := now.Unix() / 3600 * 3600

	response := DelayMetricsResponse{
		LastUpdated:    now.Unix(),
		SourceInstance: sourceInstance,
		Summary:        []InstanceStats{},
		Hourly:         []HourlyStats{},
	}

	// Aggregate summary over all hours
	summaryMap := make(map[string]*struct {
		TotalDelay  float64
		TotalCount  int64
		MinDelay    float64
		MaxDelay    float64
		Name        string
		Software    string
		Version     string
		LastUpdated int64
	})

	// Collect hourly data
	for i := 0; i < hours; i++ {
		hourBucket := currentHour - int64(i*3600)
		instancesKey := "fdma:instances:" + strconv.FormatInt(hourBucket, 10)

		instances, err := redisClient.SMembers(ctx, instancesKey).Result()
		if err != nil {
			continue
		}

		hourlyStats := HourlyStats{
			Timestamp: hourBucket,
			Instances: []InstanceStats{},
		}

		for _, host := range instances {
			stats, err := getInstanceStats(ctx, hourBucket, host)
			if err != nil || stats == nil {
				continue
			}

			hourlyStats.Instances = append(hourlyStats.Instances, *stats)

			// Aggregate for summary
			if summaryMap[host] == nil {
				summaryMap[host] = &struct {
					TotalDelay  float64
					TotalCount  int64
					MinDelay    float64
					MaxDelay    float64
					Name        string
					Software    string
					Version     string
					LastUpdated int64
				}{
					MinDelay: stats.MinDelaySeconds,
					MaxDelay: stats.MaxDelaySeconds,
				}
			}
			s := summaryMap[host]
			s.TotalDelay += stats.AvgDelaySeconds * float64(stats.SampleCount)
			s.TotalCount += stats.SampleCount
			if stats.MinDelaySeconds < s.MinDelay {
				s.MinDelay = stats.MinDelaySeconds
			}
			if stats.MaxDelaySeconds > s.MaxDelay {
				s.MaxDelay = stats.MaxDelaySeconds
			}
			if stats.Name != "" {
				s.Name = stats.Name
			}
			if stats.SoftwareName != "" {
				s.Software = stats.SoftwareName
			}
			if stats.SoftwareVersion != "" {
				s.Version = stats.SoftwareVersion
			}
			if stats.LastUpdated > s.LastUpdated {
				s.LastUpdated = stats.LastUpdated
			}
		}

		response.Hourly = append(response.Hourly, hourlyStats)
	}

	// Build summary
	for host, data := range summaryMap {
		if data.TotalCount > 0 {
			response.Summary = append(response.Summary, InstanceStats{
				Host:            host,
				Name:            data.Name,
				SoftwareName:    data.Software,
				SoftwareVersion: data.Version,
				AvgDelaySeconds: data.TotalDelay / float64(data.TotalCount),
				MinDelaySeconds: data.MinDelay,
				MaxDelaySeconds: data.MaxDelay,
				SampleCount:     data.TotalCount,
				LastUpdated:     data.LastUpdated,
			})
		}
	}

	return response
}

// GetDelayMetricsJSON returns the delay metrics as JSON bytes
func GetDelayMetricsJSON(hours int, sourceInstance string) ([]byte, error) {
	metrics := GetDelayMetrics(hours, sourceInstance)
	return json.Marshal(metrics)
}
