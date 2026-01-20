// internal/api/alert_handler.go
// 预警 API
package api

import (
	"time"

	"animetop/internal/model"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// 预警列表
// ============================================================================

// AlertsQuery 预警查询参数
type AlertsQuery struct {
	IPID         uint64 `form:"ip_id"`
	Severity     string `form:"severity"`     // info, warning, critical
	Acknowledged *bool  `form:"acknowledged"` // true/false
	Page         int    `form:"page"`
	PageSize     int    `form:"page_size"`
}

// AlertResponse 预警响应（包含 IP 名称）
type AlertResponse struct {
	model.IPAlert
	IPName string `json:"ip_name"`
}

// listAlerts 获取预警列表
// GET /api/v1/alerts
func (s *Server) listAlerts(c *gin.Context) {
	var query AlertsQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		badRequest(c, err.Error())
		return
	}

	// 默认分页
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PageSize == 0 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}

	// 构建查询
	db := s.db.Model(&model.IPAlert{})

	if query.IPID != 0 {
		db = db.Where("ip_id = ?", query.IPID)
	}
	if query.Severity != "" {
		db = db.Where("severity = ?", query.Severity)
	}
	if query.Acknowledged != nil {
		db = db.Where("acknowledged = ?", *query.Acknowledged)
	} else {
		// 默认只显示未确认
		db = db.Where("acknowledged = ?", false)
	}

	// 计算总数
	var total int64
	if err := db.Count(&total).Error; err != nil {
		internalError(c, "count failed")
		return
	}

	// 查询数据（预加载 IP 信息）
	var alerts []model.IPAlert
	offset := (query.Page - 1) * query.PageSize
	if err := db.Preload("IP").Order("created_at DESC").Offset(offset).Limit(query.PageSize).Find(&alerts).Error; err != nil {
		internalError(c, "query failed")
		return
	}

	// 构建响应
	responses := make([]AlertResponse, len(alerts))
	for i, alert := range alerts {
		responses[i] = AlertResponse{
			IPAlert: alert,
		}
		if alert.IP != nil {
			responses[i].IPName = alert.IP.Name
		}
	}

	successPaginated(c, responses, total, query.Page, query.PageSize)
}

// ============================================================================
// 确认预警
// ============================================================================

// ackAlert 确认预警
// POST /api/v1/alerts/:id/ack
func (s *Server) ackAlert(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	var alert model.IPAlert
	if err := s.db.First(&alert, id).Error; err != nil {
		notFound(c, "alert not found")
		return
	}

	if alert.Acknowledged {
		badRequest(c, "alert already acknowledged")
		return
	}

	now := time.Now()
	if err := s.db.Model(&alert).Updates(map[string]interface{}{
		"acknowledged":    true,
		"acknowledged_at": now,
	}).Error; err != nil {
		internalError(c, "update failed")
		return
	}

	alert.Acknowledged = true
	alert.AcknowledgedAt = &now

	success(c, alert)
}
