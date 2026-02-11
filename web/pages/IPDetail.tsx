
import React, { useMemo, useState, useRef, useEffect, useCallback } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  AreaChart, Area, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid,
  Bar, Cell, ComposedChart, Scatter
} from 'recharts';
import {
  ArrowLeft, Share2, Package, ExternalLink, Activity,
  BarChart3, Zap, Loader2, AlertCircle
} from 'lucide-react';
import { getIP, getIPLiquidity, getIPHourlyStats, getIPItems, IPMetadata, LiquidityData, ItemData, HourlyStatItem } from '../services/api';

interface HoveredNode {
  item: ItemData;
  price: number;
  type: 'high' | 'low';
  x: number;
  y: number;
}

// IP 详情页不响应时间范围切换，始终显示最近 12 小时数据
const HOURLY_STATS_LIMIT = 12;

const IPDetail: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [hoveredNode, setHoveredNode] = useState<HoveredNode | null>(null);
  const hideTimeoutRef = useRef<number | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  const [ip, setIp] = useState<IPMetadata | null>(null);
  const [liquidity, setLiquidity] = useState<LiquidityData | null>(null);
  const [hourlyStats, setHourlyStats] = useState<HourlyStatItem[]>([]);
  const [items, setItems] = useState<ItemData[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const fetchData = async () => {
      if (!id) return;
      const ipId = Number(id);

      setLoading(true);
      setError(null);
      try {
        const [ipRes, liquidityRes, statsRes, itemsRes] = await Promise.all([
          getIP(ipId),
          getIPLiquidity(ipId),
          getIPHourlyStats(ipId, HOURLY_STATS_LIMIT),
          getIPItems(ipId, undefined, 1, 16),
        ]);

        if (ipRes.code === 0) setIp(ipRes.data);
        if (liquidityRes.code === 0) setLiquidity(liquidityRes.data);
        if (statsRes.code === 0 && statsRes.data.stats) setHourlyStats(statsRes.data.stats);
        if (itemsRes.data) setItems(itemsRes.data);
      } catch (err) {
        console.error('Failed to fetch IP details:', err);
        setError(err instanceof Error ? err.message : 'Failed to load data');
      } finally {
        setLoading(false);
      }
    };

    fetchData();
  }, [id]);

  // 生成占位图片 URL
  const getImageUrl = (ip: IPMetadata | null) => {
    if (!ip) return '';
    if (ip.image_url) return ip.image_url;
    const seed = ip.name.substring(0, 2).toUpperCase();
    const colors = ['9147FF', 'BCFF00', 'FF8C00', '1A1A2E', 'FF85A1', '4FB0FF', 'FF4B4B', '33A1FD', '2C2C2C', 'FFD93D'];
    const colorIndex = ip.name.charCodeAt(0) % colors.length;
    return `https://api.dicebear.com/7.x/initials/svg?seed=${seed}&backgroundColor=${colors[colorIndex]}`;
  };

  // 图表数据
  const liquidityChartData = useMemo(() => {
    return hourlyStats.map(stat => ({
      time: stat.hour_bucket.substring(11, 16),
      inflow: stat.inflow,
      outflow: stat.outflow,
    }));
  }, [hourlyStats]);

  // 价格范围图表数据 - 使用 API 返回的 min/max_price_item
  const priceRangeData = useMemo(() => {
    return hourlyStats.map((stat) => ({
      time: stat.hour_bucket.substring(11, 16),
      range: [stat.min_price, stat.max_price],
      low: stat.min_price,
      high: stat.max_price,
      lowItem: stat.min_price_item ? {
        id: 0,
        source_id: stat.min_price_item.source_id,
        ip_id: stat.ip_id,
        title: stat.min_price_item.title,
        price: stat.min_price_item.price,
        status: 'sold' as const,
        item_url: stat.min_price_item.item_url,
        image_url: stat.min_price_item.image_url,
        first_seen_at: '',
        last_seen_at: '',
        price_changed: false,
      } : null,
      highItem: stat.max_price_item ? {
        id: 0,
        source_id: stat.max_price_item.source_id,
        ip_id: stat.ip_id,
        title: stat.max_price_item.title,
        price: stat.max_price_item.price,
        status: 'sold' as const,
        item_url: stat.max_price_item.item_url,
        image_url: stat.max_price_item.image_url,
        first_seen_at: '',
        last_seen_at: '',
        price_changed: false,
      } : null,
    }));
  }, [hourlyStats]);

  // 必须在所有 early return 之前定义 hooks
  const handleNodeEnter = useCallback((data: any, type: 'high' | 'low', x: number, y: number) => {
    if (hideTimeoutRef.current) window.clearTimeout(hideTimeoutRef.current);
    const item = type === 'high' ? data.highItem : data.lowItem;
    if (!item) return;
    const price = type === 'high' ? data.high : data.low;
    setHoveredNode({ item, price, type, x, y });
  }, []);

  const handleNodeLeave = useCallback(() => {
    hideTimeoutRef.current = window.setTimeout(() => {
      setHoveredNode(null);
    }, 200);
  }, []);

  if (loading) {
    return (
      <div className="flex items-center justify-center h-96">
        <Loader2 className="w-8 h-8 animate-spin text-purple-600" />
        <span className="ml-3 text-sm font-tactical text-slate-500 uppercase">Loading IP Data...</span>
      </div>
    );
  }

  if (error || !ip) {
    return (
      <div className="flex flex-col items-center justify-center h-96 text-center">
        <AlertCircle className="w-12 h-12 text-red-500 mb-4" />
        <p className="text-sm font-tactical text-slate-700 uppercase mb-2">{error ? 'Connection Error' : 'Sector Missing'}</p>
        <p className="text-xs text-slate-500">{error || 'IP not found'}</p>
        <button
          onClick={() => navigate(-1)}
          className="mt-6 px-6 py-2 bg-purple-600 text-white font-tactical text-xs uppercase hover:bg-purple-700 transition-colors"
        >
          Back to Hub
        </button>
      </div>
    );
  }

  return (
    <div className="space-y-6 lg:space-y-10 animate-in fade-in duration-700 max-w-[1700px] mx-auto pb-24">

      {/* HEADER SECTION */}
      <header className="flex flex-col xl:flex-row items-stretch bg-white border-b-4 lg:border-b-8 border-purple-600 shadow-xl overflow-hidden">
        <div className="w-full xl:w-[400px] h-64 lg:h-auto bg-slate-900 relative shrink-0">
          <img src={getImageUrl(ip)} alt={ip.name} className="w-full h-full object-cover opacity-80" />
          <div className="absolute inset-0 bg-gradient-to-t from-slate-900 via-transparent to-transparent" />
          <div className="absolute bottom-6 left-6">
            <span className="px-2 py-0.5 bg-lime-400 text-purple-900 text-[8px] font-tactical font-black uppercase mb-2 inline-block">Tactical_Monitoring</span>
            <h1 className="text-3xl lg:text-4xl font-tactical font-black text-white uppercase">{ip.name}</h1>
          </div>
        </div>
        <div className="flex-1 p-6 lg:p-10 flex flex-col justify-between">
          <div className="flex justify-between items-start">
             <div className="space-y-4">
                <button onClick={() => navigate(-1)} className="flex items-center gap-2 text-slate-400 hover:text-purple-600 transition-colors">
                   <ArrowLeft size={16} />
                   <span className="text-[10px] font-tactical font-black uppercase tracking-widest">Back to Hub</span>
                </button>
                <div className="flex flex-wrap gap-2">
                   {ip.tags?.map(tag => (
                     <span key={tag} className="px-2 py-0.5 bg-slate-100 text-[8px] font-tactical font-black text-slate-500 border border-slate-200 uppercase">#{tag}</span>
                   ))}
                </div>
             </div>
             <Share2 size={20} className="text-slate-300 hover:text-purple-600 cursor-pointer" />
          </div>
          <div className="flex flex-col md:flex-row items-start md:items-end justify-between gap-6 mt-8">
             <div className="flex items-center gap-6 bg-slate-50 p-6 border-l-4 border-lime-400">
                <div className="text-3xl lg:text-5xl font-mono font-black text-slate-900">
                  {liquidity ? liquidity.hot_score.toFixed(2) : '-'}
                </div>
                <div className="h-10 w-px bg-slate-200" />
                <p className="text-[10px] font-tactical font-black text-purple-600 uppercase">Hot Score</p>
             </div>
             <a
               href={`https://jp.mercari.com/search?keyword=${encodeURIComponent(ip.name)}`}
               target="_blank"
               rel="noopener noreferrer"
               className="flex items-center gap-3 bg-purple-600 text-white px-10 py-5 font-tactical font-black text-sm uppercase tracking-widest hover:bg-slate-900 transition-all"
             >
                <Zap size={20} className="text-lime-400 fill-lime-400" />
                Engage Sector
             </a>
          </div>
        </div>
      </header>

      {/* STATS SUMMARY */}
      {liquidity && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <div className="bg-white p-6 border-l-4 border-lime-500 shadow-md">
            <p className="text-[10px] font-tactical font-black text-slate-400 uppercase mb-2">Inflow (On Sale)</p>
            <p className="text-2xl font-mono font-black text-lime-600">+{liquidity.on_sale_inflow}</p>
          </div>
          <div className="bg-white p-6 border-l-4 border-purple-600 shadow-md">
            <p className="text-[10px] font-tactical font-black text-slate-400 uppercase mb-2">Outflow (Sold)</p>
            <p className="text-2xl font-mono font-black text-purple-600">-{liquidity.sold_inflow}</p>
          </div>
          <div className="bg-white p-6 border-l-4 border-orange-500 shadow-md">
            <p className="text-[10px] font-tactical font-black text-slate-400 uppercase mb-2">Liquidity Index</p>
            <p className="text-2xl font-mono font-black text-orange-600">{liquidity.liquidity_index.toFixed(2)}</p>
          </div>
          <div className="bg-white p-6 border-l-4 border-slate-400 shadow-md">
            <p className="text-[10px] font-tactical font-black text-slate-400 uppercase mb-2">Hot Score</p>
            <p className="text-2xl font-mono font-black text-slate-700">{liquidity.hot_score.toFixed(2)}</p>
          </div>
        </div>
      )}

      {/* CHARTS SECTION */}
      <section className="grid grid-cols-1 lg:grid-cols-2 gap-8">
        {/* Signal Graph - 纯展示模式，只有悬浮提示 */}
        <div className="bg-white p-6 lg:p-8 border-2 border-purple-600 shadow-lg select-none">
           <h3 className="font-tactical font-black text-sm lg:text-lg text-slate-900 uppercase flex items-center gap-2 mb-8">
             <Activity size={18} className="text-lime-500" />
             Neural Link Signal (12H)
           </h3>
           {liquidityChartData.length === 0 ? (
             <div className="h-[250px] lg:h-[350px] flex items-center justify-center text-slate-400 font-tactical text-sm uppercase">
               No Data Available
             </div>
           ) : (
             <div className="h-[250px] lg:h-[350px]">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={liquidityChartData}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#F1F5F9" vertical={false} />
                  <XAxis dataKey="time" tick={{ fontSize: 10 }} />
                  <YAxis hide />
                  <Tooltip
                    contentStyle={{ backgroundColor: '#1A1A2E', border: 'none', color: '#FFF', borderRadius: 4, fontSize: 12 }}
                    labelStyle={{ color: '#94A3B8', marginBottom: 4 }}
                    formatter={(value: number, name: string) => [
                      value,
                      name === 'inflow' ? 'Inflow' : 'Outflow'
                    ]}
                  />
                  <Area type="monotone" dataKey="inflow" stroke="#BCFF00" strokeWidth={3} fill="#BCFF0011" name="inflow" />
                  <Area type="monotone" dataKey="outflow" stroke="#9147FF" strokeWidth={3} fill="#9147FF11" name="outflow" />
                </AreaChart>
              </ResponsiveContainer>
            </div>
           )}
        </div>

        {/* Market Volatility Graph - 只允许价格点的悬浮+点击 */}
        <div
          ref={containerRef}
          className="bg-white p-6 lg:p-8 border-2 border-slate-900 shadow-lg relative overflow-hidden select-none"
        >
          {/* 浮窗 - 相对于图表容器定位 */}
          {hoveredNode && (
            <div
              className="absolute z-50 pointer-events-none"
              style={{
                left: Math.min(Math.max(hoveredNode.x - 100, 10), (containerRef.current?.offsetWidth || 400) - 220),
                top: hoveredNode.y > 200 ? hoveredNode.y - 110 : hoveredNode.y + 20,
              }}
            >
              <div className="bg-slate-900 border border-slate-700 rounded-lg p-3 shadow-xl w-[200px]">
                <div className="flex items-center gap-2 mb-2">
                  <div className={`w-2 h-2 rounded-full ${hoveredNode.type === 'high' ? 'bg-orange-500' : 'bg-purple-500'}`} />
                  <span className="text-[10px] font-bold text-slate-400 uppercase">
                    {hoveredNode.type === 'high' ? 'MAX' : 'MIN'} Price
                  </span>
                </div>
                <div className="flex gap-3">
                  <img
                    src={hoveredNode.item.image_url || ''}
                    className="w-12 h-12 object-cover rounded border border-slate-700"
                    onError={(e) => (e.currentTarget.style.display = 'none')}
                  />
                  <div className="flex-1 min-w-0">
                    <p className="text-[9px] text-slate-400 truncate">{hoveredNode.item.title}</p>
                    <p className="text-base font-mono font-bold text-white">¥{hoveredNode.price.toLocaleString()}</p>
                  </div>
                </div>
              </div>
            </div>
          )}

          <h3 className="font-tactical font-black text-sm lg:text-lg text-slate-900 uppercase flex items-center gap-2 mb-8">
            <BarChart3 size={18} className="text-orange-500" />
            Market Volatility Index
          </h3>
          {priceRangeData.length === 0 || priceRangeData.every(d => d.low === 0 && d.high === 0) ? (
            <div className="h-[250px] lg:h-[350px] flex items-center justify-center text-slate-400 font-tactical text-sm uppercase">
              No Price Data Available
            </div>
          ) : (
            <div className="h-[250px] lg:h-[350px]">
              <ResponsiveContainer width="100%" height="100%">
                <ComposedChart data={priceRangeData}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#F1F5F9" vertical={false} />
                  <XAxis dataKey="time" tick={{ fontSize: 10 }} />
                  <YAxis scale="sqrt" domain={['auto', 'auto']} hide />
                  <Tooltip cursor={false} content={() => null} />
                  <Bar dataKey="range" isAnimationActive={false} style={{ pointerEvents: 'none' }}>
                     {priceRangeData.map((_, index) => (
                       <Cell key={`cell-${index}`} fill="#E2E8F0" stroke="#CBD5E1" strokeWidth={1} />
                     ))}
                  </Bar>
                  <Scatter
                    dataKey="high"
                    isAnimationActive={false}
                    shape={(props: any) => {
                      const { cx, cy, payload } = props;
                      if (!payload.highItem) return null;
                      return (
                        <circle
                          cx={cx}
                          cy={cy}
                          r={6}
                          fill="#FF8C00"
                          stroke="#FFF"
                          strokeWidth={2}
                          style={{ cursor: 'pointer' }}
                          onMouseEnter={() => handleNodeEnter(payload, 'high', cx, cy)}
                          onMouseLeave={handleNodeLeave}
                          onClick={() => payload.highItem?.item_url && window.open(payload.highItem.item_url, '_blank')}
                        />
                      );
                    }}
                  />
                  <Scatter
                    dataKey="low"
                    isAnimationActive={false}
                    shape={(props: any) => {
                      const { cx, cy, payload } = props;
                      if (!payload.lowItem) return null;
                      return (
                        <circle
                          cx={cx}
                          cy={cy}
                          r={6}
                          fill="#9147FF"
                          stroke="#FFF"
                          strokeWidth={2}
                          style={{ cursor: 'pointer' }}
                          onMouseEnter={() => handleNodeEnter(payload, 'low', cx, cy)}
                          onMouseLeave={handleNodeLeave}
                          onClick={() => payload.lowItem?.item_url && window.open(payload.lowItem.item_url, '_blank')}
                        />
                      );
                    }}
                  />
                </ComposedChart>
              </ResponsiveContainer>
            </div>
          )}
        </div>
      </section>

      {/* ITEMS GRID */}
      <div className="bg-white p-6 lg:p-8 border-t-4 border-purple-600 shadow-lg">
        <div className="flex items-center justify-between mb-6">
          <h3 className="font-tactical font-black text-sm lg:text-lg text-slate-900 uppercase flex items-center gap-2">
            <Package size={18} className="text-purple-600" />
            Latest Items
          </h3>
          <span className="text-[10px] font-tactical font-black text-slate-400 uppercase">
            {items.length} Items
          </span>
        </div>
        {items.length === 0 ? (
          <div className="py-12 text-center text-slate-400 font-tactical text-sm uppercase">
            No Items Available
          </div>
        ) : (
          <div className="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-6 xl:grid-cols-8 gap-4">
             {items.map(item => (
               <a
                 key={item.id}
                 href={item.item_url}
                 target="_blank"
                 rel="noopener noreferrer"
                 className="bg-white border border-slate-100 hover:border-purple-600 transition-all p-3 group shadow-sm"
               >
                  <img src={item.image_url || 'https://picsum.photos/200'} className="aspect-square object-cover mb-3" />
                  <p className="text-[9px] font-tactical font-bold text-slate-800 line-clamp-2 uppercase h-8">{item.title}</p>
                  <div className="mt-2 pt-2 border-t border-slate-50 flex justify-between items-center">
                     <span className="font-mono font-black text-purple-600 text-xs">{item.price.toLocaleString()}</span>
                     <ExternalLink size={12} className="text-slate-300" />
                  </div>
                  <div className="mt-1">
                    <span className={`text-[8px] font-tactical font-black uppercase ${item.status === 'sold' ? 'text-red-500' : 'text-lime-500'}`}>
                      {item.status === 'sold' ? 'SOLD' : 'ON SALE'}
                    </span>
                  </div>
               </a>
             ))}
          </div>
        )}
      </div>
    </div>
  );
};

export default IPDetail;
