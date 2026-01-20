// Package analyzer provides IP liquidity analysis components.
package analyzer

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"animetop/proto/pb"

	"github.com/redis/go-redis/v9"
)

const (
	// ItemKeyPrefix is the Redis key prefix for item state tracking
	ItemKeyPrefix = "animetop:item"

	// DefaultItemTTLAvailable 是 on_sale 商品的默认 TTL (24 小时)
	DefaultItemTTLAvailable = 24 * time.Hour

	// DefaultItemTTLSold 是 sold 商品的默认 TTL (48 小时)
	// 需要覆盖冷门 IP 的可见范围停留时间 (~33 小时)
	DefaultItemTTLSold = 48 * time.Hour
)

// TrackedItemState represents the stored state of an item in Redis (v2 state machine)
type TrackedItemState struct {
	Status    string // "available" or "sold"
	Price     int32
	FirstSeen int64 // Unix timestamp
	LastSeen  int64 // Unix timestamp
}

// TransitionType represents the type of state transition
type TransitionType string

const (
	TransitionNewListing  TransitionType = "new_listing"  // First time seeing this item as on_sale
	TransitionNewSold     TransitionType = "new_sold"     // First time seeing this item as sold (sold between crawls)
	TransitionSold        TransitionType = "sold"         // Status changed from available to sold
	TransitionPriceChange TransitionType = "price_change" // Price changed while still on_sale
	TransitionRelisted    TransitionType = "relisted"     // Rare: item went from sold back to on_sale
)

// StateTransition represents a single item state change
type StateTransition struct {
	SourceID string
	Type     TransitionType
	OldPrice int32
	NewPrice int32
	IpID     uint64
}

// StateMachine tracks item state changes using Redis HASH
type StateMachine struct {
	rdb          *redis.Client
	ttlAvailable time.Duration // TTL for on_sale items
	ttlSold      time.Duration // TTL for sold items
}

// NewStateMachine creates a new StateMachine instance
func NewStateMachine(rdb *redis.Client, ttlAvailable, ttlSold time.Duration) *StateMachine {
	if ttlAvailable == 0 {
		ttlAvailable = DefaultItemTTLAvailable
	}
	if ttlSold == 0 {
		ttlSold = DefaultItemTTLSold
	}
	return &StateMachine{
		rdb:          rdb,
		ttlAvailable: ttlAvailable,
		ttlSold:      ttlSold,
	}
}

// getTTLForStatus 根据商品状态返回对应的 TTL
func (sm *StateMachine) getTTLForStatus(status string) time.Duration {
	if status == "sold" {
		return sm.ttlSold
	}
	return sm.ttlAvailable
}

// itemKey returns the Redis key for an item's state
// Format: animetop:item:{ip_id}:{source_id}
func (sm *StateMachine) itemKey(ipID uint64, sourceID string) string {
	return fmt.Sprintf("%s:%d:%s", ItemKeyPrefix, ipID, sourceID)
}

// statusToString converts ItemStatus to string for storage
func statusToString(status pb.ItemStatus) string {
	if status == pb.ItemStatus_ITEM_STATUS_SOLD {
		return "sold"
	}
	return "available"
}

// GetItemState retrieves the current state of an item
func (sm *StateMachine) GetItemState(ctx context.Context, ipID uint64, sourceID string) (*TrackedItemState, error) {
	key := sm.itemKey(ipID, sourceID)

	result, err := sm.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("hgetall: %w", err)
	}

	if len(result) == 0 {
		return nil, nil // Item doesn't exist
	}

	state := &TrackedItemState{}
	if status, ok := result["status"]; ok {
		state.Status = status
	}

	if priceStr, ok := result["price"]; ok {
		if price, err := strconv.ParseInt(priceStr, 10, 32); err == nil {
			state.Price = int32(price)
		}
	}

	if firstSeenStr, ok := result["first_seen"]; ok {
		state.FirstSeen, _ = strconv.ParseInt(firstSeenStr, 10, 64)
	}

	if lastSeenStr, ok := result["last_seen"]; ok {
		state.LastSeen, _ = strconv.ParseInt(lastSeenStr, 10, 64)
	}

	return state, nil
}

