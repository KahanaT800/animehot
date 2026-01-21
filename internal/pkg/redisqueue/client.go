package redisqueue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"animetop/internal/pkg/metrics"
	"animetop/proto/pb"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	KeyTaskQueue           = "animetop:queue:tasks"
	KeyTaskProcessingQueue = "animetop:queue:tasks:processing"
	KeyTaskPendingSet      = "animetop:queue:tasks:pending" // 去重集合
	KeyTaskStartedHash     = "animetop:queue:tasks:started" // 任务开始处理时间 (task_id -> unix timestamp)
	KeyTaskDeadLetter      = "animetop:queue:tasks:dead"    // 死信队列 (超过最大重试次数)

	KeyResultQueue           = "animetop:queue:results"
	KeyResultProcessingQueue = "animetop:queue:results:processing"
	KeyResultStartedHash     = "animetop:queue:results:started" // 结果开始处理时间 (task_id -> unix timestamp)
	KeyResultDeadLetter      = "animetop:queue:results:dead"    // 死信队列

	KeyProcessedSet = "animetop:processed" // 已处理的 task_id 集合 (幂等性检查)

	// 默认最大重试次数
	DefaultMaxRetries = 3
)

var (
	ErrNoTask     = errors.New("no task available")
	ErrNoResult   = errors.New("no result available")
	ErrTaskExists = errors.New("task already in queue") // 任务已存在
)

// Client wraps Redis List operations for task/result queues.
type Client struct {
	rdb *redis.Client
}

// NewClient creates a redisqueue client with address/password.
func NewClient(addr, password string) *Client {
	return &Client{
		rdb: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       0,
		}),
	}
}

// NewClientWithRedis creates a redisqueue client from an existing redis.Client.
func NewClientWithRedis(rdb *redis.Client) (*Client, error) {
	if rdb == nil {
		return nil, errors.New("redis client is nil")
	}
	return &Client{rdb: rdb}, nil
}

// pushTaskScript 原子性地执行 SADD + LPUSH，避免中间状态不一致。
// KEYS[1] = pending set, KEYS[2] = task queue
// ARGV[1] = dedup_key (IP ID), ARGV[2] = task JSON
// 返回: 1 = 成功推送, 0 = 任务已存在 (同一 IP 已有任务在队列中)
var pushTaskScript = redis.NewScript(`
	local added = redis.call('SADD', KEYS[1], ARGV[1])
	if added == 0 then
		return 0
	end
	redis.call('LPUSH', KEYS[2], ARGV[2])
	return 1
`)

// PushTask serializes a CrawlRequest and pushes it into the task queue.
// 使用 Lua 脚本原子执行 SADD + LPUSH，确保一致性。
// 去重基于 IP ID：同一个 IP 只能有一个任务在队列中。
// 如果该 IP 已有任务在队列中，返回 ErrTaskExists。
func (c *Client) PushTask(ctx context.Context, task *pb.CrawlRequest) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}

	taskID := task.GetTaskId()
	if taskID == "" {
		return errors.New("task id is empty")
	}

	ipID := task.GetIpId()
	if ipID == 0 {
		return errors.New("ip id is empty")
	}

	// 使用 IP ID 作为去重 key
	dedupKey := fmt.Sprintf("ip:%d", ipID)

	// 序列化
	data, err := protojson.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	// 使用 Lua 脚本原子执行 SADD + LPUSH
	result, err := pushTaskScript.Run(ctx, c.rdb,
		[]string{KeyTaskPendingSet, KeyTaskQueue},
		dedupKey, string(data),
	).Int()
	if err != nil {
		return fmt.Errorf("push task script: %w", err)
	}

	if result == 0 {
		// 该 IP 已有任务在队列中，跳过
		metrics.CrawlerTaskThroughput.WithLabelValues("in", "skipped").Inc()
		metrics.SchedulerTasksSkippedTotal.Inc()
		return ErrTaskExists
	}

	metrics.CrawlerTaskThroughput.WithLabelValues("in", "pushed").Inc()
	metrics.SchedulerTasksPushedTotal.Inc()
	return nil
}

