package analyzer

import (
	"context"
	"testing"
	"time"

	"animetop/proto/pb"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupStateMachine(t *testing.T) (*StateMachine, func()) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr: s.Addr(),
	})

	sm := NewStateMachine(rdb, 24*time.Hour, 48*time.Hour)

	return sm, func() {
		rdb.Close()
		s.Close()
	}
}

func TestStateMachine_NewListing(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	// First time seeing an on_sale item
	item := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}

	transition, err := sm.UpdateItemState(ctx, ipID, item)
	if err != nil {
		t.Fatalf("UpdateItemState failed: %v", err)
	}

	if transition == nil {
		t.Fatal("expected a transition for new listing")
	}

	if transition.Type != TransitionNewListing {
		t.Errorf("expected TransitionNewListing, got %v", transition.Type)
	}

	if transition.NewPrice != 1000 {
		t.Errorf("expected new price 1000, got %d", transition.NewPrice)
	}
}

func TestStateMachine_NewSoldTransition(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	// First time seeing item as sold (was listed and sold between crawls)
	item := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_SOLD,
	}

	transition, err := sm.UpdateItemState(ctx, ipID, item)
	if err != nil {
		t.Fatalf("UpdateItemState failed: %v", err)
	}

	// Should return new_sold transition (counts as outflow)
	if transition == nil {
		t.Fatal("expected new_sold transition for first-seen sold item")
	}
	if transition.Type != TransitionNewSold {
		t.Errorf("expected transition type new_sold, got %v", transition.Type)
	}
	if transition.NewPrice != 1000 {
		t.Errorf("expected new price 1000, got %d", transition.NewPrice)
	}

	// Item state should be tracked
	state, err := sm.GetItemState(ctx, ipID, "m001")
	if err != nil {
		t.Fatalf("GetItemState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected item state to be tracked")
	}
	if state.Status != "sold" {
		t.Errorf("expected status 'sold', got %v", state.Status)
	}
}

func TestStateMachine_SoldTransition(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	// First: item is on_sale
	item1 := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}
	_, err := sm.UpdateItemState(ctx, ipID, item1)
	if err != nil {
		t.Fatalf("first UpdateItemState failed: %v", err)
	}

	// Then: item becomes sold
	item2 := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_SOLD,
	}
	transition, err := sm.UpdateItemState(ctx, ipID, item2)
	if err != nil {
		t.Fatalf("second UpdateItemState failed: %v", err)
	}

	if transition == nil {
		t.Fatal("expected a transition for sold item")
	}

	if transition.Type != TransitionSold {
		t.Errorf("expected TransitionSold, got %v", transition.Type)
	}
}

func TestStateMachine_PriceChange(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	// First: item at 1000
	item1 := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}
	_, err := sm.UpdateItemState(ctx, ipID, item1)
	if err != nil {
		t.Fatalf("first UpdateItemState failed: %v", err)
	}

	// Then: price drops to 800
	item2 := &pb.Item{
		SourceId: "m001",
		Price:    800,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}
	transition, err := sm.UpdateItemState(ctx, ipID, item2)
	if err != nil {
		t.Fatalf("second UpdateItemState failed: %v", err)
	}

	if transition == nil {
		t.Fatal("expected a transition for price change")
	}

	if transition.Type != TransitionPriceChange {
		t.Errorf("expected TransitionPriceChange, got %v", transition.Type)
	}

	if transition.OldPrice != 1000 {
		t.Errorf("expected old price 1000, got %d", transition.OldPrice)
	}

	if transition.NewPrice != 800 {
		t.Errorf("expected new price 800, got %d", transition.NewPrice)
	}
}

func TestStateMachine_NoPriceChangeWhenSame(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	// First: item at 1000
	item := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}
	_, err := sm.UpdateItemState(ctx, ipID, item)
	if err != nil {
		t.Fatalf("first UpdateItemState failed: %v", err)
	}

	// Same item, same price
	transition, err := sm.UpdateItemState(ctx, ipID, item)
	if err != nil {
		t.Fatalf("second UpdateItemState failed: %v", err)
	}

	if transition != nil {
		t.Errorf("expected no transition when price is the same, got %v", transition.Type)
	}
}

func TestStateMachine_Relisted(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	// First: item is on_sale
	item1 := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}
	_, err := sm.UpdateItemState(ctx, ipID, item1)
	if err != nil {
		t.Fatalf("first UpdateItemState failed: %v", err)
	}

	// Then: item becomes sold
	item2 := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_SOLD,
	}
	_, err = sm.UpdateItemState(ctx, ipID, item2)
	if err != nil {
		t.Fatalf("second UpdateItemState failed: %v", err)
	}

	// Finally: item is relisted
	item3 := &pb.Item{
		SourceId: "m001",
		Price:    900,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}
	transition, err := sm.UpdateItemState(ctx, ipID, item3)
	if err != nil {
		t.Fatalf("third UpdateItemState failed: %v", err)
	}

	if transition == nil {
		t.Fatal("expected a transition for relisted item")
	}

	if transition.Type != TransitionRelisted {
		t.Errorf("expected TransitionRelisted, got %v", transition.Type)
	}
}