// UpdateItemState updates or creates an item's state and returns any transition
func (sm *StateMachine) UpdateItemState(ctx context.Context, ipID uint64, item *pb.Item) (*StateTransition, error) {
	if item == nil || item.SourceId == "" {
		return nil, nil
	}

	key := sm.itemKey(ipID, item.SourceId)
	now := time.Now().Unix()
	newStatus := statusToString(item.Status)

	// Get current state
	oldState, err := sm.GetItemState(ctx, ipID, item.SourceId)
	if err != nil {
		return nil, err
	}

	// Determine transition
	var transition *StateTransition

	if oldState == nil {
		// New item
		if item.Status == pb.ItemStatus_ITEM_STATUS_ON_SALE {
			transition = &StateTransition{
				SourceID: item.SourceId,
				Type:     TransitionNewListing,
				NewPrice: item.Price,
				IpID:     ipID,
			}
		} else if item.Status == pb.ItemStatus_ITEM_STATUS_SOLD {
			// First time seeing as sold - this item was listed and sold between crawls
			// Count as outflow (new_sold)
			transition = &StateTransition{
				SourceID: item.SourceId,
				Type:     TransitionNewSold,
				NewPrice: item.Price,
				IpID:     ipID,
			}
		}

		// Create new state
		pipe := sm.rdb.Pipeline()
		pipe.HSet(ctx, key,
			"status", newStatus,
			"price", item.Price,
			"first_seen", now,
			"last_seen", now,
		)
		pipe.Expire(ctx, key, sm.getTTLForStatus(newStatus))
		_, err = pipe.Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("create item state: %w", err)
		}
	} else {
		// Existing item - check for transitions
		if oldState.Status == "available" && newStatus == "sold" {
			// Sold transition
			transition = &StateTransition{
				SourceID: item.SourceId,
				Type:     TransitionSold,
				OldPrice: oldState.Price,
				NewPrice: item.Price,
				IpID:     ipID,
			}
		} else if oldState.Status == "sold" && newStatus == "available" {
			// Relisted (rare)
			transition = &StateTransition{
				SourceID: item.SourceId,
				Type:     TransitionRelisted,
				OldPrice: oldState.Price,
				NewPrice: item.Price,
				IpID:     ipID,
			}
		} else if oldState.Status == "available" && newStatus == "available" && oldState.Price != item.Price {
			// Price change
			transition = &StateTransition{
				SourceID: item.SourceId,
				Type:     TransitionPriceChange,
				OldPrice: oldState.Price,
				NewPrice: item.Price,
				IpID:     ipID,
			}
		}

		// Update state
		pipe := sm.rdb.Pipeline()
		pipe.HSet(ctx, key,
			"status", newStatus,
			"price", item.Price,
			"last_seen", now,
		)
		pipe.Expire(ctx, key, sm.getTTLForStatus(newStatus))
		_, err = pipe.Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("update item state: %w", err)
		}
	}

	return transition, nil
}

// pipelineBatchSize is the max commands per pipeline batch to avoid timeout
// Increased from 200 to 500 to reduce Redis round-trips (300 items = 1 batch instead of 2)
const pipelineBatchSize = 500

