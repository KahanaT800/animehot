// internal/api/ip_handler.go
// IP 管理 API
package api

import (
	"fmt"
	"net/http"
	"time"

	"animetop/internal/model"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// IP 列表与查询
// ============================================================================

// ListIPsQuery 列表查询参数
type ListIPsQuery struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	Status   string `form:"status"`
	Category string `form:"category"`
	Keyword  string `form:"keyword"` // 模糊搜索
}

// listIPs 获取 IP 列表
// GET /api/v1/ips
func (s *Server) listIPs(c *gin.Context) {
	var query ListIPsQuery
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

	// 构建查询
	db := s.db.Model(&model.IPMetadata{})

	// 默认过滤已删除的记录，除非明确指定 status=deleted
	if query.Status != "" {
		db = db.Where("status = ?", query.Status)
	} else {
		db = db.Where("status != ?", "deleted")
	}
	if query.Category != "" {
		db = db.Where("category = ?", query.Category)
	}
	if query.Keyword != "" {
		like := "%" + query.Keyword + "%"
		db = db.Where("name LIKE ? OR name_en LIKE ? OR name_cn LIKE ?", like, like, like)
	}

	// 计算总数
	var total int64
	if err := db.Count(&total).Error; err != nil {
		internalError(c, "count failed")
		return
	}

	// 查询数据
	var ips []model.IPMetadata
	offset := (query.Page - 1) * query.PageSize
	if err := db.Order("weight DESC, id ASC").Offset(offset).Limit(query.PageSize).Find(&ips).Error; err != nil {
		internalError(c, "query failed")
		return
	}

	successPaginated(c, ips, total, query.Page, query.PageSize)
}

// getIP 获取单个 IP
// GET /api/v1/ips/:id
func (s *Server) getIP(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	var ip model.IPMetadata
	if err := s.db.First(&ip, id).Error; err != nil {
		notFound(c, "IP not found")
		return
	}

	success(c, ip)
}

// ============================================================================
// IP 创建与更新（保留给前端）
// ============================================================================

// CreateIPRequest 创建 IP 请求
type CreateIPRequest struct {
	Name       string   `json:"name" binding:"required"`
	NameEN     string   `json:"name_en"`
	NameCN     string   `json:"name_cn"`
	Category   string   `json:"category"`
	Tags       []string `json:"tags"`
	ImageURL   string   `json:"image_url"`
	ExternalID string   `json:"external_id"`
	// Weight 初始权重 (可选，默认 1.0)
	// 注意: 权重会根据爬取数据自动调整，手动设置的值可能被覆盖
	// 权重影响爬取间隔: interval = BaseInterval / weight
	Weight float64 `json:"weight"`
	Notes  string  `json:"notes"`
}

// createIP 创建 IP
// POST /api/v1/ips
func (s *Server) createIP(c *gin.Context) {
	var req CreateIPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}

	weight := req.Weight
	if weight <= 0 {
		weight = 1.0
	}

	ip := model.IPMetadata{
		Name:       req.Name,
		NameEN:     req.NameEN,
		NameCN:     req.NameCN,
		Category:   req.Category,
		Tags:       model.Tags(req.Tags),
		ImageURL:   req.ImageURL,
		ExternalID: req.ExternalID,
		Weight:     weight,
		Notes:      req.Notes,
		Status:     model.IPStatusActive,
	}

	if err := s.db.Create(&ip).Error; err != nil {
		internalError(c, "create failed: "+err.Error())
		return
	}

	// 添加到调度器
	if s.scheduler != nil {
		s.scheduler.AddIP(&ip)
	}

	c.JSON(http.StatusCreated, Response{
		Code:    0,
		Message: "created",
		Data:    ip,
	})
}

// UpdateIPRequest 更新 IP 请求
type UpdateIPRequest struct {
	Name       *string   `json:"name"`
	NameEN     *string   `json:"name_en"`
	NameCN     *string   `json:"name_cn"`
	Category   *string   `json:"category"`
	Tags       *[]string `json:"tags"`
	ImageURL   *string   `json:"image_url"`
	ExternalID *string   `json:"external_id"`
	// Weight 权重 (可选)
	// 注意: 权重会根据爬取数据自动调整，手动设置的值可能被覆盖
	// 权重影响爬取间隔: interval = BaseInterval / weight
	Weight *float64 `json:"weight"`
	Notes  *string  `json:"notes"`
	Status *string  `json:"status"`
}

