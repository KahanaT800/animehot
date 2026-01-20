// internal/api/system_handler.go
// 系统状态 API
package api

import (
	"time"

	"animetop/internal/model"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// 系统状态
// ============================================================================

// SystemStatusResponse 系统状态响应
type SystemStatusResponse struct {
	Status    string                 `json:"status"`
	Timestamp string                 `json:"timestamp"`
	Database  DatabaseStatus         `json:"database"`
	Pipeline  *PipelineStatus        `json:"pipeline,omitempty"`
	Scheduler *SchedulerStatusBrief  `json:"scheduler,omitempty"`
	Stats     map[string]interface{} `json:"stats"`
}

// DatabaseStatus 数据库状态
type DatabaseStatus struct {
	Connected bool  `json:"connected"`
	IPCount   int64 `json:"ip_count"`
}

// PipelineStatus Pipeline 状态
type PipelineStatus struct {
	Running     bool    `json:"running"`
	Processed   int64   `json:"processed"`
	Failed      int64   `json:"failed"`
	SuccessRate float64 `json:"success_rate"`
}

// SchedulerStatusBrief 调度器简要状态
type SchedulerStatusBrief struct {
	Running    bool `json:"running"`
	TotalIPs   int  `json:"total_ips"`
	OverdueIPs int  `json:"overdue_ips"`
}

// getSystemStatus 获取系统状态
// GET /api/v1/system/status
func (s *Server) getSystemStatus(c *gin.Context) {
	resp := SystemStatusResponse{
		Status:    "ok",
		Timestamp: time.Now().Format(time.RFC3339),
		Stats:     make(map[string]interface{}),
	}

	// 数据库状态
	var ipCount int64
	dbConnected := true
	if err := s.db.Model(&model.IPMetadata{}).Where("status = ?", model.IPStatusActive).Count(&ipCount).Error; err != nil {
		dbConnected = false
	}
	resp.Database = DatabaseStatus{
		Connected: dbConnected,
		IPCount:   ipCount,
	}

	// Pipeline 状态
	if s.pipeline != nil {
		stats := s.pipeline.GetStats()
		resp.Pipeline = &PipelineStatus{
			Running:     s.pipeline.IsRunning(),
			Processed:   stats.Processed,
			Failed:      stats.Failed,
			SuccessRate: stats.SuccessRate,
		}
	}

	// Scheduler 状态
	if s.scheduler != nil {
		stats := s.scheduler.GetStats()
		resp.Scheduler = &SchedulerStatusBrief{
			Running:    s.scheduler.IsRunning(),
			TotalIPs:   stats.TotalIPs,
			OverdueIPs: stats.OverdueIPs,
		}
	}

	// 额外统计
	var alertCount int64
	s.db.Model(&model.IPAlert{}).Where("acknowledged = ?", false).Count(&alertCount)
	resp.Stats["unacked_alerts"] = alertCount

	var itemCount int64
	s.db.Model(&model.ItemSnapshot{}).Where("status = ?", model.ItemStatusOnSale).Count(&itemCount)
	resp.Stats["active_items"] = itemCount

	success(c, resp)
}

// ============================================================================
// 调度器详情
// ============================================================================

// ScheduleInfoResponse 调度信息响应
type ScheduleInfoResponse struct {
	IPID         uint64 `json:"ip_id"`
	IPName       string `json:"ip_name"`
	NextSchedule string `json:"next_schedule"`
	TimeUntil    string `json:"time_until"`
	IsOverdue    bool   `json:"is_overdue"`
}

// getSchedulerStatus 获取调度器详细状态
// GET /api/v1/system/scheduler
func (s *Server) getSchedulerStatus(c *gin.Context) {
	if s.scheduler == nil {
		internalError(c, "scheduler not available")
		return
	}

	// 获取调度状态
	scheduleStatus := s.scheduler.GetScheduleStatus()
	stats := s.scheduler.GetStats()

	// 获取 IP 名称映射
	var ips []model.IPMetadata
	ipIDs := make([]uint64, 0, len(scheduleStatus))
	for ipID := range scheduleStatus {
		ipIDs = append(ipIDs, ipID)
	}
	s.db.Where("id IN ?", ipIDs).Find(&ips)

	ipNameMap := make(map[uint64]string)
	for _, ip := range ips {
		ipNameMap[ip.ID] = ip.Name
	}

	// 构建响应
	schedules := make([]ScheduleInfoResponse, 0, len(scheduleStatus))
	for ipID, info := range scheduleStatus {
		schedules = append(schedules, ScheduleInfoResponse{
			IPID:         ipID,
			IPName:       ipNameMap[ipID],
			NextSchedule: info.NextSchedule.Format(time.RFC3339),
			TimeUntil:    info.TimeUntil.String(),
			IsOverdue:    info.IsOverdue,
		})
	}

	success(c, gin.H{
		"running":     s.scheduler.IsRunning(),
		"total_ips":   stats.TotalIPs,
		"overdue_ips": stats.OverdueIPs,
		"schedules":   schedules,
	})
}
