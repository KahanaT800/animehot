package scheduler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestScheduleStore(t *testing.T) (*RedisScheduleStore, *miniredis.Miniredis, func()) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr: s.Addr(),
	})

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	store := NewRedisScheduleStore(rdb, logger)

	return store, s, func() {
		rdb.Close()
		s.Close()
	}
}

func TestNewRedisScheduleStore(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	assert.NotNil(t, store)
	assert.NotNil(t, store.rdb)
	assert.Equal(t, ScheduleZSetKey, store.key)
}

func TestScheduleStore_Schedule(t *testing.T) {
	store, s, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(11)
	nextTime := time.Now().Add(time.Hour)

	err := store.Schedule(ctx, ipID, nextTime)
	require.NoError(t, err)

	// Verify the ZSET entry
	score, err := s.ZScore(ScheduleZSetKey, "11")
	require.NoError(t, err)
	assert.InDelta(t, float64(nextTime.Unix()), score, 1)
}

func TestScheduleStore_Schedule_UpdatesExisting(t *testing.T) {
	store, s, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(11)

	// Schedule once
	firstTime := time.Now().Add(time.Hour)
	err := store.Schedule(ctx, ipID, firstTime)
	require.NoError(t, err)

	// Schedule again with different time (should update, not create duplicate)
	secondTime := time.Now().Add(2 * time.Hour)
	err = store.Schedule(ctx, ipID, secondTime)
	require.NoError(t, err)

	// Check that there's only one entry
	count, err := store.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Check that the score was updated
	score, err := s.ZScore(ScheduleZSetKey, "11")
	require.NoError(t, err)
	assert.InDelta(t, float64(secondTime.Unix()), score, 1)
}

func TestScheduleStore_GetDue_Empty(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()

	dueIPs, err := store.GetDue(ctx)
	require.NoError(t, err)
	assert.Empty(t, dueIPs)
}

func TestScheduleStore_GetDue_SomeOverdue(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// Schedule some IPs: 2 overdue, 2 future
	_ = store.Schedule(ctx, 1, now.Add(-2*time.Hour)) // overdue
	_ = store.Schedule(ctx, 2, now.Add(-1*time.Hour)) // overdue
	_ = store.Schedule(ctx, 3, now.Add(1*time.Hour))  // future
	_ = store.Schedule(ctx, 4, now.Add(2*time.Hour))  // future

	dueIPs, err := store.GetDue(ctx)
	require.NoError(t, err)
	assert.Len(t, dueIPs, 2)
	assert.Contains(t, dueIPs, uint64(1))
	assert.Contains(t, dueIPs, uint64(2))
}

func TestScheduleStore_GetDue_AllOverdue(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	_ = store.Schedule(ctx, 1, now.Add(-2*time.Hour))
	_ = store.Schedule(ctx, 2, now.Add(-1*time.Hour))
	_ = store.Schedule(ctx, 3, now.Add(-30*time.Minute))

	dueIPs, err := store.GetDue(ctx)
	require.NoError(t, err)
	assert.Len(t, dueIPs, 3)
}

func TestScheduleStore_GetNextTime_Empty(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()

	nextTime, exists, err := store.GetNextTime(ctx)
	require.NoError(t, err)
	assert.False(t, exists)
	assert.True(t, nextTime.IsZero())
}

func TestScheduleStore_GetNextTime_ReturnsEarliest(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	earliest := now.Add(30 * time.Minute)
	_ = store.Schedule(ctx, 1, now.Add(2*time.Hour))
	_ = store.Schedule(ctx, 2, earliest) // earliest
	_ = store.Schedule(ctx, 3, now.Add(1*time.Hour))

	nextTime, exists, err := store.GetNextTime(ctx)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.InDelta(t, earliest.Unix(), nextTime.Unix(), 1)
}

func TestScheduleStore_Remove(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()

	// Add some IPs
	_ = store.Schedule(ctx, 1, time.Now().Add(time.Hour))
	_ = store.Schedule(ctx, 2, time.Now().Add(time.Hour))

	// Remove one
	err := store.Remove(ctx, 1)
	require.NoError(t, err)

	// Verify removal
	count, err := store.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Check the remaining one
	_, exists, err := store.GetScheduleTime(ctx, 1)
	require.NoError(t, err)
	assert.False(t, exists)

	_, exists, err = store.GetScheduleTime(ctx, 2)
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestScheduleStore_Remove_NonExistent(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remove non-existent should not error
	err := store.Remove(ctx, 999)
	require.NoError(t, err)
}

func TestScheduleStore_Count(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()

	// Initially empty
	count, err := store.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Add some
	_ = store.Schedule(ctx, 1, time.Now().Add(time.Hour))
	_ = store.Schedule(ctx, 2, time.Now().Add(time.Hour))
	_ = store.Schedule(ctx, 3, time.Now().Add(time.Hour))

	count, err = store.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestScheduleStore_GetAll(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	time1 := now.Add(1 * time.Hour)
	time2 := now.Add(2 * time.Hour)
	time3 := now.Add(3 * time.Hour)

	_ = store.Schedule(ctx, 1, time1)
	_ = store.Schedule(ctx, 2, time2)
	_ = store.Schedule(ctx, 3, time3)

	all, err := store.GetAll(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	assert.InDelta(t, time1.Unix(), all[1].Unix(), 1)
	assert.InDelta(t, time2.Unix(), all[2].Unix(), 1)
	assert.InDelta(t, time3.Unix(), all[3].Unix(), 1)
}

func TestScheduleStore_GetScheduleTime(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	nextTime := time.Now().Add(time.Hour)

	_ = store.Schedule(ctx, 11, nextTime)

	// Get existing
	gotTime, exists, err := store.GetScheduleTime(ctx, 11)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.InDelta(t, nextTime.Unix(), gotTime.Unix(), 1)

	// Get non-existing
	_, exists, err = store.GetScheduleTime(ctx, 999)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestScheduleStore_ScheduleBatch(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	schedules := map[uint64]time.Time{
		1: now.Add(1 * time.Hour),
		2: now.Add(2 * time.Hour),
		3: now.Add(3 * time.Hour),
	}

	err := store.ScheduleBatch(ctx, schedules)
	require.NoError(t, err)

	count, err := store.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestScheduleStore_ScheduleBatch_Empty(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()

	err := store.ScheduleBatch(ctx, map[uint64]time.Time{})
	require.NoError(t, err)
}

func TestScheduleStore_ConcurrentAccess(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	done := make(chan bool, 2)

	// Concurrent writes
	go func() {
		for i := uint64(0); i < 100; i++ {
			_ = store.Schedule(ctx, i, time.Now().Add(time.Duration(i)*time.Minute))
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < 100; i++ {
			_, _ = store.GetDue(ctx)
			_, _, _ = store.GetNextTime(ctx)
			_, _ = store.Count(ctx)
		}
		done <- true
	}()

	<-done
	<-done
}

func TestScheduleStore_OrderPreserved(t *testing.T) {
	store, _, cleanup := setupTestScheduleStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// Add in random order
	_ = store.Schedule(ctx, 3, now.Add(3*time.Hour))
	_ = store.Schedule(ctx, 1, now.Add(1*time.Hour))
	_ = store.Schedule(ctx, 2, now.Add(2*time.Hour))

	// GetNextTime should return the earliest
	nextTime, exists, err := store.GetNextTime(ctx)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.InDelta(t, now.Add(1*time.Hour).Unix(), nextTime.Unix(), 1)
}