// PushTaskForce 强制推送任务，不检查去重（用于特殊场景）。
func (c *Client) PushTaskForce(ctx context.Context, task *pb.CrawlRequest) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}
	data, err := protojson.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	if err := c.rdb.LPush(ctx, KeyTaskQueue, string(data)).Err(); err != nil {
		return fmt.Errorf("lpush task: %w", err)
	}
	// 也加入 pending set
	if taskID := task.GetTaskId(); taskID != "" {
		c.rdb.SAdd(ctx, KeyTaskPendingSet, taskID)
	}
	metrics.CrawlerTaskThroughput.WithLabelValues("in", "pushed").Inc()
	return nil
}

// PopTask blocks until a task is available or timeout is reached.
// 同时记录任务开始处理的时间到 KeyTaskStartedHash。
func (c *Client) PopTask(ctx context.Context, timeout time.Duration) (*pb.CrawlRequest, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client is not initialized")
	}
	result, err := c.rdb.BRPopLPush(ctx, KeyTaskQueue, KeyTaskProcessingQueue, timeout).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNoTask
	}
	if err != nil {
		return nil, fmt.Errorf("brpoplpush task: %w", err)
	}

	var req pb.CrawlRequest
	if err := protojson.Unmarshal([]byte(result), &req); err != nil {
		return nil, fmt.Errorf("unmarshal task: %w", err)
	}

	// 记录任务开始处理的时间（用于 Janitor 判断超时）
	if taskID := req.GetTaskId(); taskID != "" {
		c.rdb.HSet(ctx, KeyTaskStartedHash, taskID, time.Now().Unix())
	}

	metrics.CrawlerTaskThroughput.WithLabelValues("out", "popped").Inc()
	return &req, nil
}

// PushResult serializes a CrawlResponse and pushes it into the result queue.
func (c *Client) PushResult(ctx context.Context, res *pb.CrawlResponse) error {
	if res == nil {
		return errors.New("result is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}
	data, err := protojson.Marshal(res)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := c.rdb.LPush(ctx, KeyResultQueue, string(data)).Err(); err != nil {
		return fmt.Errorf("lpush result: %w", err)
	}
	return nil
}

// PopResult blocks until a result is available or timeout is reached.
// 使用 BRPopLPush 实现可靠消费，结果先移到 processing queue。
func (c *Client) PopResult(ctx context.Context, timeout time.Duration) (*pb.CrawlResponse, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client is not initialized")
	}
	result, err := c.rdb.BRPopLPush(ctx, KeyResultQueue, KeyResultProcessingQueue, timeout).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNoResult
	}
	if err != nil {
		return nil, fmt.Errorf("brpoplpush result: %w", err)
	}

	var resp pb.CrawlResponse
	if err := protojson.Unmarshal([]byte(result), &resp); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	// 记录结果开始处理的时间（用于 Janitor 判断超时）
	if taskID := resp.GetTaskId(); taskID != "" {
		c.rdb.HSet(ctx, KeyResultStartedHash, taskID, time.Now().Unix())
	}

	return &resp, nil
}

// ackTaskScript 原子性地从 processing queue 中找到并删除匹配 task_id 的任务。
// KEYS[1] = processing queue, KEYS[2] = pending set, KEYS[3] = started hash
// ARGV[1] = task_id, ARGV[2] = dedup_key (ip:xxx)
// 返回: 删除的任务数量
var ackTaskScript = redis.NewScript(`
	local queue = KEYS[1]
	local pending = KEYS[2]
	local started = KEYS[3]
	local taskId = ARGV[1]
	local dedupKey = ARGV[2]

	-- 遍历 processing queue 找到匹配的任务
	local tasks = redis.call('LRANGE', queue, 0, -1)
	local removed = 0
	for _, task in ipairs(tasks) do
		-- 检查 JSON 中是否包含该 task_id
		if string.find(task, '"taskId":"' .. taskId .. '"', 1, true) then
			redis.call('LREM', queue, 1, task)
			removed = removed + 1
			break
		end
	end

	-- 从 pending set (使用 dedup_key) 和 started hash (使用 task_id) 中移除
	redis.call('SREM', pending, dedupKey)
	redis.call('HDEL', started, taskId)

	return removed
`)