// updateIP 更新 IP
// PUT /api/v1/ips/:id
func (s *Server) updateIP(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	var ip model.IPMetadata
	if err := s.db.First(&ip, id).Error; err != nil {
		notFound(c, "IP not found")
		return
	}

	var req UpdateIPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}

	// 更新字段
	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.NameEN != nil {
		updates["name_en"] = *req.NameEN
	}
	if req.NameCN != nil {
		updates["name_cn"] = *req.NameCN
	}
	if req.Category != nil {
		updates["category"] = *req.Category
	}
	if req.Tags != nil {
		updates["tags"] = model.Tags(*req.Tags)
	}
	if req.ImageURL != nil {
		updates["image_url"] = *req.ImageURL
	}
	if req.ExternalID != nil {
		updates["external_id"] = *req.ExternalID
	}
	if req.Weight != nil {
		updates["weight"] = *req.Weight
	}
	if req.Notes != nil {
		updates["notes"] = *req.Notes
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if len(updates) == 0 {
		badRequest(c, "no fields to update")
		return
	}

	if err := s.db.Model(&ip).Updates(updates).Error; err != nil {
		internalError(c, "update failed")
		return
	}

	// 更新调度器
	if s.scheduler != nil {
		if req.Weight != nil {
			s.scheduler.UpdateIPWeight(id, *req.Weight)
		}
		if req.Status != nil {
			if *req.Status == string(model.IPStatusActive) {
				s.scheduler.AddIP(&ip)
			} else {
				s.scheduler.RemoveIP(id)
			}
		}
	}

	// 重新加载
	s.db.First(&ip, id)
	success(c, ip)
}

// deleteIP 删除 IP（软删除）
// DELETE /api/v1/ips/:id
func (s *Server) deleteIP(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	var ip model.IPMetadata
	if err := s.db.First(&ip, id).Error; err != nil {
		notFound(c, "IP not found")
		return
	}

	// 软删除（设置 status 为 deleted）
	if err := s.db.Model(&ip).Update("status", model.IPStatusDeleted).Error; err != nil {
		internalError(c, "delete failed")
		return
	}

	// 从调度器移除
	if s.scheduler != nil {
		s.scheduler.RemoveIP(id)
	}

	success(c, gin.H{"id": id, "deleted": true})
}

// triggerIP 手动触发 IP 爬取
// POST /api/v1/ips/:id/trigger
func (s *Server) triggerIP(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	var ip model.IPMetadata
	if err := s.db.First(&ip, id).Error; err != nil {
		notFound(c, "IP not found")
		return
	}

	if s.scheduler == nil {
		internalError(c, "scheduler not available")
		return
	}

	s.scheduler.TriggerNow(id)
	success(c, gin.H{"id": id, "triggered": true})
}

// ============================================================================
// IP 批量导入
// ============================================================================

// ImportIPEntry 导入条目
type ImportIPEntry struct {
	Name       string   `json:"name" binding:"required"`
	NameEN     string   `json:"name_en"`
	NameCN     string   `json:"name_cn"`
	Category   string   `json:"category"`
	Tags       []string `json:"tags"`
	ImageURL   string   `json:"image_url"`
	ExternalID string   `json:"external_id"`
	// Weight 初始权重 (可选，默认 1.0)
	// 注意: 权重会根据爬取数据自动调整，手动设置的值可能被覆盖
	// 权重影响爬取间隔: interval = BaseInterval / weight
	Weight float64 `json:"weight"`
	Notes  string  `json:"notes"`
}

// ImportIPsRequest 批量导入请求
type ImportIPsRequest struct {
	IPs    []ImportIPEntry `json:"ips" binding:"required,min=1"`
	Upsert bool            `json:"upsert"` // 默认 true，存在则更新
}

// ImportIPsResponse 导入响应
type ImportIPsResponse struct {
	Created int               `json:"created"`
	Updated int               `json:"updated"`
	Failed  int               `json:"failed"`
	Details []ImportIPsDetail `json:"details,omitempty"`
}

