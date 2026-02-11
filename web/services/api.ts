/**
 * API Service Layer
 * 封装后端 API 调用
 */

const API_BASE = import.meta.env.VITE_API_BASE || '';

/**
 * 通用 API 请求封装
 */
async function request<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const url = `${API_BASE}${endpoint}`;
  const response = await fetch(url, {
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
    ...options,
  });

  if (!response.ok) {
    throw new Error(`API Error: ${response.status} ${response.statusText}`);
  }

  return response.json();
}

// ==================== 类型定义 ====================

export interface ApiResponse<T> {
  code: number;
  data: T;
  message?: string;
}

export interface IPMetadata {
  id: number;
  name: string;
  name_en?: string;
  name_cn?: string;
  category?: string;
  tags?: string[];
  weight?: number;
  image_url?: string;
  status?: string;
  last_crawled_at?: string;
  created_at?: string;
  updated_at?: string;
}

export interface LeaderboardItem {
  rank: number;
  ip_id: number;
  ip_name: string;
  ip_name_en?: string;
  inflow: number;
  outflow: number;
  score: number;
}

export interface LeaderboardData {
  type: 'hot' | 'inflow' | 'outflow';
  hours: number;
  time_range: {
    start: string;
    end: string;
  };
  items: LeaderboardItem[];
  count: number;
  from_cache?: boolean;
}

export interface LiquidityData {
  ip_id: number;
  ip_name: string;
  on_sale_inflow: number;
  on_sale_outflow: number;
  on_sale_total: number;
  sold_inflow: number;
  sold_total: number;
  liquidity_index: number;
  hot_score: number;
  updated_at: string;
  from_cache?: boolean;
}

export interface HourlyStatItem {
  id: number;
  ip_id: number;
  hour_bucket: string;
  inflow: number;
  outflow: number;
  liquidity_index: number;
  active_count: number;
  avg_price: number;
  median_price: number;
  min_price: number;
  max_price: number;
  min_price_item?: {
    source_id: string;
    title: string;
    price: number;
    image_url: string;
    item_url: string;
  };
  max_price_item?: {
    source_id: string;
    title: string;
    price: number;
    image_url: string;
    item_url: string;
  };
  sample_count: number;
}

export interface HourlyStatsData {
  ip_id: number;
  ip_name: string;
  count: number;
  stats: HourlyStatItem[];
  from_cache?: boolean;
}

export interface ItemData {
  id: number;
  source_id: string;
  ip_id: number;
  title: string;
  price: number;
  status: 'on_sale' | 'sold';
  item_url: string;
  image_url: string;
  first_seen_at: string;
  last_seen_at: string;
  sold_at?: string;
  price_changed: boolean;
}

// 商品列表 API 响应 - data 直接是数组
export interface ItemsResponse {
  code: number;
  message: string;
  data: ItemData[];
  total: number;
  page: number;
  page_size: number;
  from_cache?: boolean;
}

// ==================== API 函数 ====================

/**
 * 健康检查
 */
export async function healthCheck(): Promise<{ status: string }> {
  return request('/health');
}

/**
 * 获取 IP 列表
 */
export async function getIPs(): Promise<ApiResponse<IPMetadata[]>> {
  return request('/api/v1/ips');
}

/**
 * 获取排行榜
 * @param type 排行榜类型: hot | inflow | outflow
 * @param hours 时间窗口 (1-24)
 * @param limit 返回数量 (最大100)
 */
export async function getLeaderboard(
  type: 'hot' | 'inflow' | 'outflow' = 'hot',
  hours: number = 24,
  limit: number = 10
): Promise<ApiResponse<LeaderboardData>> {
  const params = new URLSearchParams({
    type,
    hours: String(hours),
    limit: String(limit),
  });
  return request(`/api/v1/leaderboard?${params}`);
}

/**
 * 获取 IP 流动性数据
 * 注意：后端返回最近一个完整小时的数据，不支持 hours 参数
 * @param ipId IP ID
 */
export async function getIPLiquidity(
  ipId: number
): Promise<ApiResponse<LiquidityData>> {
  return request(`/api/v1/ips/${ipId}/liquidity`);
}

/**
 * 获取 IP 小时统计
 * @param ipId IP ID
 * @param limit 返回小时数
 */
export async function getIPHourlyStats(
  ipId: number,
  limit: number = 24
): Promise<ApiResponse<HourlyStatsData>> {
  const params = new URLSearchParams({ limit: String(limit) });
  return request(`/api/v1/ips/${ipId}/stats/hourly?${params}`);
}

/**
 * 获取 IP 商品列表
 * @param ipId IP ID
 * @param status 商品状态 (on_sale | sold)
 * @param page 页码
 * @param pageSize 每页数量
 */
export async function getIPItems(
  ipId: number,
  status?: 'on_sale' | 'sold',
  page: number = 1,
  pageSize: number = 20
): Promise<ItemsResponse> {
  const params = new URLSearchParams({
    page: String(page),
    page_size: String(pageSize),
  });
  if (status) {
    params.set('status', status);
  }
  return request(`/api/v1/ips/${ipId}/items?${params}`);
}

/**
 * 获取单个 IP 详情
 * @param ipId IP ID
 */
export async function getIP(ipId: number): Promise<ApiResponse<IPMetadata>> {
  return request(`/api/v1/ips/${ipId}`);
}