// AckTask removes a processed task from the processing queue, pending set, and started hash.
// 使用 task_id 匹配而非完整 JSON，避免序列化差异导致的匹配失败。
// 这允许该 IP 在下一个调度周期被重新推送新任务。
func (c *Client) AckTask(ctx context.Context, task *pb.CrawlRequest) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}

	taskID := task.GetTaskId()
	if taskID == "" {
		return errors.New("task id is empty")
	}

	ipID := task.GetIpId()
	dedupKey := fmt.Sprintf("ip:%d", ipID)

	// 使用 Lua 脚本原子执行
	_, err := ackTaskScript.Run(ctx, c.rdb,
		[]string{KeyTaskProcessingQueue, KeyTaskPendingSet, KeyTaskStartedHash},
		taskID, dedupKey,
	).Int()
	if err != nil {
		return fmt.Errorf("ack task script: %w", err)
	}

	return nil
}

// ackResultScript 原子性地从 result processing queue 中找到并删除匹配 task_id 的结果。
// KEYS[1] = processing queue, KEYS[2] = started hash
// ARGV[1] = task_id
// 返回: 删除的结果数量
var ackResultScript = redis.NewScript(`
	local queue = KEYS[1]
	local started = KEYS[2]
	local taskId = ARGV[1]

	-- 遍历 processing queue 找到匹配的结果
	local results = redis.call('LRANGE', queue, 0, -1)
	local removed = 0
	for _, result in ipairs(results) do
		-- 检查 JSON 中是否包含该 task_id
		if string.find(result, '"taskId":"' .. taskId .. '"', 1, true) then
			redis.call('LREM', queue, 1, result)
			removed = removed + 1
			break
		end
	end

	-- 从 started hash 中移除
	redis.call('HDEL', started, taskId)

	return removed
`)

// AckResult removes a processed result from the result processing queue.
// 使用 task_id 匹配而非完整 JSON，避免序列化差异导致的匹配失败。
func (c *Client) AckResult(ctx context.Context, resp *pb.CrawlResponse) error {
	if resp == nil {
		return errors.New("result is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}

	taskID := resp.GetTaskId()
	if taskID == "" {
		return errors.New("task id is empty")
	}

	// 使用 Lua 脚本原子执行
	_, err := ackResultScript.Run(ctx, c.rdb,
		[]string{KeyResultProcessingQueue, KeyResultStartedHash},
		taskID,
	).Int()
	if err != nil {
		return fmt.Errorf("ack result script: %w", err)
	}

	return nil
}

// QueueDepth returns the current length of task and result queues.
func (c *Client) QueueDepth(ctx context.Context) (int64, int64, error) {
	if c == nil || c.rdb == nil {
		return 0, 0, errors.New("redis client is not initialized")
	}
	tasks, err := c.rdb.LLen(ctx, KeyTaskQueue).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("llen tasks: %w", err)
	}
	results, err := c.rdb.LLen(ctx, KeyResultQueue).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("llen results: %w", err)
	}
	return tasks, results, nil
}

// PendingSetSize returns the number of unique tasks currently pending.
func (c *Client) PendingSetSize(ctx context.Context) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client is not initialized")
	}
	size, err := c.rdb.SCard(ctx, KeyTaskPendingSet).Result()
	if err != nil {
		return 0, fmt.Errorf("scard pending set: %w", err)
	}
	return size, nil
}

