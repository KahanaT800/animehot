-- migrations/001_init.sql
-- IP Liquidity Analyzer - Initial Schema
-- Created: 2026-01-16
-- Updated: 2026-01-17 - Removed keywords, added tags and other fields

-- ============================================================================
-- ip_metadata: IP 元数据表
-- 存储被监控的 IP 信息，包括名称、标签、权重等
-- ============================================================================
CREATE TABLE IF NOT EXISTS ip_metadata (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name            VARCHAR(255) NOT NULL COMMENT 'IP 名称（日语，作为搜索关键词）',
    name_en         VARCHAR(255) NOT NULL DEFAULT '' COMMENT '英语别名',
    name_cn         VARCHAR(255) NOT NULL DEFAULT '' COMMENT '中文别名',
    category        VARCHAR(50) NOT NULL DEFAULT '' COMMENT '分类（anime/game/vocaloid/vtuber）',
    tags            JSON COMMENT '标签数组',
    image_url       VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'IP 图片 URL',
    external_id     VARCHAR(100) NOT NULL DEFAULT '' COMMENT '外部 ID（mal:xxx, bgm:xxx）',
    notes           TEXT COMMENT '备注',
    weight          DECIMAL(5,2) NOT NULL DEFAULT 1.00 COMMENT '调度权重（1.0=基准，2.0=双倍频率）',
    status          VARCHAR(20) NOT NULL DEFAULT 'active' COMMENT 'IP 监控状态',
    last_crawled_at DATETIME(3) COMMENT '最后爬取时间',
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    deleted_at      DATETIME(3) DEFAULT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_name (name),
    KEY idx_ip_status (status),
    KEY idx_weight (weight DESC),
    KEY idx_last_crawled (last_crawled_at),
    KEY idx_external_id (external_id),
    KEY idx_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP 元数据';

-- ============================================================================
-- ip_stats_hourly: 小时级统计表
-- 记录每个 IP 每小时的流动性指标（进货量、出货量、流动性指数）
-- ============================================================================
CREATE TABLE IF NOT EXISTS ip_stats_hourly (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    ip_id           BIGINT UNSIGNED NOT NULL COMMENT '关联 ip_metadata.id',
    hour_bucket     DATETIME NOT NULL COMMENT '小时时间桶（如 2026-01-16 14:00:00）',
    inflow          INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '进货量（新增商品数）',
    outflow         INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '出货量（售出商品数）',
    liquidity_index DECIMAL(8,4) COMMENT '流动性指数（outflow/inflow，NULL表示inflow为0）',
    active_count    INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '活跃商品总数',
    avg_price       DECIMAL(10,2) COMMENT '平均价格（日元）',
    min_price       DECIMAL(10,2) COMMENT '最低价格',
    max_price       DECIMAL(10,2) COMMENT '最高价格',
    price_stddev    DECIMAL(10,2) COMMENT '价格标准差',
    sample_count    INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '样本数（用于聚合验证）',
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_ip_hour (ip_id, hour_bucket),
    KEY idx_hour_bucket (hour_bucket),
    KEY idx_liquidity (liquidity_index DESC),
    KEY idx_ip_time_range (ip_id, hour_bucket DESC),
    CONSTRAINT fk_stats_ip FOREIGN KEY (ip_id) REFERENCES ip_metadata(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP 小时级统计';

-- ============================================================================
-- item_snapshots: 商品快照表
-- 记录爬取到的商品状态历史（用于状态追踪和调试）
-- ============================================================================
CREATE TABLE IF NOT EXISTS item_snapshots (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    ip_id           BIGINT UNSIGNED NOT NULL COMMENT '关联 ip_metadata.id',
    source_id       VARCHAR(64) NOT NULL COMMENT '来源平台商品 ID（如 Mercari 的 m123456）',
    title           VARCHAR(500) NOT NULL DEFAULT '' COMMENT '商品标题',
    price           DECIMAL(10,2) COMMENT '价格（日元）',
    status          VARCHAR(20) NOT NULL DEFAULT 'on_sale' COMMENT '商品状态（on_sale/sold/deleted）',
    image_url       VARCHAR(512) NOT NULL DEFAULT '' COMMENT '商品图片 URL',
    item_url        VARCHAR(512) NOT NULL DEFAULT '' COMMENT '商品详情页 URL',
    first_seen_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '首次发现时间',
    last_seen_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '最后发现时间',
    sold_at         DATETIME(3) COMMENT '售出时间（状态变为 sold 时记录）',
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_ip_source (ip_id, source_id),
    KEY idx_item_status (status),
    KEY idx_ip_status_time (ip_id, status, last_seen_at DESC),
    KEY idx_source_id (source_id),
    CONSTRAINT fk_snapshot_ip FOREIGN KEY (ip_id) REFERENCES ip_metadata(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='商品快照';

-- ============================================================================
-- ip_alerts: 预警记录表
-- 记录流动性异常等预警信息
-- ============================================================================
CREATE TABLE IF NOT EXISTS ip_alerts (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    ip_id           BIGINT UNSIGNED NOT NULL COMMENT '关联 ip_metadata.id',
    alert_type      VARCHAR(50) NOT NULL COMMENT '预警类型（high_outflow/low_liquidity/price_drop/surge）',
    severity        VARCHAR(20) NOT NULL DEFAULT 'info' COMMENT '严重程度（info/warning/critical）',
    message         TEXT NOT NULL COMMENT '预警消息',
    threshold       DECIMAL(10,4) COMMENT '触发阈值',
    actual_value    DECIMAL(10,4) COMMENT '实际值',
    acknowledged    TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否已确认',
    acknowledged_at DATETIME(3) COMMENT '确认时间',
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_ip_alerts (ip_id, created_at DESC),
    KEY idx_alert_severity (severity),
    KEY idx_unacked (acknowledged, created_at DESC),
    CONSTRAINT fk_alert_ip FOREIGN KEY (ip_id) REFERENCES ip_metadata(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP 预警记录';
