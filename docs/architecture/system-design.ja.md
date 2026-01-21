# Anime Hot システムアーキテクチャ設計書

## 目次

1. [システム概要](#1-システム概要)
2. [スケジューラ設計](#2-スケジューラ設計)
3. [クローラー設計](#3-クローラー設計)
4. [Pipeline 設計](#4-pipeline-設計)
5. [Redis データ構造詳細](#5-redis-データ構造詳細)
6. [システム連携とデータフロー](#6-システム連携とデータフロー)
7. [信頼性保証](#7-信頼性保証)

---

## 1. システム概要

### 1.1 アーキテクチャ全景

```mermaid
flowchart TB
    subgraph Services["サービス層"]
        Scheduler["Scheduler<br/>（スケジューラ）"]
        Crawler["Crawler<br/>（クローラー）"]
        Pipeline["Pipeline<br/>（処理パイプライン）"]
    end

    subgraph Redis["Redis"]
        ZSET["ZSET<br/>（スケジュールキュー）"]
        LIST["LIST<br/>（タスクキュー）"]
        HASH["HASH<br/>（ステートマシン）"]
        SET["SET<br/>（重複排除）"]
    end

    subgraph MySQL["MySQL"]
        ip_metadata["ip_metadata"]
        ip_stats_hourly["ip_stats_hourly"]
        item_snapshots["item_snapshots"]
        ip_alerts["ip_alerts"]
    end

    Scheduler -->|"Task Queue"| Crawler
    Crawler -->|"Result Queue"| Pipeline
    Scheduler --> Redis
    Pipeline --> Redis
    Pipeline --> MySQL
```

### 1.2 コアコンポーネントの役割

| コンポーネント | 役割 | 主要ファイル |
|----------------|------|--------------|
| **Scheduler** | IP スケジュール管理、タスク投入、バックプレッシャー制御 | `internal/scheduler/ip_scheduler.go` |
| **Crawler** | ブラウザ自動化、ページ取得、アンチクロール対策 | `internal/crawler/` |
| **Pipeline** | 結果処理、ステートマシン更新、データ集計 | `internal/analyzer/pipeline.go` |

### 1.3 データフロー

```mermaid
flowchart LR
    Scheduler["Scheduler"]
    Redis["Redis"]
    Crawler["Crawler"]
    Pipeline["Pipeline"]
    MySQL["MySQL"]

    Scheduler -->|"ZSET クエリ"| Redis
    Scheduler -->|"ZADD 更新"| Redis
    Redis <-->|"BRPOPLPUSH<br/>Task/Ack"| Crawler
    Crawler -->|"BRPOPLPUSH<br/>Result/Ack"| Pipeline
    Redis -->|"HASH ステートマシン"| Pipeline
    Pipeline -->|"Upsert Stats/Snapshots"| MySQL
    MySQL -->|"閉ループスケジュール更新"| Scheduler
```

---

## 2. スケジューラ設計

### 2.1 コア設計理念

スケジューラは **ZSET 永続化 + 精密スリープ** の設計を採用し、従来のメモリスケジューリングの3つの問題を解決：

| 問題 | 従来方式 | 現行方式 |
|------|----------|----------|
| 再起動時データ消失 | メモリ map 消失 | Redis ZSET 永続化 |
| ポーリング効率低下 | 固定間隔ポーリング | 次タスクまで精密スリープ |
| 閉ループ不完全 | 次回ポーリングを待つ | Pipeline が直接 ZSET 更新 |

### 2.2 ZSET スケジュールキュー

**Redis Key**: `animetop:schedule:pending`

```mermaid
flowchart TB
    subgraph ZSET["Redis ZSET: schedule:pending"]
        direction TB
        Header["Score (Unix Timestamp) | Member (IP ID)"]
        R1["1705890000 | 11"]
        R2["1705890300 | 15"]
        R3["1705890600 | 18"]
        R4["1705891200 | 23"]
    end

    ZADD["ZADD<br/>（スケジュール更新）"] --> ZSET
    ZSET --> ZRANGEBYSCORE["ZRANGEBYSCORE<br/>（期限到来取得）"]
```

**コア操作**:

| 操作 | Redis コマンド | 用途 |
|------|----------------|------|
| IP スケジュール | `ZADD key NX score member` | 次回スケジュール時刻を設定/更新 |
| 期限到来取得 | `ZRANGEBYSCORE key -inf now` | 全期限到来タスクを取得 |
| 最近取得 | `ZRANGE key 0 0 WITHSCORES` | 精密スリープ計算用 |
| IP 削除 | `ZREM key member` | IP 削除時にクリーンアップ |
| カウント | `ZCARD key` | キュー深度モニタリング |

### 2.3 精密スリープメカニズム

```go
// internal/scheduler/ip_scheduler.go:196-254
func (s *IPScheduler) scheduleLoop(ctx context.Context) {
    for {
        // 1. 最近のスケジュール時刻を取得
        nextTime, exists, _ := s.scheduleStore.GetNextTime(ctx)

        // 2. 精密スリープ時間を計算
        if exists && nextTime.After(time.Now()) {
            sleepDuration := time.Until(nextTime)
            // 上限 5 分、長時間 ctx.Done に応答しないのを防ぐ
            if sleepDuration > maxSleepDuration {
                sleepDuration = maxSleepDuration
            }
            select {
            case <-ctx.Done():
                return
            case <-time.After(sleepDuration):
            }
        }

        // 3. 期限到来タスクを取得して処理
        s.checkAndSchedule(ctx)
    }
}
```

**スリープ戦略**:
- 次タスク時刻まで精密スリープ
- 最大スリープ 5 分（応答性保証）
- context キャンセル対応（グレースフルシャットダウン）

### 2.4 動的間隔計算

**基本公式**:
```
interval = BaseInterval / weight
interval = clamp(interval, MinInterval, MaxInterval)
```

**設定パラメータ**:

| パラメータ | デフォルト | 説明 |
|------------|----------|------|
| `BaseInterval` | 2h | 基本間隔 (weight=1.0) |
| `MinInterval` | 1h | ホット IP 下限 |
| `MaxInterval` | 2h | コールド IP 上限 |

**重みと間隔の対応**:

| Weight | 計算 | 実際の間隔 |
|--------|------|----------|
| 4.0 | 2h/4.0=30min | 1h (下限) |
| 2.0 | 2h/2.0=1h | 1h |
| 1.0 | 2h/1.0=2h | 2h |
| 0.5 | 2h/0.5=4h | 2h (上限) |

### 2.5 バックプレッシャー制御

```go
// internal/scheduler/ip_scheduler.go:295-365
func (s *IPScheduler) checkAndSchedule(ctx context.Context) {
    // 期限到来 IP をバッチ取得
    dueIPs, _ := s.scheduleStore.GetDue(ctx)

    for i := 0; i < len(dueIPs); i += s.config.BatchSize {
        batch := dueIPs[i:min(i+s.config.BatchSize, len(dueIPs))]

        for _, ipID := range batch {
            // タスクをキューに投入
            s.pushTasksForIP(ctx, ipID)
        }

        // キュー消化を待って次バッチを投入
        s.waitForQueueDrain(ctx)
    }
}
```

**バックプレッシャーパラメータ**:

| パラメータ | デフォルト | 説明 |
|------------|----------|------|
| `BatchSize` | 50 | バッチ投入タスク数 |
| `BackpressureThreshold` | 25 | キュー深度閾値 |

### 2.6 初期化フロー

```mermaid
flowchart TB
    Start["起動時"] --> Check{"ZSET は<br/>空か？"}
    Check -->|"空"| LoadDB["MySQL から<br/>全アクティブ IP を読み込み"]
    Check -->|"空でない"| Loop["スケジュールメインループ開始"]

    LoadDB --> CalcTime["各 IP の<br/>次回スケジュール時刻を計算"]

    subgraph CalcLogic["スケジュール時刻計算"]
        HasCrawled{"LastCrawledAt<br/>あり？"}
        HasCrawled -->|"はい"| Formula1["next = LastCrawledAt + interval"]
        HasCrawled -->|"いいえ"| Formula2["即時スケジュール（分散起動）"]
    end

    CalcTime --> CalcLogic
    CalcLogic --> BatchZADD["バッチ ZADD で ZSET に登録"]
    BatchZADD --> Loop
```

---

## 3. クローラー設計

### 3.1 コアアーキテクチャ

```mermaid
flowchart TB
    subgraph CrawlerService["Crawler Service"]
        direction TB
        subgraph TopLayer["実行層"]
            WorkerPool["Worker Pool<br/>（同時実行制御）"]
            BrowserManager["Browser<br/>Manager"]
            PageFetch["Page Fetch<br/>（DOM 解析）"]
        end
        subgraph BottomLayer["サポート層"]
            Watchdog["Watchdog<br/>（タイムアウト監視）"]
            Draining["Draining<br/>（スムーズ切り替え）"]
            Stealth["Stealth<br/>（アンチクロール回避）"]
        end
        WorkerPool --> BrowserManager --> PageFetch
        WorkerPool --> Watchdog
        BrowserManager --> Draining
        PageFetch --> Stealth
    end
```

### 3.2 タスク消費フロー

```go
// internal/crawler/crawl.go:18-208
func (s *Service) StartWorker(ctx context.Context) {
    for {
        // 1. セマフォで同時実行制御
        s.semaphore.Acquire()

        // 2. Redis からタスク取得 (BRPOPLPUSH)
        task, err := s.redisQueue.PopTask(ctx, 2*time.Second)

        // 3. タスクコルーチンを起動
        go func() {
            defer s.semaphore.Release()

            // 4. ウォッチドッグタイムアウト制御
            taskCtx, cancel := context.WithTimeout(ctx, taskTimeout)
            defer cancel()

            // 5. クロール実行
            resp, err := s.doCrawl(taskCtx, task)

            // 6. 結果投入 & タスク確認
            s.redisQueue.PushResult(ctx, resp)
            s.redisQueue.AckTask(ctx, task)
        }()
    }
}
```

### 3.3 固定ページ取得 (v2 モード)

```mermaid
flowchart TB
    subgraph crawlWithFixedPages["crawlWithFixedPages()"]
        direction TB
        subgraph Phase1["Phase 1: 出品中商品"]
            Loop1["for page := 1; page <= 5; page++"]
            URL1["url := BuildURL(keyword, ON_SALE)"]
            Fetch1["items := fetchPageContent(url)"]
            Append1["allItems = append(allItems, items)"]
            Loop1 --> URL1 --> Fetch1 --> Append1
        end
        subgraph Phase2["Phase 2: 売却済み商品"]
            Loop2["for page := 1; page <= 5; page++"]
            URL2["url := BuildURL(keyword, SOLD)"]
            Fetch2["items := fetchPageContent(url)"]
            Append2["allItems = append(allItems, items)"]
            Loop2 --> URL2 --> Fetch2 --> Append2
        end
        Return["Return: CrawlResponse{Items, TotalFound}"]
        Phase1 --> Phase2 --> Return
    end
```

**設計選択**:
- 固定 5+5 ページ、アンカー不要
- 直列実行（on_sale → sold）
- 各ページ独立エラーハンドリング、単一ページ失敗は全体に影響しない

### 3.4 ブラウザ Draining メカニズム

プロキシ切り替えやブラウザ再起動時、スムーズな移行が必要：

```mermaid
flowchart TB
    subgraph Draining["Browser Draining"]
        direction TB
        Step1["1. Draining モードに入る"]
        Step1a["このブラウザへの新タスクを拒否"]
        Step2["2. アクティブページの完了を待機 (最大 60s)"]
        Step2a["実行中タスクは完了まで継続"]
        Step3["3. バックグラウンドで新ブラウザを起動"]
        Step3a["新タスクは新ブラウザを使用"]
        Step4["4. 参照をアトミックに切り替え"]
        Step4a["旧ブラウザを非同期でクローズ"]

        Step1 --> Step1a --> Step2 --> Step2a --> Step3 --> Step3a --> Step4 --> Step4a
    end
```

### 3.5 エラー分類とプロキシ切り替え

```go
// internal/crawler/detect.go
func classifyError(err error) crawlErrorType {
    switch {
    case isTimeout(err):
        return errTypeTimeout
    case isBlocked(err):      // 403, 429, Cloudflare, CAPTCHA
        return errTypeBlocked
    case isNetworkError(err): // 接続失敗
        return errTypeNetwork
    default:
        return errTypeUnknown
    }
}

// プロキシ切り替え条件
func shouldActivateProxy(err error) bool {
    errType := classifyError(err)
    return errType == errTypeBlocked ||
           errType == errTypeTimeout ||
           errType == errTypeNetwork
}
```

**プロキシ切り替え戦略**:
- 連続 10 回失敗後にトリガー
- まずプロキシの健全性をチェック
- 切り替え後 30 分のクールダウン期間を設定

---

## 4. Pipeline 設計

### 4.1 コアアーキテクチャ

```mermaid
flowchart TB
    subgraph Pipeline["Pipeline"]
        WorkerPool["Worker Pool<br/>（設定可能な worker 数、デフォルト 2）"]

        subgraph processResult["processResult()"]
            direction TB
            subgraph Phase1["Phase 1: コア処理 (直列)"]
                P1a["冪等性チェック"]
                P1b["初回クロール検出"]
                P1c["ステートマシン処理"]
                P1a --> P1b --> P1c
            end
            subgraph Phase2["Phase 2: データ永続化 (並列)"]
                P2a["UpsertItemSnapshots (MySQL)"]
                P2b["AggregateHourlyStats (MySQL)"]
            end
            subgraph Phase3["Phase 3: 後処理 (並列)"]
                P3a["CheckAndCreateAlerts"]
                P3b["UpdateIPLastCrawled"]
                P3c["AdjustInterval"]
            end
            subgraph Phase35["Phase 3.5: 閉ループスケジュール"]
                P35["scheduler.ScheduleIP(ipID, nextTime)"]
            end
            subgraph Phase4["Phase 4: キャッシュ無効化 (非同期)"]
                P4a["InvalidateHourlyLeaderboard"]
                P4b["InvalidateIPDetailCache"]
            end
            Phase1 --> Phase2 --> Phase3 --> Phase35 --> Phase4
        end

        WorkerPool --> processResult
    end
```

### 4.2 ステートマシン処理

**Redis Key**: `animetop:item:{ip_id}:{source_id}`

```mermaid
classDiagram
    class ItemStateMachine {
        +string status : "available" | "sold"
        +int price : 現在価格 (円)
        +int64 first_seen : 初回発見タイムスタンプ
        +int64 last_seen : 最終更新タイムスタンプ
    }
    note for ItemStateMachine "Redis HASH<br/>TTL: on_sale=24h, sold=48h"
```

**状態遷移**:

| 遷移タイプ | トリガー条件 | 統計カウント |
|------------|--------------|--------------|
| `new_listing` | 商品初回出現かつ status=on_sale | inflow +1 |
| `sold` | 状態が available → sold | outflow +1 |
| `new_sold` | 商品初回出現かつ status=sold | outflow +1 |
| `price_change` | 価格変更かつまだ販売中 | - |
| `relisted` | 状態が sold → available (稀) | - |

### 4.3 閉ループスケジュール更新

```go
// internal/analyzer/pipeline.go:420-445
// Phase 3.5: 閉ループスケジュール
if p.scheduler != nil {
    // 新しい重みに基づいて次回間隔を計算
    nextInterval := p.config.IntervalAdjuster.BaseInterval
    if newWeight > 0 {
        nextInterval = time.Duration(float64(p.config.IntervalAdjuster.BaseInterval) / newWeight)
    }

    // 境界制限
    if nextInterval < p.config.IntervalAdjuster.MinInterval {
        nextInterval = p.config.IntervalAdjuster.MinInterval
    }
    if nextInterval > p.config.IntervalAdjuster.MaxInterval {
        nextInterval = p.config.IntervalAdjuster.MaxInterval
    }

    // ZSET を更新
    nextTime := time.Now().Add(nextInterval)
    p.scheduler.ScheduleIP(ctx, ipID, nextTime)
}
```

### 4.4 動的間隔調整

**調整ルール** (5+5 ページ設定基準):

| 条件 | アクション | 重み変化 |
|------|------------|----------|
| inflow > 500 または outflow > 500 | 加速 | weight += 0.25 |
| inflow < 250 かつ outflow < 15 | 減速 | weight -= 0.1 |
| その他 | 回帰 | 1.0 に向かう |

---

## 5. Redis データ構造詳細

### 5.1 データ構造概要

```mermaid
flowchart TB
    subgraph RedisStructures["Redis データ構造"]
        direction TB

        subgraph ZSET["ZSET (ソート済みセット)"]
            ZSET1["animetop:schedule:pending<br/>IP スケジュールキュー<br/>score: Unix timestamp, member: IP ID"]
        end

        subgraph LIST["LIST (リスト)"]
            LIST1["animetop:queue:tasks - タスクキュー"]
            LIST2["animetop:queue:tasks:processing - 処理中キュー"]
            LIST3["animetop:queue:tasks:deadletter - デッドレターキュー"]
            LIST4["animetop:queue:results - 結果キュー"]
            LIST5["animetop:queue:results:processing - 結果処理中"]
            LIST6["animetop:queue:results:deadletter - 結果デッドレター"]
        end

        subgraph HASH["HASH (ハッシュ)"]
            HASH1["animetop:item:{ip_id}:{source_id}<br/>商品ステートマシン<br/>fields: status, price, first_seen, last_seen<br/>TTL: on_sale=24h, sold=48h"]
            HASH2["animetop:queue:tasks:started<br/>タスク開始時刻<br/>field: task_id, value: timestamp"]
        end

        subgraph SET["SET (セット)"]
            SET1["animetop:queue:tasks:pending<br/>タスク重複排除セット<br/>member: ip:{ip_id}"]
            SET2["animetop:processed<br/>冪等性チェック<br/>member: task_id, TTL: 24h"]
        end

        subgraph STRING["STRING (文字列/JSON)"]
            STR1["animetop:leaderboard:{type}:{hours} - ランキングキャッシュ"]
            STR2["animetop:ip:{ip_id}:liquidity - 流動性キャッシュ"]
            STR3["animetop:ip:{ip_id}:hourly_stats:* - 時間別統計キャッシュ"]
            STR4["animetop:ip:{ip_id}:items:* - 商品一覧キャッシュ"]
        end
    end
```

### 5.2 LIST 詳細 - 信頼性キュー

**パターン**: BRPOPLPUSH 信頼性キュー

```mermaid
flowchart TB
    subgraph NormalFlow["通常フロー"]
        direction LR
        tasks1["tasks"] -->|"BRPOPLPUSH"| processing1["processing"]
        processing1 -->|"LREM<br/>（処理成功後 Ack）"| deleted1["（削除）"]
    end

    subgraph FailureRecovery["障害復旧"]
        direction LR
        processing2["processing"] -->|"LRANGE"| check["タイムアウトチェック"]
        check -->|"retry_count <= 3<br/>LPUSH"| tasks2["tasks（リトライ）"]
        check -->|"retry_count > 3"| deadletter["deadletter"]
    end

    NormalFlow --> FailureRecovery
```

---

## 6. システム連携とデータフロー

### 6.1 完全データフロー図

```mermaid
flowchart TB
    subgraph Step1["① スケジュールトリガー"]
        S1_Scheduler["Scheduler"] -->|"ZRANGEBYSCORE"| S1_DueIP["期限到来 IP 取得"]
        S1_DueIP -->|"PushTask"| S1_TaskQueue["Task Queue"]
    end

    subgraph Step2["② タスク消費"]
        S2_Crawler["Crawler"] -->|"BRPOPLPUSH"| S2_TaskQueue["Task Queue"]
        S2_TaskQueue --> S2_ProcessingQueue["Processing Queue"]
    end

    subgraph Step3["③ クロール実行"]
        S3_Crawler["Crawler"] -->|"go-rod"| S3_Mercari["Mercari"]
        S3_Mercari -->|"parse"| S3_Items["Items[]"]
        S3_Items -->|"PushResult"| S3_ResultQueue["Result Queue"]
    end

    subgraph Step4["④ タスク確認"]
        S4_Crawler["Crawler"] -->|"AckTask"| S4_Process["Processing Queue (LREM)<br/>+ Pending Set (SREM)"]
    end

    subgraph Step5["⑤ 結果処理"]
        S5_Pipeline["Pipeline"] -->|"BRPOPLPUSH"| S5_ResultQueue["Result Queue"]
        S5_ResultQueue --> S5_ResultProcessing["Result Processing Queue"]
    end

    subgraph Step6["⑥ ステートマシン更新"]
        S6_Pipeline["Pipeline"] -->|"HGETALL/HSET"| S6_Hash["Item HASH"]
        S6_Hash --> S6_Transitions["Transitions[]"]
    end

    subgraph Step7["⑦ データ永続化"]
        S7_Pipeline["Pipeline"] -->|"Upsert"| S7_MySQL["MySQL<br/>(ip_stats_hourly, item_snapshots)"]
    end

    subgraph Step8["⑧ 閉ループスケジュール"]
        S8_Pipeline["Pipeline"] -->|"ZADD"| S8_ZSET["Schedule ZSET<br/>（次回スケジュール時刻を更新）"]
    end

    subgraph Step9["⑨ キャッシュ無効化"]
        S9_Pipeline["Pipeline"] -->|"DEL/SCAN"| S9_Cache["キャッシュ Keys<br/>（ランキング + IP 詳細）"]
    end

    Step1 --> Step2 --> Step3 --> Step4
    Step4 --> Step5 --> Step6 --> Step7 --> Step8 --> Step9
```

### 6.2 コンポーネントインターフェース定義

```go
// IPScheduler インターフェース (Pipeline 呼び出し用)
type IPScheduler interface {
    ScheduleIP(ctx context.Context, ipID uint64, nextTime time.Time) error
}

// ScheduleStore インターフェース (Scheduler 使用)
type ScheduleStore interface {
    Schedule(ctx context.Context, ipID uint64, nextTime time.Time) error
    GetDue(ctx context.Context) ([]uint64, error)
    GetNextTime(ctx context.Context) (time.Time, bool, error)
    Remove(ctx context.Context, ipID uint64) error
    Count(ctx context.Context) (int64, error)
}

// RedisQueue インターフェース (Crawler/Pipeline 共用)
type RedisQueue interface {
    PushTask(ctx context.Context, task *pb.CrawlRequest) error
    PopTask(ctx context.Context, timeout time.Duration) (*pb.CrawlRequest, error)
    AckTask(ctx context.Context, task *pb.CrawlRequest) error
    PushResult(ctx context.Context, result *pb.CrawlResponse) error
    PopResult(ctx context.Context, timeout time.Duration) (*pb.CrawlResponse, error)
    AckResult(ctx context.Context, result *pb.CrawlResponse) error
}
```

### 6.3 メッセージシリアライゼーション (Protocol Buffers)

サービス間通信は **Protocol Buffers** でメッセージ形式を定義し、**protojson** で JSON にシリアライズして Redis に格納します。

#### 6.3.1 メッセージ定義

```protobuf
// proto/crawler.proto

// クロールリクエスト (Scheduler → Crawler)
message CrawlRequest {
  uint64 ip_id = 1;              // IP データベース ID
  string keyword = 2;            // 検索キーワード
  string task_id = 4;            // タスク追跡 ID (UUID)
  int64 created_at = 5;          // タスク作成時刻 (Unix タイムスタンプ)
  int32 retry_count = 9;         // リトライ回数
  int32 pages_on_sale = 10;      // 販売中ページ数 (デフォルト 5)
  int32 pages_sold = 11;         // 売却済みページ数 (デフォルト 5)
}

// 商品情報
message Item {
  string source_id = 1;          // 商品ソース ID (m123456789)
  string title = 2;              // 商品タイトル
  int32 price = 3;               // 価格 (円)
  string image_url = 4;          // 商品画像 URL
  string item_url = 5;           // 商品詳細ページ URL
  ItemStatus status = 6;         // 商品ステータス (on_sale/sold)
}

// クロールレスポンス (Crawler → Pipeline)
message CrawlResponse {
  uint64 ip_id = 1;              // IP データベース ID
  repeated Item items = 3;       // 取得した商品リスト
  int32 total_found = 4;         // 総数
  string error_message = 5;      // エラーメッセージ
  string task_id = 6;            // タスク追跡 ID
  int64 crawled_at = 7;          // クロール完了時刻
  int32 pages_crawled = 10;      // 実際のページ数
  int32 retry_count = 11;        // リトライ回数
}
```

#### 6.3.2 シリアライゼーションフロー

```mermaid
flowchart LR
    subgraph Scheduler
        S1["pb.CrawlRequest{}"]
        S2["protojson.Marshal()"]
        S3["JSON bytes"]
    end

    subgraph Redis
        R1["LPUSH tasks"]
        R2["BRPOPLPUSH"]
    end

    subgraph Crawler
        C1["JSON bytes"]
        C2["protojson.Unmarshal()"]
        C3["pb.CrawlRequest{}"]
    end

    S1 --> S2 --> S3 --> R1
    R1 --> R2 --> C1 --> C2 --> C3
```

#### 6.3.3 Redis 内の JSON フォーマット

```json
// Task Queue (animetop:queue:tasks)
{
  "ipId": "11",
  "keyword": "鬼滅の刃",
  "taskId": "550e8400-e29b-41d4-a716-446655440000",
  "createdAt": "1705890000",
  "pagesOnSale": 5,
  "pagesSold": 5
}

// Result Queue (animetop:queue:results)
{
  "ipId": "11",
  "items": [
    {
      "sourceId": "m1234567890",
      "title": "鬼滅の刃 フィギュア",
      "price": 5000,
      "imageUrl": "https://...",
      "itemUrl": "https://...",
      "status": "ITEM_STATUS_ON_SALE"
    }
  ],
  "totalFound": 50,
  "taskId": "550e8400-e29b-41d4-a716-446655440000",
  "crawledAt": "1705890150",
  "pagesCrawled": 10
}
```

#### 6.3.4 protojson を選択した理由

| 比較項目 | protojson (JSON) | バイナリ proto |
|----------|------------------|----------------|
| 可読性 | ✅ `redis-cli` で直接確認可能 | ❌ デコードが必要 |
| デバッグ | ✅ ログフレンドリー | ❌ 読みにくい |
| フィールド互換性 | ✅ 欠落フィールドは自動でゼロ値 | ✅ 同様 |
| サイズ | 大きい (~2x) | 小さい |
| 性能 | 十分 (キューシナリオ) | より高速 |

**選択理由**: キューメッセージ量は少ない (~10 msg/min) ため、極限の性能より可読性を優先。

#### 6.3.5 コード生成

```bash
# proto から Go コードを生成
make proto

# または直接実行
protoc --go_out=. --go_opt=module=animetop proto/crawler.proto
```

生成ファイル: `proto/pb/crawler.pb.go`

### 6.4 初期化順序

```go
// cmd/analyzer/main.go

func main() {
    // 1. インフラストラクチャ
    db := initMySQL(cfg.MySQL)
    rdb := initRedis(cfg.Redis)
    queue := redisqueue.NewClientWithRedis(rdb)

    // 2. スケジュールストア (Scheduler より先)
    scheduleStore := scheduler.NewRedisScheduleStore(rdb, logger)

    // 3. スケジューラ (Pipeline より先)
    ipScheduler := scheduler.NewIPScheduler(db, rdb, queue, scheduleStore, cfg, logger)

    // 4. Pipeline (Scheduler に依存)
    pipeline := analyzer.NewPipeline(db, rdb, queue, analyzerCfg, pipelineCfg, ipScheduler)

    // 5. API Server (Pipeline + Scheduler に依存)
    server := api.NewServer(db, rdb, pipeline, ipScheduler, logger, apiCfg)

    // 6. 起動順序
    pipeline.Start(ctx)     // まずコンシューマを起動
    ipScheduler.Start(ctx)  // 次にプロデューサを起動
    server.Start()          // 最後に API を起動
}
```

---

## 7. 信頼性保証

### 7.1 障害シナリオと対応

| 障害シナリオ | 影響 | 対応メカニズム |
|--------------|------|----------------|
| Crawler クラッシュ | タスクが processing で停止 | Janitor 救援 (10分タイムアウト) |
| Pipeline クラッシュ | 結果が processing で停止 | Janitor 救援 |
| Redis 再起動 | ZSET/Queue データ消失 | RDB/AOF 永続化 |
| MySQL スロークエリ | Pipeline タイムアウト | 独立コネクションプール + タイムアウト制御 |
| ネットワーク不安定 | タスクタイムアウト | 自動リトライ (最大 3 回) |

### 7.2 Janitor 救援メカニズム

```go
// internal/scheduler/ip_scheduler.go:455-482
func (s *IPScheduler) janitorLoop(ctx context.Context) {
    ticker := time.NewTicker(s.config.JanitorInterval) // デフォルト 5 分
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // スタックしたタスクを救援
            s.queue.RescueStuckTasks(ctx, s.config.JanitorTimeout)
            // スタックした結果を救援
            s.queue.RescueStuckResults(ctx, s.config.JanitorTimeout)
        }
    }
}
```

**救援フロー**:

```mermaid
flowchart TB
    Scan["1. started_hash をスキャン<br/>タスク開始時刻を取得"] --> Compare["2. 現在時刻と比較<br/>タイムアウトタスクを特定 (> JanitorTimeout)"]
    Compare --> CheckRetry{"3. retry_count を確認"}
    CheckRetry -->|"< 3"| Requeue["再キュー<br/>retry_count++"]
    CheckRetry -->|">= 3"| DeadLetter["デッドレターキューへ移動"]
    Requeue --> Cleanup["4. started_hash レコードをクリーンアップ"]
    DeadLetter --> Cleanup
```

### 7.3 冪等性保証

**多層冪等性チェック**:

```mermaid
flowchart TB
    subgraph IdempotencyLayers["冪等性保証レイヤー"]
        direction TB
        subgraph Layer1["Layer 1: タスク重複排除 (Scheduler 層)"]
            L1["pending_set で同一 IP のアクティブタスクは1つのみ保証"]
        end
        subgraph Layer2["Layer 2: 結果冪等性 (Pipeline 層)"]
            L2["processed_set で task_id の処理済みをチェック"]
        end
        subgraph Layer3["Layer 3: データベース冪等性 (MySQL 層)"]
            L3["ON DUPLICATE KEY UPDATE で upsert アトミック性を保証"]
        end
        Layer1 --> Layer2 --> Layer3
    end
```

### 7.4 データ整合性

**最終整合性モデル**:

```mermaid
flowchart TB
    subgraph WriteOrder["書き込み順序"]
        direction LR
        W1["1. State Machine<br/>(Redis HASH)"] -->|"即座に可視"| W2["2. MySQL<br/>(統計データ)"]
        W2 -->|"トランザクションコミット後に可視"| W3["3. Cache Invalidation"]
        W3 -->|"API 次回リクエストで MySQL からロード"| W4["完了"]
    end

    subgraph ReadOrder["読み取り順序"]
        direction LR
        R1["1. API がまず Redis キャッシュを読む"] --> R2{"キャッシュヒット？"}
        R2 -->|"Miss"| R3["2. MySQL を読む"]
        R3 --> R4["3. 非同期で Redis キャッシュに書き戻し"]
        R2 -->|"Hit"| R5["キャッシュデータを返す"]
    end

    subgraph ConsistencyWindow["整合性ウィンドウ"]
        CW1["キャッシュ TTL: 10 分"]
        CW2["能動的無効化: Pipeline 処理後即座にトリガー"]
    end
```

---

## 付録

### A. 設定パラメータ早見表

| パラメータ | 環境変数 | デフォルト | 説明 |
|------------|----------|----------|------|
| BaseInterval | `SCHEDULER_BASE_INTERVAL` | 2h | 基本クロール間隔 |
| MinInterval | `SCHEDULER_MIN_INTERVAL` | 1h | 最小間隔 |
| MaxInterval | `SCHEDULER_MAX_INTERVAL` | 2h | 最大間隔 |
| PagesOnSale | `SCHEDULER_PAGES_ON_SALE` | 5 | 出品中ページ数 |
| PagesSold | `SCHEDULER_PAGES_SOLD` | 5 | 売却済みページ数 |
| BatchSize | `SCHEDULER_BATCH_SIZE` | 50 | バッチ投入数 |
| JanitorInterval | `JANITOR_INTERVAL` | 5m | Janitor 間隔 |
| JanitorTimeout | `JANITOR_TIMEOUT` | 10m | タスクタイムアウト閾値 |
| MaxRetries | - | 3 | 最大リトライ回数 |
| ItemTTLAvailable | `ANALYZER_ITEM_TTL_AVAILABLE` | 24h | on_sale TTL |
| ItemTTLSold | `ANALYZER_ITEM_TTL_SOLD` | 48h | sold TTL |

### B. Redis Key 早見表

| Key パターン | 型 | 説明 |
|--------------|------|------|
| `animetop:schedule:pending` | ZSET | スケジュールキュー |
| `animetop:queue:tasks` | LIST | タスクキュー |
| `animetop:queue:tasks:processing` | LIST | 処理中タスク |
| `animetop:queue:tasks:deadletter` | LIST | デッドレタータスク |
| `animetop:queue:tasks:pending` | SET | タスク重複排除 |
| `animetop:queue:tasks:started` | HASH | タスク開始時刻 |
| `animetop:queue:results` | LIST | 結果キュー |
| `animetop:queue:results:processing` | LIST | 処理中結果 |
| `animetop:item:{ip_id}:{source_id}` | HASH | 商品状態 |
| `animetop:processed` | SET | 冪等性チェック |
| `animetop:leaderboard:{type}:{hours}` | STRING | ランキングキャッシュ |
| `animetop:ip:{ip_id}:*` | STRING | IP 詳細キャッシュ |

### C. 主要コードエントリポイント

| モジュール | ファイル | エントリ関数 |
|------------|----------|--------------|
| Scheduler | `internal/scheduler/ip_scheduler.go` | `NewIPScheduler()`, `Start()` |
| ScheduleStore | `internal/scheduler/schedule_store.go` | `NewRedisScheduleStore()` |
| Crawler | `internal/crawler/service.go` | `NewService()`, `Start()` |
| CrawlLogic | `internal/crawler/crawl.go` | `StartWorker()`, `doCrawl()` |
| Pipeline | `internal/analyzer/pipeline.go` | `NewPipeline()`, `Start()` |
| StateMachine | `internal/analyzer/state_machine.go` | `ProcessItemsBatch()` |
| RedisQueue | `internal/pkg/redisqueue/client.go` | `NewClientWithRedis()` |