// rescueScript 是用于原子性 rescue 任务的 Lua 脚本。
// 只有当 LREM 成功移除了任务时，才执行 LPUSH，防止多个 Janitor 重复添加。
// 同时清理 started hash 中的记录。
// KEYS[1] = processing queue, KEYS[2] = task queue, KEYS[3] = started hash, KEYS[4] = dead letter queue, KEYS[5] = pending set
// ARGV[1] = old task JSON, ARGV[2] = task_id, ARGV[3] = new task JSON (with incremented retry), ARGV[4] = is_dead (1=dead, 0=retry), ARGV[5] = dedup_key
// 返回: 1 = 成功 rescue/dead, 0 = 任务不存在
var rescueScript = redis.NewScript(`
	local removed = redis.call('LREM', KEYS[1], 1, ARGV[1])
	if removed > 0 then
		if ARGV[4] == "1" then
			-- 移到死信队列，同时从 pending set 移除 (使用 dedup_key)
			redis.call('LPUSH', KEYS[4], ARGV[3])
			redis.call('SREM', KEYS[5], ARGV[5])
		else
			-- 重新入队，保留在 pending set (阻止重复推送)
			redis.call('LPUSH', KEYS[2], ARGV[3])
		end
		redis.call('HDEL', KEYS[3], ARGV[2])
		return 1
	end
	return 0
`)

// RescueResult 包含 rescue 操作的结果
type RescueResult struct {
	Rescued   int // 重新入队数量
	DeadLeter int // 移入死信队列数量
}

// RescueStuckTasks scans processing queue and requeues tasks that exceed timeout.
// 超过最大重试次数的任务移入死信队列。
func (c *Client) RescueStuckTasks(ctx context.Context, timeout time.Duration) (RescueResult, error) {
	result := RescueResult{}
	if c == nil || c.rdb == nil {
		return result, errors.New("redis client is not initialized")
	}

	// 获取所有任务的开始时间
	startedTimes, err := c.rdb.HGetAll(ctx, KeyTaskStartedHash).Result()
	if err != nil {
		return result, fmt.Errorf("hgetall started: %w", err)
	}
	if len(startedTimes) == 0 {
		return result, nil
	}

	// 获取 processing queue 中的任务
	tasksRaw, err := c.rdb.LRange(ctx, KeyTaskProcessingQueue, 0, -1).Result()
	if err != nil {
		return result, fmt.Errorf("lrange processing: %w", err)
	}
	if len(tasksRaw) == 0 {
		// processing queue 为空，但 started hash 有记录，清理孤立记录
		for taskID := range startedTimes {
			c.rdb.HDel(ctx, KeyTaskStartedHash, taskID)
		}
		return result, nil
	}

	now := time.Now().Unix()
	threshold := int64(timeout.Seconds())

	for _, raw := range tasksRaw {
		var task pb.CrawlRequest
		if err := protojson.Unmarshal([]byte(raw), &task); err != nil {
			continue
		}

		taskID := task.GetTaskId()
		if taskID == "" {
			continue
		}

		// 从 started hash 获取开始时间
		startedStr, ok := startedTimes[taskID]
		if !ok {
			// 没有记录开始时间，可能是老数据，使用 CreatedAt 作为后备
			if task.GetCreatedAt() == 0 {
				continue
			}
			if now-task.GetCreatedAt() <= threshold {
				continue
			}
		} else {
			// 使用开始时间判断
			var started int64
			if _, err := fmt.Sscanf(startedStr, "%d", &started); err != nil {
				continue
			}
			if now-started <= threshold {
				continue
			}
		}

		// 递增重试计数
		task.RetryCount++
		isDead := task.RetryCount > DefaultMaxRetries

		// 序列化新任务
		newData, err := protojson.Marshal(&task)
		if err != nil {
			continue
		}

		isDeadStr := "0"
		if isDead {
			isDeadStr = "1"
		}

		// 使用 Lua 脚本原子性地执行
		dedupKey := fmt.Sprintf("ip:%d", task.GetIpId())
		res, err := rescueScript.Run(ctx, c.rdb,
			[]string{KeyTaskProcessingQueue, KeyTaskQueue, KeyTaskStartedHash, KeyTaskDeadLetter, KeyTaskPendingSet},
			raw, taskID, string(newData), isDeadStr, dedupKey,
		).Int()
		if err != nil {
			continue
		}
		if res == 1 {
			if isDead {
				result.DeadLeter++
			} else {
				result.Rescued++
			}
		}
	}

	return result, nil
}

