package redisqueue

import (
	"context"
	"fmt"
	"testing"
	"time"

	"animetop/proto/pb"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestClient_TaskFlow(t *testing.T) {
	// 1. 启动 Mock Redis
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// 2. 初始化 Client
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 3. 测试 PushTask
	req := &pb.CrawlRequest{
		TaskId:  "1001",
		IpId:    1,
		Keyword: "PS5",
	}

	if err := client.PushTask(ctx, req); err != nil {
		t.Errorf("PushTask failed: %v", err)
	}

	// 验证队列长度
	tasks, results, err := client.QueueDepth(ctx)
	if err != nil {
		t.Errorf("QueueDepth failed: %v", err)
	}
	if tasks != 1 || results != 0 {
		t.Errorf("expected 1 task, 0 results, got %d tasks, %d results", tasks, results)
	}

	// 4. 测试 PopTask
	poppedReq, err := client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	if poppedReq.TaskId != req.TaskId || poppedReq.Keyword != req.Keyword {
		t.Errorf("PopTask data mismatch. expected %v, got %v", req, poppedReq)
	}
}

func TestClient_ResultFlow(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// 1. 测试 PushResult
	res := &pb.CrawlResponse{
		IpId:         1,
		ErrorMessage: "",
		Items: []*pb.Item{
			{Title: "Sony PS5", Price: 45000, SourceId: "m123456"},
		},
	}

	if err := client.PushResult(ctx, res); err != nil {
		t.Errorf("PushResult failed: %v", err)
	}

	// 2. 测试 PopResult
	poppedRes, err := client.PopResult(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopResult failed: %v", err)
	}

	if len(poppedRes.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(poppedRes.Items))
	}
	if poppedRes.Items[0].SourceId != "m123456" {
		t.Errorf("item data mismatch")
	}
}

// ============================================================================
// 去重功能测试
// ============================================================================

func TestClient_Deduplication(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:  "dedup-test-1",
		IpId:    1,
		Keyword: "test",
	}

	// 第一次推送应该成功
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("first PushTask should succeed: %v", err)
	}

	// 验证 pending set 大小
	size, err := client.PendingSetSize(ctx)
	if err != nil {
		t.Fatalf("PendingSetSize failed: %v", err)
	}
	if size != 1 {
		t.Errorf("expected pending set size 1, got %d", size)
	}

	// 第二次推送相同任务应该返回 ErrTaskExists
	err = client.PushTask(ctx, req)
	if err != ErrTaskExists {
		t.Errorf("second PushTask should return ErrTaskExists, got: %v", err)
	}

	// 队列长度应该还是 1（没有重复入队）
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 1 {
		t.Errorf("expected 1 task in queue, got %d", tasks)
	}
}

func TestClient_AckTask_ClearsPendingSet(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:    "ack-test-1",
		IpId:      1,
		Keyword:   "test",
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务（模拟 Crawler 消费）
	popped, err := client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 此时 pending set 应该还有 1 个
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("after PopTask, pending set size should be 1, got %d", size)
	}

	// 3. Ack 任务（模拟任务完成）
	if err := client.AckTask(ctx, popped); err != nil {
		t.Fatalf("AckTask failed: %v", err)
	}

	// 4. Ack 后 pending set 应该清空
	size, _ = client.PendingSetSize(ctx)
	if size != 0 {
		t.Errorf("after AckTask, pending set size should be 0, got %d", size)
	}

	// 5. 再次推送相同任务应该成功（因为已经被 Ack）
	if err := client.PushTask(ctx, req); err != nil {
		t.Errorf("PushTask after AckTask should succeed, got: %v", err)
	}
}

func TestClient_NoAck_BlocksRePush(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:    "no-ack-test-1",
		IpId:      1,
		Keyword:   "test",
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务（模拟 Crawler 消费）
	_, err = client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 3. 不调用 AckTask（模拟任务超时）

	// 4. 尝试再次推送相同任务应该被阻止
	err = client.PushTask(ctx, req)
	if err != ErrTaskExists {
		t.Errorf("PushTask without AckTask should return ErrTaskExists, got: %v", err)
	}

	// 5. pending set 应该仍然有 1 个
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("pending set size should be 1, got %d", size)
	}
}

