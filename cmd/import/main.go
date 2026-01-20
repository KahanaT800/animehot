// cmd/import/main.go
// IP 数据导入工具 - 从 JSON 文件导入 IP 元数据到数据库
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"animetop/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// IPImportEntry JSON 导入条目
type IPImportEntry struct {
	Name       string   `json:"name"`                  // 必填：日语名称（作为搜索关键词）
	NameEN     string   `json:"name_en,omitempty"`     // 可选：英语别名
	NameCN     string   `json:"name_cn,omitempty"`     // 可选：中文别名
	Category   string   `json:"category,omitempty"`    // 可选：分类
	Tags       []string `json:"tags,omitempty"`        // 可选：标签
	ImageURL   string   `json:"image_url,omitempty"`   // 可选：图片 URL
	ExternalID string   `json:"external_id,omitempty"` // 可选：外部 ID
	Weight     float64  `json:"weight,omitempty"`      // 可选：权重（默认 1.0）
	Notes      string   `json:"notes,omitempty"`       // 可选：备注
}

// IPImportFile JSON 导入文件结构
type IPImportFile struct {
	IPs []IPImportEntry `json:"ips"`
}

func main() {
	// 命令行参数
	dsn := flag.String("dsn", "", "MySQL DSN (or use DB_DSN env)")
	file := flag.String("file", "", "JSON file path (required)")
	dryRun := flag.Bool("dry-run", false, "Dry run mode (don't write to database)")
	upsert := flag.Bool("upsert", true, "Update existing entries by name")
	flag.Parse()

	// 支持从环境变量读取 DSN
	if *dsn == "" {
		*dsn = os.Getenv("DB_DSN")
	}

	if *dsn == "" || *file == "" {
		fmt.Println("Usage: import -dsn <mysql_dsn> -file <json_file>")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -dsn      MySQL DSN (e.g., user:pass@tcp(localhost:3306)/animetop?charset=utf8mb4&parseTime=True)")
		fmt.Println("  -file     JSON file path")
		fmt.Println("  -dry-run  Dry run mode (default: false)")
		fmt.Println("  -upsert   Update existing entries (default: true)")
		fmt.Println()
		fmt.Println("JSON file format:")
		fmt.Println(`{
  "ips": [
    {
      "name": "初音ミク",
      "name_en": "Hatsune Miku",
      "name_cn": "初音未来",
      "category": "vocaloid",
      "tags": ["vocaloid", "crypton"],
      "image_url": "https://example.com/miku.jpg",
      "external_id": "mal:12345",
      "weight": 1.5,
      "notes": "备注"
    }
  ]
}`)
		os.Exit(1)
	}

	// 读取 JSON 文件
	data, err := os.ReadFile(*file)
	if err != nil {
		log.Fatalf("Failed to read file: %v", err)
	}

	var importFile IPImportFile
	if err := json.Unmarshal(data, &importFile); err != nil {
		log.Fatalf("Failed to parse JSON: %v", err)
	}

	fmt.Printf("Loaded %d IPs from %s\n", len(importFile.IPs), *file)

	if *dryRun {
		fmt.Println("\n[DRY RUN MODE - No changes will be made]")
		for i, ip := range importFile.IPs {
			fmt.Printf("  %d. %s", i+1, ip.Name)
			if ip.NameEN != "" {
				fmt.Printf(" (%s)", ip.NameEN)
			}
			if ip.Category != "" {
				fmt.Printf(" [%s]", ip.Category)
			}
			fmt.Println()
		}
		return
	}

	// 连接数据库
	db, err := gorm.Open(mysql.Open(*dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// 导入数据
	var created, updated, failed int
	for _, entry := range importFile.IPs {
		if entry.Name == "" {
			fmt.Printf("  [SKIP] Empty name\n")
			failed++
			continue
		}

		ip := convertToModel(entry)

		var result *gorm.DB
		if *upsert {
			// UPSERT: 存在则更新，不存在则插入
			result = db.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "name"}},
				DoUpdates: clause.AssignmentColumns([]string{"name_en", "name_cn", "category", "tags", "image_url", "external_id", "notes", "weight", "updated_at"}),
			}).Create(&ip)
		} else {
			// 仅插入，忽略已存在
			result = db.Clauses(clause.OnConflict{
				DoNothing: true,
			}).Create(&ip)
		}

		if result.Error != nil {
			fmt.Printf("  [FAIL] %s: %v\n", entry.Name, result.Error)
			failed++
		} else if result.RowsAffected == 0 {
			fmt.Printf("  [SKIP] %s (already exists)\n", entry.Name)
		} else {
			// 检查是更新还是插入
			if ip.ID > 0 && ip.CreatedAt != ip.UpdatedAt {
				fmt.Printf("  [UPDATE] %s (id=%d)\n", entry.Name, ip.ID)
				updated++
			} else {
				fmt.Printf("  [CREATE] %s (id=%d)\n", entry.Name, ip.ID)
				created++
			}
		}
	}

	fmt.Printf("\nSummary: %d created, %d updated, %d failed\n", created, updated, failed)
}

// convertToModel 将导入条目转换为数据库模型
func convertToModel(entry IPImportEntry) model.IPMetadata {
	weight := entry.Weight
	if weight <= 0 {
		weight = 1.0
	}

	tags := model.Tags(entry.Tags)
	if tags == nil {
		tags = model.Tags{}
	}

	return model.IPMetadata{
		Name:       entry.Name,
		NameEN:     entry.NameEN,
		NameCN:     entry.NameCN,
		Category:   entry.Category,
		Tags:       tags,
		ImageURL:   entry.ImageURL,
		ExternalID: entry.ExternalID,
		Notes:      entry.Notes,
		Weight:     weight,
		Status:     model.IPStatusActive,
	}
}