// ProcessItemsBatch processes a batch of items and returns all transitions
// Optimized version using Redis Pipeline with batching to reduce network round-trips
func (sm *StateMachine) ProcessItemsBatch(ctx context.Context, ipID uint64, items []*pb.Item) ([]StateTransition, error) {
	if len(items) == 0 {
		return nil, nil
	}

	start := time.Now()

	// Filter valid items and build keys
	type itemInfo struct {
		item *pb.Item
		key  string
	}
	validItems := make([]itemInfo, 0, len(items))
	for _, item := range items {
		if item == nil || item.SourceId == "" {
			continue
		}
		validItems = append(validItems, itemInfo{
			item: item,
			key:  sm.itemKey(ipID, item.SourceId),
		})
	}

	if len(validItems) == 0 {
		return nil, nil
	}

	// Step 1: Batch HGetAll to fetch all current states (in batches)
	oldStates := make([]*TrackedItemState, len(validItems))
	for batchStart := 0; batchStart < len(validItems); batchStart += pipelineBatchSize {
		batchEnd := batchStart + pipelineBatchSize
		if batchEnd > len(validItems) {
			batchEnd = len(validItems)
		}
		batch := validItems[batchStart:batchEnd]

		pipe := sm.rdb.Pipeline()
		cmds := make([]*redis.MapStringStringCmd, len(batch))
		for i, info := range batch {
			cmds[i] = pipe.HGetAll(ctx, info.key)
		}
		_, err := pipe.Exec(ctx)
		if err != nil && err != redis.Nil {
			return nil, fmt.Errorf("batch hgetall: %w", err)
		}

		for i, cmd := range cmds {
			result, _ := cmd.Result()
			oldStates[batchStart+i] = sm.parseHashResult(result)
		}
	}
	step1Duration := time.Since(start)

	// Step 2: Process transitions and collect updates
	now := time.Now().Unix()
	var transitions []StateTransition

	type updateInfo struct {
		key       string
		isNew     bool
		status    string
		price     int32
		firstSeen int64
		lastSeen  int64
	}
	updates := make([]updateInfo, 0, len(validItems))

	for i, info := range validItems {
		oldState := oldStates[i]
		newStatus := statusToString(info.item.Status)

		// Determine transition
		transition := sm.determineTransition(ipID, info.item, oldState, newStatus)
		if transition != nil {
			transitions = append(transitions, *transition)
		}

		// Collect update info
		if oldState == nil {
			updates = append(updates, updateInfo{
				key:       info.key,
				isNew:     true,
				status:    newStatus,
				price:     info.item.Price,
				firstSeen: now,
				lastSeen:  now,
			})
		} else {
			updates = append(updates, updateInfo{
				key:       info.key,
				isNew:     false,
				status:    newStatus,
				price:     info.item.Price,
				firstSeen: oldState.FirstSeen,
				lastSeen:  now,
			})
		}
	}
	step2Duration := time.Since(start) - step1Duration

	// Step 3: Batch HSet + Expire to update all states (in batches)
	for batchStart := 0; batchStart < len(updates); batchStart += pipelineBatchSize {
		batchEnd := batchStart + pipelineBatchSize
		if batchEnd > len(updates) {
			batchEnd = len(updates)
		}
		batch := updates[batchStart:batchEnd]

		pipe := sm.rdb.Pipeline()
		for _, u := range batch {
			if u.isNew {
				pipe.HSet(ctx, u.key,
					"status", u.status,
					"price", u.price,
					"first_seen", u.firstSeen,
					"last_seen", u.lastSeen,
				)
			} else {
				pipe.HSet(ctx, u.key,
					"status", u.status,
					"price", u.price,
					"last_seen", u.lastSeen,
				)
			}
			pipe.Expire(ctx, u.key, sm.getTTLForStatus(u.status))
		}
		_, err := pipe.Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("batch hset: %w", err)
		}
	}
	totalDuration := time.Since(start)

	// Log performance metrics
	slog.Debug("ProcessItemsBatch completed",
		slog.Uint64("ip_id", ipID),
		slog.Int("items", len(validItems)),
		slog.Int("transitions", len(transitions)),
		slog.Duration("step1_hgetall", step1Duration),
		slog.Duration("step2_process", step2Duration),
		slog.Duration("step3_hset", totalDuration-step1Duration-step2Duration),
		slog.Duration("total", totalDuration))

	return transitions, nil
}

// parseHashResult parses HGetAll result into TrackedItemState
func (sm *StateMachine) parseHashResult(result map[string]string) *TrackedItemState {
	if len(result) == 0 {
		return nil
	}

	state := &TrackedItemState{}
	if status, ok := result["status"]; ok {
		state.Status = status
	}
	if priceStr, ok := result["price"]; ok {
		if price, err := strconv.ParseInt(priceStr, 10, 32); err == nil {
			state.Price = int32(price)
		}
	}
	if firstSeenStr, ok := result["first_seen"]; ok {
		state.FirstSeen, _ = strconv.ParseInt(firstSeenStr, 10, 64)
	}
	if lastSeenStr, ok := result["last_seen"]; ok {
		state.LastSeen, _ = strconv.ParseInt(lastSeenStr, 10, 64)
	}
	return state
}