// ImportIPsDetail 导入详情
type ImportIPsDetail struct {
	Name   string `json:"name"`
	Status string `json:"status"` // created, updated, failed, skipped
	ID     uint64 `json:"id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// importIPs 批量导入 IP
// POST /api/v1/admin/import
func (s *Server) importIPs(c *gin.Context) {
	var req ImportIPsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}

	// 默认开启 upsert
	upsert := true
	if c.Query("upsert") == "false" {
		upsert = false
	}
	// 也支持 JSON body 中的 upsert 字段
	if req.Upsert {
		upsert = true
	}

	var created, updated, failed int
	var details []ImportIPsDetail

	for _, entry := range req.IPs {
		if entry.Name == "" {
			failed++
			details = append(details, ImportIPsDetail{
				Name:   "(empty)",
				Status: "failed",
				Error:  "name is required",
			})
			continue
		}

		weight := entry.Weight
		if weight <= 0 {
			weight = 1.0
		}

		tags := model.Tags(entry.Tags)
		if tags == nil {
			tags = model.Tags{}
		}

		ip := model.IPMetadata{
			Name:       entry.Name,
			NameEN:     entry.NameEN,
			NameCN:     entry.NameCN,
			Category:   entry.Category,
			Tags:       tags,
			ImageURL:   entry.ImageURL,
			ExternalID: entry.ExternalID,
			Weight:     weight,
			Notes:      entry.Notes,
			Status:     model.IPStatusActive,
		}

		if upsert {
			// 查找是否存在
			var existing model.IPMetadata
			err := s.db.Where("name = ?", entry.Name).First(&existing).Error

			if err == nil {
				// 存在，更新
				updates := map[string]interface{}{
					"name_en":     entry.NameEN,
					"name_cn":     entry.NameCN,
					"category":    entry.Category,
					"tags":        tags,
					"image_url":   entry.ImageURL,
					"external_id": entry.ExternalID,
					"weight":      weight,
					"notes":       entry.Notes,
				}
				if err := s.db.Model(&existing).Updates(updates).Error; err != nil {
					failed++
					details = append(details, ImportIPsDetail{
						Name:   entry.Name,
						Status: "failed",
						Error:  err.Error(),
					})
				} else {
					updated++
					details = append(details, ImportIPsDetail{
						Name:   entry.Name,
						Status: "updated",
						ID:     uint64(existing.ID),
					})
					// 更新调度器权重
					if s.scheduler != nil {
						s.scheduler.UpdateIPWeight(uint64(existing.ID), weight)
					}
				}
			} else {
				// 不存在，创建
				if err := s.db.Create(&ip).Error; err != nil {
					failed++
					details = append(details, ImportIPsDetail{
						Name:   entry.Name,
						Status: "failed",
						Error:  err.Error(),
					})
				} else {
					created++
					details = append(details, ImportIPsDetail{
						Name:   entry.Name,
						Status: "created",
						ID:     uint64(ip.ID),
					})
					// 添加到调度器
					if s.scheduler != nil {
						s.scheduler.AddIP(&ip)
					}
				}
			}
		} else {
			// 仅创建，不更新
			if err := s.db.Create(&ip).Error; err != nil {
				failed++
				details = append(details, ImportIPsDetail{
					Name:   entry.Name,
					Status: "failed",
					Error:  err.Error(),
				})
			} else {
				created++
				details = append(details, ImportIPsDetail{
					Name:   entry.Name,
					Status: "created",
					ID:     uint64(ip.ID),
				})
				if s.scheduler != nil {
					s.scheduler.AddIP(&ip)
				}
			}
		}
	}

	resp := ImportIPsResponse{
		Created: created,
		Updated: updated,
		Failed:  failed,
		Details: details,
	}

	s.logger.Info("import completed",
		"created", created,
		"updated", updated,
		"failed", failed,
	)

	success(c, resp)
}

// triggerDailyArchive 手动触发日归档
// POST /api/v1/admin/archive/daily?date=2026-01-18
func (s *Server) triggerDailyArchive(c *gin.Context) {
	dateStr := c.Query("date")
	var date time.Time
	var err error

	if dateStr != "" {
		date, err = time.Parse("2006-01-02", dateStr)
		if err != nil {
			badRequest(c, "invalid date format, use YYYY-MM-DD")
			return
		}
	} else {
		// 默认归档昨天的数据
		date = time.Now().AddDate(0, 0, -1)
	}

	if s.scheduler == nil {
		internalError(c, "scheduler not available")
		return
	}

	if err := s.scheduler.TriggerDailyArchive(c.Request.Context(), date); err != nil {
		internalError(c, fmt.Sprintf("archive failed: %v", err))
		return
	}

	success(c, gin.H{
		"message": "daily archive completed",
		"date":    date.Format("2006-01-02"),
	})
}

// triggerWeeklyArchive 手动触发周归档
// POST /api/v1/admin/archive/weekly?week_start=2026-01-13
func (s *Server) triggerWeeklyArchive(c *gin.Context) {
	dateStr := c.Query("week_start")
	var weekStart time.Time
	var err error

	if dateStr != "" {
		weekStart, err = time.Parse("2006-01-02", dateStr)
		if err != nil {
			badRequest(c, "invalid date format, use YYYY-MM-DD")
			return
		}
	} else {
		// 默认归档上周的数据 (上周一)
		now := time.Now()
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		weekStart = now.AddDate(0, 0, -weekday-6) // 上周一
	}

	if s.scheduler == nil {
		internalError(c, "scheduler not available")
		return
	}

	if err := s.scheduler.TriggerWeeklyArchive(c.Request.Context(), weekStart); err != nil {
		internalError(c, fmt.Sprintf("archive failed: %v", err))
		return
	}

	success(c, gin.H{
		"message":    "weekly archive completed",
		"week_start": weekStart.Format("2006-01-02"),
	})
}