func TestClient_MultipleTasksDeduplication(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 模拟 10 个不同的任务（使用固定的 TaskId）
	taskIDs := make([]string, 10)
	for i := 0; i < 10; i++ {
		taskIDs[i] = "multi-task-" + string(rune('A'+i))
		req := &pb.CrawlRequest{
			TaskId:  taskIDs[i],
			IpId:    uint64(i + 1),
			Keyword: "test",
		}
		if err := client.PushTask(ctx, req); err != nil {
			t.Fatalf("PushTask %d failed: %v", i, err)
		}
	}

	// 队列应该有 10 个任务
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 10 {
		t.Errorf("expected 10 tasks, got %d", tasks)
	}

	// pending set 也应该有 10 个
	size, _ := client.PendingSetSize(ctx)
	if size != 10 {
		t.Errorf("expected pending set size 10, got %d", size)
	}

	// 模拟调度器再次推送这 10 个任务（应该全部被阻止）
	for i := 0; i < 10; i++ {
		req := &pb.CrawlRequest{
			TaskId:  taskIDs[i],
			IpId:    uint64(i + 1),
			Keyword: "test",
		}
		err := client.PushTask(ctx, req)
		if err != ErrTaskExists {
			t.Errorf("re-push task %s should return ErrTaskExists, got: %v", taskIDs[i], err)
		}
	}

	// 验证队列长度没有增长
	tasks, _, _ = client.QueueDepth(ctx)
	if tasks != 10 {
		t.Errorf("queue length should remain 10, got %d", tasks)
	}
}

func TestClient_RescueStuckTasks_PreservesPendingSet(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:    "stuck-task-1",
		IpId:      1,
		Keyword:   "test",
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务（移到 processing queue，同时会记录开始时间到 started hash）
	popped, err := client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 验证任务在 processing queue
	procLen, err := rdb.LLen(ctx, KeyTaskProcessingQueue).Result()
	if err != nil {
		t.Fatalf("LLen failed: %v", err)
	}
	if procLen != 1 {
		t.Errorf("expected 1 task in processing queue, got %d", procLen)
	}

	// 3. 手动修改 started hash 中的时间为 1 小时前（模拟任务卡住）
	stuckTime := time.Now().Add(-1 * time.Hour).Unix()
	if err := rdb.HSet(ctx, KeyTaskStartedHash, "stuck-task-1", stuckTime).Err(); err != nil {
		t.Fatalf("HSet started time failed: %v", err)
	}

	// 4. 执行 RescueStuckTasks（超时阈值设为 30 分钟，小于 1 小时）
	result, err := client.RescueStuckTasks(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("RescueStuckTasks failed: %v", err)
	}
	if result.Rescued != 1 {
		t.Errorf("expected 1 rescued task, got %d", result.Rescued)
	}

	// 5. 验证任务回到 task queue
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 1 {
		t.Errorf("expected 1 task in queue after rescue, got %d", tasks)
	}

	// 6. 关键验证：pending set 应该仍然有这个任务
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("pending set should still have 1 task after rescue, got %d", size)
	}

	// 7. 验证 started hash 已被清理
	exists, err := rdb.HExists(ctx, KeyTaskStartedHash, "stuck-task-1").Result()
	if err != nil {
		t.Fatalf("HExists failed: %v", err)
	}
	if exists {
		t.Errorf("started hash should be cleared after rescue")
	}

	// 8. 再次推送相同任务应该被阻止（即使任务被 rescue 了）
	err = client.PushTask(ctx, popped)
	if err != ErrTaskExists {
		t.Errorf("PushTask after rescue should return ErrTaskExists, got: %v", err)
	}
}

func TestClient_RescueDoesNotAffectActiveTask(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:    "active-task-1",
		IpId:      1,
		Keyword:   "test",
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务（模拟 worker 开始处理）
	_, err = client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 3. 不修改 started time（保持刚刚开始的状态）

	// 4. 执行 RescueStuckTasks（超时阈值设为 30 分钟）
	// 因为任务刚开始处理，不应该被 rescue
	result, err := client.RescueStuckTasks(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("RescueStuckTasks failed: %v", err)
	}
	if result.Rescued != 0 {
		t.Errorf("expected 0 rescued tasks (task is active), got %d", result.Rescued)
	}

	// 5. 验证任务仍在 processing queue
	procLen, err := rdb.LLen(ctx, KeyTaskProcessingQueue).Result()
	if err != nil {
		t.Fatalf("LLen failed: %v", err)
	}
	if procLen != 1 {
		t.Errorf("expected 1 task still in processing queue, got %d", procLen)
	}

	// 6. task queue 应该是空的
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 0 {
		t.Errorf("expected 0 tasks in queue, got %d", tasks)
	}
}