// rescueResultScript 是用于原子性 rescue 结果的 Lua 脚本。
// KEYS[1] = result processing queue, KEYS[2] = result queue, KEYS[3] = started hash, KEYS[4] = dead letter queue
// ARGV[1] = old result JSON, ARGV[2] = task_id, ARGV[3] = new result JSON, ARGV[4] = is_dead
// 返回: 1 = 成功 rescue/dead, 0 = 结果不存在
var rescueResultScript = redis.NewScript(`
	local removed = redis.call('LREM', KEYS[1], 1, ARGV[1])
	if removed > 0 then
		if ARGV[4] == "1" then
			redis.call('LPUSH', KEYS[4], ARGV[3])
		else
			redis.call('LPUSH', KEYS[2], ARGV[3])
		end
		redis.call('HDEL', KEYS[3], ARGV[2])
		return 1
	end
	return 0
`)

// RescueStuckResults scans result processing queue and requeues results that exceed timeout.
// 超过最大重试次数的结果移入死信队列。
func (c *Client) RescueStuckResults(ctx context.Context, timeout time.Duration) (RescueResult, error) {
	result := RescueResult{}
	if c == nil || c.rdb == nil {
		return result, errors.New("redis client is not initialized")
	}

	// 获取所有结果的开始时间
	startedTimes, err := c.rdb.HGetAll(ctx, KeyResultStartedHash).Result()
	if err != nil {
		return result, fmt.Errorf("hgetall started: %w", err)
	}
	if len(startedTimes) == 0 {
		return result, nil
	}

	// 获取 result processing queue 中的结果
	resultsRaw, err := c.rdb.LRange(ctx, KeyResultProcessingQueue, 0, -1).Result()
	if err != nil {
		return result, fmt.Errorf("lrange processing: %w", err)
	}
	if len(resultsRaw) == 0 {
		// processing queue 为空，但 started hash 有记录，清理孤立记录
		for taskID := range startedTimes {
			c.rdb.HDel(ctx, KeyResultStartedHash, taskID)
		}
		return result, nil
	}

	now := time.Now().Unix()
	threshold := int64(timeout.Seconds())

	for _, raw := range resultsRaw {
		var resp pb.CrawlResponse
		if err := protojson.Unmarshal([]byte(raw), &resp); err != nil {
			continue
		}

		taskID := resp.GetTaskId()
		if taskID == "" {
			continue
		}

		// 从 started hash 获取开始时间
		startedStr, ok := startedTimes[taskID]
		if !ok {
			// 没有记录开始时间，可能是老数据，使用 CrawledAt 作为后备
			if resp.GetCrawledAt() == 0 {
				continue
			}
			if now-resp.GetCrawledAt() <= threshold {
				continue
			}
		} else {
			// 使用开始时间判断
			var started int64
			if _, err := fmt.Sscanf(startedStr, "%d", &started); err != nil {
				continue
			}
			if now-started <= threshold {
				continue
			}
		}

		// 递增重试计数
		resp.RetryCount++
		isDead := resp.RetryCount > DefaultMaxRetries

		// 序列化新结果
		newData, err := protojson.Marshal(&resp)
		if err != nil {
			continue
		}

		isDeadStr := "0"
		if isDead {
			isDeadStr = "1"
		}

		// 使用 Lua 脚本原子性地执行
		res, err := rescueResultScript.Run(ctx, c.rdb,
			[]string{KeyResultProcessingQueue, KeyResultQueue, KeyResultStartedHash, KeyResultDeadLetter},
			raw, taskID, string(newData), isDeadStr,
		).Int()
		if err != nil {
			continue
		}
		if res == 1 {
			if isDead {
				result.DeadLeter++
			} else {
				result.Rescued++
			}
		}
	}

	return result, nil
}