// determineTransition determines the state transition for an item
func (sm *StateMachine) determineTransition(ipID uint64, item *pb.Item, oldState *TrackedItemState, newStatus string) *StateTransition {
	if oldState == nil {
		// New item
		if item.Status == pb.ItemStatus_ITEM_STATUS_ON_SALE {
			return &StateTransition{
				SourceID: item.SourceId,
				Type:     TransitionNewListing,
				NewPrice: item.Price,
				IpID:     ipID,
			}
		} else if item.Status == pb.ItemStatus_ITEM_STATUS_SOLD {
			// First time seeing as sold - this item was listed and sold between crawls
			// Count as outflow (new_sold)
			return &StateTransition{
				SourceID: item.SourceId,
				Type:     TransitionNewSold,
				NewPrice: item.Price,
				IpID:     ipID,
			}
		}
		return nil
	}

	// Existing item - check for transitions
	if oldState.Status == "available" && newStatus == "sold" {
		return &StateTransition{
			SourceID: item.SourceId,
			Type:     TransitionSold,
			OldPrice: oldState.Price,
			NewPrice: item.Price,
			IpID:     ipID,
		}
	}
	if oldState.Status == "sold" && newStatus == "available" {
		return &StateTransition{
			SourceID: item.SourceId,
			Type:     TransitionRelisted,
			OldPrice: oldState.Price,
			NewPrice: item.Price,
			IpID:     ipID,
		}
	}
	if oldState.Status == "available" && newStatus == "available" && oldState.Price != item.Price {
		return &StateTransition{
			SourceID: item.SourceId,
			Type:     TransitionPriceChange,
			OldPrice: oldState.Price,
			NewPrice: item.Price,
			IpID:     ipID,
		}
	}
	return nil
}

// TransitionSummary summarizes the transitions for statistics
type TransitionSummary struct {
	NewListings  int
	Sold         int
	PriceChanges int
	Relisted     int
}

// SummarizeTransitions creates a summary from a list of transitions
func SummarizeTransitions(transitions []StateTransition) TransitionSummary {
	var summary TransitionSummary
	for _, t := range transitions {
		switch t.Type {
		case TransitionNewListing:
			summary.NewListings++
		case TransitionSold, TransitionNewSold:
			// Both sold (status change) and new_sold (first seen as sold) count as outflow
			summary.Sold++
		case TransitionPriceChange:
			summary.PriceChanges++
		case TransitionRelisted:
			summary.Relisted++
		}
	}
	return summary
}

// ClearItemState removes an item's state (used for testing or cleanup)
func (sm *StateMachine) ClearItemState(ctx context.Context, ipID uint64, sourceID string) error {
	key := sm.itemKey(ipID, sourceID)
	return sm.rdb.Del(ctx, key).Err()
}

// ClearAllItems removes all item states for an IP (used for testing or reset)
func (sm *StateMachine) ClearAllItems(ctx context.Context, ipID uint64) error {
	pattern := fmt.Sprintf("%s:%d:*", ItemKeyPrefix, ipID)
	var cursor uint64
	var keys []string

	for {
		var err error
		var batch []string
		batch, cursor, err = sm.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("scan keys: %w", err)
		}
		keys = append(keys, batch...)
		if cursor == 0 {
			break
		}
	}

	if len(keys) > 0 {
		if err := sm.rdb.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("delete keys: %w", err)
		}
	}

	return nil
}

// GetItemCount returns the approximate count of tracked items for an IP
func (sm *StateMachine) GetItemCount(ctx context.Context, ipID uint64) (int64, error) {
	pattern := fmt.Sprintf("%s:%d:*", ItemKeyPrefix, ipID)
	var count int64
	var cursor uint64

	for {
		var err error
		var batch []string
		batch, cursor, err = sm.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return 0, fmt.Errorf("scan keys: %w", err)
		}
		count += int64(len(batch))
		if cursor == 0 {
			break
		}
	}

	return count, nil
}