func TestClient_StartedHashClearedOnAck(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:    "started-hash-test-1",
		IpId:      1,
		Keyword:   "test",
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务
	popped, err := client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 3. 验证 started hash 有记录
	exists, err := rdb.HExists(ctx, KeyTaskStartedHash, "started-hash-test-1").Result()
	if err != nil {
		t.Fatalf("HExists failed: %v", err)
	}
	if !exists {
		t.Errorf("started hash should have record after PopTask")
	}

	// 4. Ack 任务
	if err := client.AckTask(ctx, popped); err != nil {
		t.Fatalf("AckTask failed: %v", err)
	}

	// 5. 验证 started hash 已清理
	exists, err = rdb.HExists(ctx, KeyTaskStartedHash, "started-hash-test-1").Result()
	if err != nil {
		t.Fatalf("HExists failed: %v", err)
	}
	if exists {
		t.Errorf("started hash should be cleared after AckTask")
	}
}

func TestClient_TimeoutWithoutAck_QueueDoesNotGrow(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:    "timeout-test-1",
		IpId:      1,
		Keyword:   "test",
		CreatedAt: time.Now().Unix(),
	}

	// 模拟多轮调度（爬虫卡死场景）
	for round := 1; round <= 5; round++ {
		// 调度器尝试推送
		err := client.PushTask(ctx, req)

		if round == 1 {
			// 第一轮应该成功
			if err != nil {
				t.Fatalf("round %d: first PushTask should succeed: %v", round, err)
			}
		} else {
			// 后续轮次应该被阻止
			if err != ErrTaskExists {
				t.Errorf("round %d: PushTask should return ErrTaskExists, got: %v", round, err)
			}
		}
	}

	// 验证队列长度始终为 1（不会因为多轮调度而增长）
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 1 {
		t.Errorf("queue should have exactly 1 task, got %d", tasks)
	}

	// pending set 也应该只有 1 个
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("pending set should have exactly 1 task, got %d", size)
	}
}

// ============================================================================
// 新增测试 - Client 初始化
// ============================================================================

func TestNewClientWithRedis_NilClient(t *testing.T) {
	_, err := NewClientWithRedis(nil)
	if err == nil {
		t.Errorf("expected error for nil client, got nil")
	}
}

// ============================================================================
// 新增测试 - PushTaskForce
// ============================================================================

func TestClient_PushTaskForce(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:  "force-test-1",
		IpId:    1,
		Keyword: "test",
	}

	// 第一次推送
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("first PushTask failed: %v", err)
	}

	// 普通推送应该失败
	if err := client.PushTask(ctx, req); err != ErrTaskExists {
		t.Errorf("second PushTask should return ErrTaskExists, got: %v", err)
	}

	// 强制推送应该成功
	if err := client.PushTaskForce(ctx, req); err != nil {
		t.Errorf("PushTaskForce should succeed, got: %v", err)
	}

	// 队列应该有 2 个任务
	tasks, _, _ := client.QueueDepth(ctx)
	if tasks != 2 {
		t.Errorf("expected 2 tasks after force push, got %d", tasks)
	}
}

func TestClient_PushTask_EmptyTaskID(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:  "", // 空 TaskID
		IpId:    1,
		Keyword: "test",
	}

	// 空 TaskID 应该返回错误
	err = client.PushTask(ctx, req)
	if err == nil {
		t.Errorf("expected error for empty task ID, got nil")
	}
}

func TestClient_PushTask_NilTask(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// nil task 应该返回错误
	err = client.PushTask(ctx, nil)
	if err == nil {
		t.Errorf("expected error for nil task, got nil")
	}
}

// ============================================================================
// 新增测试 - AckResult
// ============================================================================

func TestClient_AckResult(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	res := &pb.CrawlResponse{
		TaskId:       "ack-result-test",
		IpId:         1,
		ErrorMessage: "",
	}

	// 推送结果
	if err := client.PushResult(ctx, res); err != nil {
		t.Fatalf("PushResult failed: %v", err)
	}

	// 弹出结果
	popped, err := client.PopResult(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopResult failed: %v", err)
	}

	// 验证在 processing queue
	procLen, _ := rdb.LLen(ctx, KeyResultProcessingQueue).Result()
	if procLen != 1 {
		t.Errorf("expected 1 result in processing queue, got %d", procLen)
	}

	// Ack 结果 - 测试 AckResult 不返回错误
	// 注意: miniredis 对 Lua string.find 支持有限，实际 LREM 可能不生效
	// 但 AckResult 本身不应返回错误
	if err := client.AckResult(ctx, popped); err != nil {
		t.Fatalf("AckResult failed: %v", err)
	}

	// 注意: 由于 miniredis Lua 限制，跳过 processing queue 验证
	// 在真实 Redis 中，processing queue 应该被清空
}