func TestStateMachine_ProcessItemsBatch(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	items := []*pb.Item{
		{SourceId: "m001", Price: 1000, Status: pb.ItemStatus_ITEM_STATUS_ON_SALE},
		{SourceId: "m002", Price: 2000, Status: pb.ItemStatus_ITEM_STATUS_ON_SALE},
		{SourceId: "m003", Price: 3000, Status: pb.ItemStatus_ITEM_STATUS_SOLD}, // first seen as sold -> new_sold transition
	}

	transitions, err := sm.ProcessItemsBatch(ctx, ipID, items)
	if err != nil {
		t.Fatalf("ProcessItemsBatch failed: %v", err)
	}

	// Should have 3 transitions:
	// - m001: new_listing
	// - m002: new_listing
	// - m003: new_sold (first seen as sold counts as outflow)
	if len(transitions) != 3 {
		t.Errorf("expected 3 transitions, got %d", len(transitions))
	}

	newListingCount := 0
	newSoldCount := 0
	for _, tr := range transitions {
		if tr.Type == TransitionNewListing {
			newListingCount++
		} else if tr.Type == TransitionNewSold {
			newSoldCount++
		}
	}
	if newListingCount != 2 {
		t.Errorf("expected 2 new_listing transitions, got %d", newListingCount)
	}
	if newSoldCount != 1 {
		t.Errorf("expected 1 new_sold transition, got %d", newSoldCount)
	}
}

func TestStateMachine_SummarizeTransitions(t *testing.T) {
	transitions := []StateTransition{
		{Type: TransitionNewListing},
		{Type: TransitionNewListing},
		{Type: TransitionSold},
		{Type: TransitionPriceChange},
		{Type: TransitionPriceChange},
		{Type: TransitionPriceChange},
		{Type: TransitionRelisted},
	}

	summary := SummarizeTransitions(transitions)

	if summary.NewListings != 2 {
		t.Errorf("expected 2 new listings, got %d", summary.NewListings)
	}
	if summary.Sold != 1 {
		t.Errorf("expected 1 sold, got %d", summary.Sold)
	}
	if summary.PriceChanges != 3 {
		t.Errorf("expected 3 price changes, got %d", summary.PriceChanges)
	}
	if summary.Relisted != 1 {
		t.Errorf("expected 1 relisted, got %d", summary.Relisted)
	}
}

func TestStateMachine_ClearItemState(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	// Create an item
	item := &pb.Item{
		SourceId: "m001",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}
	_, err := sm.UpdateItemState(ctx, ipID, item)
	if err != nil {
		t.Fatalf("UpdateItemState failed: %v", err)
	}

	// Verify it exists
	state, err := sm.GetItemState(ctx, ipID, "m001")
	if err != nil || state == nil {
		t.Fatal("expected item to exist before clear")
	}

	// Clear it
	err = sm.ClearItemState(ctx, ipID, "m001")
	if err != nil {
		t.Fatalf("ClearItemState failed: %v", err)
	}

	// Verify it's gone
	state, err = sm.GetItemState(ctx, ipID, "m001")
	if err != nil {
		t.Fatalf("GetItemState failed: %v", err)
	}
	if state != nil {
		t.Error("expected item to be cleared")
	}
}

func TestStateMachine_ClearAllItems(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()
	ipID := uint64(1)

	// Create multiple items
	items := []*pb.Item{
		{SourceId: "m001", Price: 1000, Status: pb.ItemStatus_ITEM_STATUS_ON_SALE},
		{SourceId: "m002", Price: 2000, Status: pb.ItemStatus_ITEM_STATUS_ON_SALE},
		{SourceId: "m003", Price: 3000, Status: pb.ItemStatus_ITEM_STATUS_ON_SALE},
	}
	_, err := sm.ProcessItemsBatch(ctx, ipID, items)
	if err != nil {
		t.Fatalf("ProcessItemsBatch failed: %v", err)
	}

	// Verify count
	count, err := sm.GetItemCount(ctx, ipID)
	if err != nil {
		t.Fatalf("GetItemCount failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 items, got %d", count)
	}

	// Clear all
	err = sm.ClearAllItems(ctx, ipID)
	if err != nil {
		t.Fatalf("ClearAllItems failed: %v", err)
	}

	// Verify count is 0
	count, err = sm.GetItemCount(ctx, ipID)
	if err != nil {
		t.Fatalf("GetItemCount failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 items after clear, got %d", count)
	}
}

func TestStateMachine_GetItemState_NotFound(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()

	state, err := sm.GetItemState(ctx, 1, "nonexistent")
	if err != nil {
		t.Fatalf("GetItemState failed: %v", err)
	}
	if state != nil {
		t.Error("expected nil state for nonexistent item")
	}
}

func TestStateMachine_NilItem(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()

	transition, err := sm.UpdateItemState(ctx, 1, nil)
	if err != nil {
		t.Fatalf("UpdateItemState failed: %v", err)
	}
	if transition != nil {
		t.Error("expected nil transition for nil item")
	}
}

func TestStateMachine_EmptySourceID(t *testing.T) {
	sm, cleanup := setupStateMachine(t)
	defer cleanup()

	ctx := context.Background()

	item := &pb.Item{
		SourceId: "",
		Price:    1000,
		Status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
	}

	transition, err := sm.UpdateItemState(ctx, 1, item)
	if err != nil {
		t.Fatalf("UpdateItemState failed: %v", err)
	}
	if transition != nil {
		t.Error("expected nil transition for empty source ID")
	}
}