// DeduplicateQueue 清理任务队列中的重复任务，保留每个 task_id 的第一个条目。
// 返回移除的重复任务数量。
func (c *Client) DeduplicateQueue(ctx context.Context) (int, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client is not initialized")
	}

	// 获取队列中所有任务
	tasksRaw, err := c.rdb.LRange(ctx, KeyTaskQueue, 0, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("lrange tasks: %w", err)
	}
	if len(tasksRaw) == 0 {
		return 0, nil
	}

	// 跟踪已见过的 task_id
	seen := make(map[string]bool)
	duplicates := []string{}

	for _, raw := range tasksRaw {
		var task pb.CrawlRequest
		if err := protojson.Unmarshal([]byte(raw), &task); err != nil {
			continue
		}
		taskID := task.GetTaskId()
		if taskID == "" {
			continue
		}

		if seen[taskID] {
			// 已见过，标记为重复
			duplicates = append(duplicates, raw)
		} else {
			seen[taskID] = true
		}
	}

	// 移除所有重复项
	removed := 0
	for _, raw := range duplicates {
		count, err := c.rdb.LRem(ctx, KeyTaskQueue, 1, raw).Result()
		if err == nil && count > 0 {
			removed++
		}
	}

	return removed, nil
}

// RemoveFromPendingSet 从 pending set 中移除指定的 task_id。
// 用于删除任务时清理残留。
func (c *Client) RemoveFromPendingSet(ctx context.Context, taskID string) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}
	if taskID == "" {
		return errors.New("task id is empty")
	}
	return c.rdb.SRem(ctx, KeyTaskPendingSet, taskID).Err()
}

// ============================================================================
// 幂等性检查
// ============================================================================

// MarkProcessed 标记 task_id 为已处理（用于幂等性检查）。
// TTL 用于自动清理过期记录。
func (c *Client) MarkProcessed(ctx context.Context, taskID string, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}
	if taskID == "" {
		return errors.New("task id is empty")
	}
	// SADD + EXPIRE 不是原子的，但对于幂等性检查足够
	if err := c.rdb.SAdd(ctx, KeyProcessedSet, taskID).Err(); err != nil {
		return fmt.Errorf("sadd processed: %w", err)
	}
	// 每次添加后刷新 TTL
	c.rdb.Expire(ctx, KeyProcessedSet, ttl)
	return nil
}

// IsProcessed 检查 task_id 是否已处理过。
func (c *Client) IsProcessed(ctx context.Context, taskID string) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("redis client is not initialized")
	}
	if taskID == "" {
		return false, errors.New("task id is empty")
	}
	return c.rdb.SIsMember(ctx, KeyProcessedSet, taskID).Result()
}

// ============================================================================
// 启动恢复 (Startup Recovery)
// ============================================================================

// RecoverOrphanedResults 恢复启动时残留在 processing 队列中的结果
// 与 RescueStuckResults 不同，此方法:
// 1. 不递增 RetryCount (不惩罚任务)
// 2. 不移入死信队列
// 3. 用于服务启动时的一次性恢复
// 返回恢复的结果数量
func (c *Client) RecoverOrphanedResults(ctx context.Context) (int, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client is not initialized")
	}

	recovered := 0

	for {
		// 使用 RPOPLPUSH 原子移动 (从 processing 尾部取，放入 results 头部)
		result, err := c.rdb.RPopLPush(ctx, KeyResultProcessingQueue, KeyResultQueue).Result()
		if errors.Is(err, redis.Nil) {
			break // 队列已空
		}
		if err != nil {
			return recovered, fmt.Errorf("rpoplpush: %w", err)
		}

		// 解析获取 task_id，清理 started hash
		var resp pb.CrawlResponse
		if err := protojson.Unmarshal([]byte(result), &resp); err == nil && resp.GetTaskId() != "" {
			c.rdb.HDel(ctx, KeyResultStartedHash, resp.GetTaskId())
		}

		recovered++
	}

	return recovered, nil
}