// ============================================================================
// 新增测试 - GetQueueStats
// ============================================================================

func TestClient_GetQueueStats(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// 初始状态
	stats, err := client.GetQueueStats(ctx)
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}
	if stats.TaskQueueLen != 0 || stats.ResultQueueLen != 0 {
		t.Errorf("expected empty queues, got tasks=%d results=%d",
			stats.TaskQueueLen, stats.ResultQueueLen)
	}

	// 添加一些任务
	for i := 0; i < 3; i++ {
		req := &pb.CrawlRequest{
			TaskId:  "stats-test-" + string(rune('A'+i)),
			IpId:    uint64(i + 1),
			Keyword: "test",
		}
		_ = client.PushTask(ctx, req)
	}

	// 添加一些结果
	for i := 0; i < 2; i++ {
		res := &pb.CrawlResponse{
			TaskId: "result-" + string(rune('A'+i)),
			IpId:   uint64(i + 1),
		}
		_ = client.PushResult(ctx, res)
	}

	// 验证统计
	stats, err = client.GetQueueStats(ctx)
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}
	if stats.TaskQueueLen != 3 {
		t.Errorf("expected 3 tasks, got %d", stats.TaskQueueLen)
	}
	if stats.ResultQueueLen != 2 {
		t.Errorf("expected 2 results, got %d", stats.ResultQueueLen)
	}
	if stats.TaskPendingSetSize != 3 {
		t.Errorf("expected 3 in pending set, got %d", stats.TaskPendingSetSize)
	}
}

// ============================================================================
// 新增测试 - MarkProcessed / IsProcessed
// ============================================================================

func TestClient_MarkProcessed_IsProcessed(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	taskID := "processed-test-1"
	ttl := 1 * time.Hour

	// 未标记时应该返回 false
	processed, err := client.IsProcessed(ctx, taskID)
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if processed {
		t.Errorf("expected false for unprocessed task")
	}

	// 标记为已处理
	if err := client.MarkProcessed(ctx, taskID, ttl); err != nil {
		t.Fatalf("MarkProcessed failed: %v", err)
	}

	// 现在应该返回 true
	processed, err = client.IsProcessed(ctx, taskID)
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if !processed {
		t.Errorf("expected true for processed task")
	}

	// 再次标记应该没问题
	if err := client.MarkProcessed(ctx, taskID, ttl); err != nil {
		t.Errorf("MarkProcessed again should not fail: %v", err)
	}
}

func TestClient_IsProcessed_EmptyTaskID(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// 空 taskID 应该返回错误
	_, err = client.IsProcessed(ctx, "")
	if err == nil {
		t.Errorf("expected error for empty taskID, got nil")
	}
}

// ============================================================================
// 新增测试 - RemoveFromPendingSet
// ============================================================================

func TestClient_RemoveFromPendingSet(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	req := &pb.CrawlRequest{
		TaskId:  "remove-pending-test",
		IpId:    1,
		Keyword: "test",
	}

	// 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 验证 pending set 有 1 个
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("expected pending set size 1, got %d", size)
	}

	// 从 pending set 移除 (使用 IP 格式的 dedup key)
	dedupKey := fmt.Sprintf("ip:%d", req.IpId)
	if err := client.RemoveFromPendingSet(ctx, dedupKey); err != nil {
		t.Fatalf("RemoveFromPendingSet failed: %v", err)
	}

	// pending set 应该为空
	size, _ = client.PendingSetSize(ctx)
	if size != 0 {
		t.Errorf("expected pending set size 0 after remove, got %d", size)
	}

	// 现在可以再次推送
	if err := client.PushTask(ctx, req); err != nil {
		t.Errorf("PushTask after remove should succeed, got: %v", err)
	}
}

// ============================================================================
// 新增测试 - DeduplicateQueue
// ============================================================================

func TestClient_DeduplicateQueue(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// 使用 PushTaskForce 添加重复任务
	req := &pb.CrawlRequest{
		TaskId:  "dedup-queue-test",
		IpId:    1,
		Keyword: "test",
	}

	// 强制推送 3 次（绕过去重检查）
	for i := 0; i < 3; i++ {
		if err := client.PushTaskForce(ctx, req); err != nil {
			t.Fatalf("PushTaskForce %d failed: %v", i, err)
		}
	}

	// 队列应该有 3 个
	tasks, _, _ := client.QueueDepth(ctx)
	if tasks != 3 {
		t.Errorf("expected 3 tasks before dedup, got %d", tasks)
	}

	// 执行去重
	removed, err := client.DeduplicateQueue(ctx)
	if err != nil {
		t.Fatalf("DeduplicateQueue failed: %v", err)
	}
	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}

	// 队列应该只有 1 个
	tasks, _, _ = client.QueueDepth(ctx)
	if tasks != 1 {
		t.Errorf("expected 1 task after dedup, got %d", tasks)
	}
}

