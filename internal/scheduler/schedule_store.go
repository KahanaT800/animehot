package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// ScheduleZSetKey Redis ZSET key for storing IP schedule times
	// Score = Unix timestamp of next scheduled time
	// Member = IP ID
	ScheduleZSetKey = "animetop:schedule:pending"
)

// ScheduleStore defines the interface for schedule persistence
type ScheduleStore interface {
	// Schedule sets the next schedule time for an IP
	// Uses ZADD which will update existing entries (no duplicates)
	Schedule(ctx context.Context, ipID uint64, nextTime time.Time) error

	// GetDue returns all IPs that are due for scheduling (score <= now)
	GetDue(ctx context.Context) ([]uint64, error)

	// GetNextTime returns the earliest schedule time (for precise sleeping)
	// Returns the time, whether any entry exists, and any error
	GetNextTime(ctx context.Context) (time.Time, bool, error)

	// Remove removes an IP from the schedule (e.g., when IP is deleted)
	Remove(ctx context.Context, ipID uint64) error

	// Count returns the number of IPs in the schedule
	Count(ctx context.Context) (int64, error)

	// GetAll returns all scheduled IPs with their next schedule times (for debugging)
	GetAll(ctx context.Context) (map[uint64]time.Time, error)

	// GetScheduleTime returns the next schedule time for a specific IP
	GetScheduleTime(ctx context.Context, ipID uint64) (time.Time, bool, error)
}

// RedisScheduleStore implements ScheduleStore using Redis ZSET
type RedisScheduleStore struct {
	rdb    *redis.Client
	key    string
	logger *slog.Logger
}

// NewRedisScheduleStore creates a new Redis-based schedule store
func NewRedisScheduleStore(rdb *redis.Client, logger *slog.Logger) *RedisScheduleStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisScheduleStore{
		rdb:    rdb,
		key:    ScheduleZSetKey,
		logger: logger,
	}
}

// Schedule sets the next schedule time for an IP
// ZADD will update the score if the member already exists
func (s *RedisScheduleStore) Schedule(ctx context.Context, ipID uint64, nextTime time.Time) error {
	score := float64(nextTime.Unix())
	member := strconv.FormatUint(ipID, 10)

	err := s.rdb.ZAdd(ctx, s.key, redis.Z{
		Score:  score,
		Member: member,
	}).Err()

	if err != nil {
		return fmt.Errorf("ZADD failed: %w", err)
	}

	s.logger.Debug("scheduled IP",
		slog.Uint64("ip_id", ipID),
		slog.Time("next_time", nextTime),
		slog.Float64("score", score))

	return nil
}

// GetDue returns all IPs that are due for scheduling (score <= now)
func (s *RedisScheduleStore) GetDue(ctx context.Context) ([]uint64, error) {
	now := float64(time.Now().Unix())

	// ZRANGEBYSCORE with scores from -inf to now
	members, err := s.rdb.ZRangeByScore(ctx, s.key, &redis.ZRangeBy{
		Min: "-inf",
		Max: strconv.FormatFloat(now, 'f', 0, 64),
	}).Result()

	if err != nil {
		return nil, fmt.Errorf("ZRANGEBYSCORE failed: %w", err)
	}

	ipIDs := make([]uint64, 0, len(members))
	for _, m := range members {
		id, err := strconv.ParseUint(m, 10, 64)
		if err != nil {
			s.logger.Warn("invalid IP ID in schedule",
				slog.String("member", m),
				slog.String("error", err.Error()))
			continue
		}
		ipIDs = append(ipIDs, id)
	}

	return ipIDs, nil
}

// GetNextTime returns the earliest schedule time
func (s *RedisScheduleStore) GetNextTime(ctx context.Context) (time.Time, bool, error) {
	// ZRANGE with WITHSCORES to get the first element
	result, err := s.rdb.ZRangeWithScores(ctx, s.key, 0, 0).Result()
	if err != nil {
		return time.Time{}, false, fmt.Errorf("ZRANGE failed: %w", err)
	}

	if len(result) == 0 {
		return time.Time{}, false, nil
	}

	timestamp := int64(result[0].Score)
	return time.Unix(timestamp, 0), true, nil
}

// Remove removes an IP from the schedule
func (s *RedisScheduleStore) Remove(ctx context.Context, ipID uint64) error {
	member := strconv.FormatUint(ipID, 10)
	err := s.rdb.ZRem(ctx, s.key, member).Err()
	if err != nil {
		return fmt.Errorf("ZREM failed: %w", err)
	}

	s.logger.Debug("removed IP from schedule", slog.Uint64("ip_id", ipID))
	return nil
}

// Count returns the number of IPs in the schedule
func (s *RedisScheduleStore) Count(ctx context.Context) (int64, error) {
	count, err := s.rdb.ZCard(ctx, s.key).Result()
	if err != nil {
		return 0, fmt.Errorf("ZCARD failed: %w", err)
	}
	return count, nil
}

// GetAll returns all scheduled IPs with their next schedule times
func (s *RedisScheduleStore) GetAll(ctx context.Context) (map[uint64]time.Time, error) {
	result, err := s.rdb.ZRangeWithScores(ctx, s.key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("ZRANGE failed: %w", err)
	}

	schedules := make(map[uint64]time.Time, len(result))
	for _, z := range result {
		member, ok := z.Member.(string)
		if !ok {
			continue
		}
		id, err := strconv.ParseUint(member, 10, 64)
		if err != nil {
			continue
		}
		schedules[id] = time.Unix(int64(z.Score), 0)
	}

	return schedules, nil
}

// GetScheduleTime returns the next schedule time for a specific IP
func (s *RedisScheduleStore) GetScheduleTime(ctx context.Context, ipID uint64) (time.Time, bool, error) {
	member := strconv.FormatUint(ipID, 10)
	score, err := s.rdb.ZScore(ctx, s.key, member).Result()
	if err != nil {
		if err == redis.Nil {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("ZSCORE failed: %w", err)
	}

	return time.Unix(int64(score), 0), true, nil
}

// ScheduleBatch schedules multiple IPs at once (atomic operation)
func (s *RedisScheduleStore) ScheduleBatch(ctx context.Context, schedules map[uint64]time.Time) error {
	if len(schedules) == 0 {
		return nil
	}

	members := make([]redis.Z, 0, len(schedules))
	for ipID, nextTime := range schedules {
		members = append(members, redis.Z{
			Score:  float64(nextTime.Unix()),
			Member: strconv.FormatUint(ipID, 10),
		})
	}

	err := s.rdb.ZAdd(ctx, s.key, members...).Err()
	if err != nil {
		return fmt.Errorf("ZADD batch failed: %w", err)
	}

	s.logger.Debug("batch scheduled IPs", slog.Int("count", len(schedules)))
	return nil
}