// RecoverOrphanedTasks 恢复启动时残留在 processing 队列中的任务
// 与 RescueStuckTasks 不同，此方法:
// 1. 不递增 RetryCount (不惩罚任务)
// 2. 不移入死信队列
// 3. 用于服务启动时的一次性恢复
// 返回恢复的任务数量
func (c *Client) RecoverOrphanedTasks(ctx context.Context) (int, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client is not initialized")
	}

	recovered := 0

	for {
		// 使用 RPOPLPUSH 原子移动 (从 processing 尾部取，放入 tasks 头部)
		result, err := c.rdb.RPopLPush(ctx, KeyTaskProcessingQueue, KeyTaskQueue).Result()
		if errors.Is(err, redis.Nil) {
			break // 队列已空
		}
		if err != nil {
			return recovered, fmt.Errorf("rpoplpush: %w", err)
		}

		// 解析获取 task_id，清理 started hash
		var req pb.CrawlRequest
		if err := protojson.Unmarshal([]byte(result), &req); err == nil && req.GetTaskId() != "" {
			c.rdb.HDel(ctx, KeyTaskStartedHash, req.GetTaskId())
		}

		recovered++
	}

	return recovered, nil
}

// ============================================================================
// 队列统计
// ============================================================================

// QueueStats 队列统计信息
type QueueStats struct {
	// Task Queue
	TaskQueueLen       int64 // 待处理任务数
	TaskProcessingLen  int64 // 处理中任务数
	TaskDeadLetterLen  int64 // 死信任务数
	TaskPendingSetSize int64 // 去重集合大小

	// Result Queue
	ResultQueueLen      int64 // 待处理结果数
	ResultProcessingLen int64 // 处理中结果数
	ResultDeadLetterLen int64 // 死信结果数

	// Processed
	ProcessedSetSize int64 // 已处理集合大小
}

// GetQueueStats 获取队列统计信息。
func (c *Client) GetQueueStats(ctx context.Context) (*QueueStats, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client is not initialized")
	}

	stats := &QueueStats{}

	// Task queues
	if v, err := c.rdb.LLen(ctx, KeyTaskQueue).Result(); err == nil {
		stats.TaskQueueLen = v
	}
	if v, err := c.rdb.LLen(ctx, KeyTaskProcessingQueue).Result(); err == nil {
		stats.TaskProcessingLen = v
	}
	if v, err := c.rdb.LLen(ctx, KeyTaskDeadLetter).Result(); err == nil {
		stats.TaskDeadLetterLen = v
	}
	if v, err := c.rdb.SCard(ctx, KeyTaskPendingSet).Result(); err == nil {
		stats.TaskPendingSetSize = v
	}

	// Result queues
	if v, err := c.rdb.LLen(ctx, KeyResultQueue).Result(); err == nil {
		stats.ResultQueueLen = v
	}
	if v, err := c.rdb.LLen(ctx, KeyResultProcessingQueue).Result(); err == nil {
		stats.ResultProcessingLen = v
	}
	if v, err := c.rdb.LLen(ctx, KeyResultDeadLetter).Result(); err == nil {
		stats.ResultDeadLetterLen = v
	}

	// Processed set
	if v, err := c.rdb.SCard(ctx, KeyProcessedSet).Result(); err == nil {
		stats.ProcessedSetSize = v
	}

	return stats, nil
}