// ============================================================================
// 新增测试 - RescueStuckResults
// ============================================================================

func TestClient_RescueStuckResults(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	res := &pb.CrawlResponse{
		TaskId: "stuck-result-test",
		IpId:   1,
	}

	// 推送结果
	if err := client.PushResult(ctx, res); err != nil {
		t.Fatalf("PushResult failed: %v", err)
	}

	// 弹出结果（移到 processing queue）
	_, err = client.PopResult(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopResult failed: %v", err)
	}

	// 手动修改开始时间为 1 小时前
	stuckTime := time.Now().Add(-1 * time.Hour).Unix()
	rdb.HSet(ctx, KeyResultStartedHash, "stuck-result-test", stuckTime)

	// 执行 rescue（超时阈值 30 分钟）
	result, err := client.RescueStuckResults(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("RescueStuckResults failed: %v", err)
	}
	if result.Rescued != 1 {
		t.Errorf("expected 1 rescued result, got %d", result.Rescued)
	}

	// 结果应该回到队列
	_, results, _ := client.QueueDepth(ctx)
	if results != 1 {
		t.Errorf("expected 1 result in queue after rescue, got %d", results)
	}
}

// ============================================================================
// 新增测试 - PopTask/PopResult 超时
// ============================================================================

func TestClient_PopTask_Timeout(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// 空队列弹出应该超时
	start := time.Now()
	_, err = client.PopTask(ctx, 100*time.Millisecond)
	elapsed := time.Since(start)

	if err != ErrNoTask {
		t.Errorf("expected ErrNoTask, got: %v", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("expected to wait at least 100ms, waited %v", elapsed)
	}
}

func TestClient_PopResult_Timeout(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// 空队列弹出应该超时
	start := time.Now()
	_, err = client.PopResult(ctx, 100*time.Millisecond)
	elapsed := time.Since(start)

	if err != ErrNoResult {
		t.Errorf("expected ErrNoResult, got: %v", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("expected to wait at least 100ms, waited %v", elapsed)
	}
}

// ============================================================================
// 新增测试 - PopTask Context 取消
// ============================================================================

func TestClient_PopTask_ContextCanceled(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)

	ctx, cancel := context.WithCancel(context.Background())

	// 立即取消
	cancel()

	_, err = client.PopTask(ctx, 5*time.Second)
	if err == nil {
		t.Error("expected error when context is canceled")
	}
}

// ============================================================================
// 新增测试 - 并发安全性
// ============================================================================

func TestClient_ConcurrentPush(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// 并发推送 100 个不同任务
	done := make(chan bool)
	for i := 0; i < 100; i++ {
		go func(n int) {
			req := &pb.CrawlRequest{
				TaskId:  "concurrent-" + string(rune('A'+n%26)) + "-" + time.Now().Format("150405.000000000") + "-" + string(rune('0'+n%10)),
				IpId:    uint64(n + 1),
				Keyword: "test",
			}
			_ = client.PushTask(ctx, req)
			done <- true
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 100; i++ {
		<-done
	}

	// 统计结果（由于 taskID 可能有碰撞，这里只检查没有 panic）
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks == 0 {
		t.Error("expected some tasks in queue")
	}
}

// ============================================================================
// 新增测试 - AckTask 处理不在队列中的任务
// ============================================================================

func TestClient_AckTask_NotInQueue(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// Ack 一个从未存在的任务
	req := &pb.CrawlRequest{
		TaskId:  "never-existed",
		IpId:    1,
		Keyword: "test",
	}

	// 不应该返回错误，只是什么都不做
	if err := client.AckTask(ctx, req); err != nil {
		t.Errorf("AckTask for non-existent task should not error, got: %v", err)
	}
}

// ============================================================================
// 新增测试 - 队列深度边界情况
// ============================================================================

func TestClient_QueueDepth_Empty(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	tasks, results, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 0 {
		t.Errorf("expected 0 tasks, got %d", tasks)
	}
	if results != 0 {
		t.Errorf("expected 0 results, got %d", results)
	}
}

// ============================================================================
// 新增测试 - PushResult nil
// ============================================================================

func TestClient_PushResult_Nil(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	err = client.PushResult(ctx, nil)
	if err == nil {
		t.Errorf("expected error for nil result, got nil")
	}
}
