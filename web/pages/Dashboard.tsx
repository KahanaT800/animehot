
import React, { useMemo, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  ResponsiveContainer,
  Cell,
  LabelList
} from 'recharts';
import { ShieldAlert, TrendingUp, TrendingDown, Loader2 } from 'lucide-react';
import { getLeaderboard, getIPs, LeaderboardItem, IPMetadata } from '../services/api';
import { TimeRange } from '../types';

interface DashboardProps {
  timeRange: TimeRange;
}

const Dashboard: React.FC<DashboardProps> = ({ timeRange }) => {
  const navigate = useNavigate();
  const [hotList, setHotList] = useState<LeaderboardItem[]>([]);
  const [inflowList, setInflowList] = useState<LeaderboardItem[]>([]);
  const [outflowList, setOutflowList] = useState<LeaderboardItem[]>([]);
  const [ipMap, setIpMap] = useState<Record<number, IPMetadata>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [hoveredBarIndex, setHoveredBarIndex] = useState<number | null>(null);

  // TimeRange 直接作为 API hours 参数
  const apiHours = timeRange;

  useEffect(() => {
    const fetchData = async () => {
      setLoading(true);
      setError(null);
      try {
        const [hotRes, inflowRes, outflowRes, ipsRes] = await Promise.all([
          getLeaderboard('hot', apiHours, 10),
          getLeaderboard('inflow', apiHours, 10),
          getLeaderboard('outflow', apiHours, 10),
          getIPs(),
        ]);

        if (hotRes.code === 0) setHotList(hotRes.data.items || []);
        if (inflowRes.code === 0) setInflowList(inflowRes.data.items || []);
        if (outflowRes.code === 0) setOutflowList(outflowRes.data.items || []);

        if (ipsRes.code === 0 && ipsRes.data) {
          const map: Record<number, IPMetadata> = {};
          ipsRes.data.forEach(ip => {
            map[ip.id] = ip;
          });
          setIpMap(map);
        }
      } catch (err) {
        console.error('Failed to fetch leaderboard:', err);
        setError(err instanceof Error ? err.message : 'Failed to load data');
      } finally {
        setLoading(false);
      }
    };

    fetchData();
  }, [apiHours]);

  const medianLiquidity = useMemo(() => {
    const allItems = [...hotList, ...inflowList, ...outflowList];
    if (allItems.length === 0) return 0;
    const volumes = allItems.map(s => s.inflow + s.outflow).sort((a, b) => a - b);
    const mid = Math.floor(volumes.length / 2);
    return volumes.length % 2 !== 0 ? volumes[mid] : (volumes[mid - 1] + volumes[mid]) / 2;
  }, [hotList, inflowList, outflowList]);

  const totalOps = useMemo(() => {
    // 使用 hot 列表的数据计算（避免重复）
    return hotList.reduce((acc, curr) => acc + curr.inflow + curr.outflow, 0);
  }, [hotList]);

  const chartData = useMemo(() => {
    return hotList.map(item => ({
      name: item.ip_name,
      score: parseFloat(item.score.toFixed(2)),
      id: item.ip_id,
    }));
  }, [hotList]);

  // 获取 IP 图片 URL
  const getImageUrl = (ipId: number, name: string) => {
    const ip = ipMap[ipId];
    if (ip?.image_url) return ip.image_url;
    // 降级为名称缩写占位图
    const seed = name.substring(0, 2).toUpperCase();
    const colors = ['9147FF', 'BCFF00', 'FF8C00', '1A1A2E', 'FF85A1', '4FB0FF', 'FF4B4B', '33A1FD', '2C2C2C', 'FFD93D'];
    const colorIndex = name.charCodeAt(0) % colors.length;
    return `https://api.dicebear.com/7.x/initials/svg?seed=${seed}&backgroundColor=${colors[colorIndex]}`;
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-96">
        <Loader2 className="w-8 h-8 animate-spin text-purple-600" />
        <span className="ml-3 text-sm font-tactical text-slate-500 uppercase">Loading Data...</span>
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex flex-col items-center justify-center h-96 text-center">
        <ShieldAlert className="w-12 h-12 text-red-500 mb-4" />
        <p className="text-sm font-tactical text-slate-700 uppercase mb-2">Connection Error</p>
        <p className="text-xs text-slate-500">{error}</p>
      </div>
    );
  }

  return (
    <div className="space-y-6 lg:space-y-10 animate-in fade-in slide-in-from-bottom-6 duration-700 pb-20">

      {/* Banner: Stacks on Mobile */}
      <div className="flex flex-col md:flex-row justify-between items-start md:items-end gap-6 bg-purple-600 p-6 lg:p-8 border-l-8 border-lime-400 shadow-2xl relative overflow-hidden">
         <div className="absolute right-0 top-0 opacity-10 pointer-events-none hidden lg:block">
            <ShieldAlert size={200} className="text-white rotate-12" />
         </div>
         <div className="relative z-10">
            <div className="flex items-center gap-2 mb-2">
               <div className="w-2 h-2 lg:w-3 lg:h-3 bg-lime-400 rounded-full animate-ping" />
               <span className="text-lime-400 font-tactical font-black text-[10px] tracking-widest uppercase">System Online</span>
            </div>
            <h1 className="text-2xl lg:text-4xl font-tactical font-black text-white tracking-tighter uppercase leading-none">Market Ranking</h1>
         </div>
         <div className="flex flex-wrap gap-2 lg:gap-4 relative z-10 w-full md:w-auto">
            <div className="bg-purple-900/50 p-3 lg:p-4 border border-lime-400/50 flex-1 md:min-w-[180px]">
               <p className="text-[8px] font-tactical text-purple-200 uppercase font-bold mb-1">Median Liquidity</p>
               <p className="text-xl lg:text-2xl font-mono font-black text-lime-400">
                {Math.floor(medianLiquidity).toLocaleString()} <span className="text-[10px] opacity-50 text-white font-tactical">UNITS</span>
               </p>
            </div>
            <div className="bg-purple-900/50 p-3 lg:p-4 border border-purple-400/30 flex-1 md:min-w-[180px]">
               <p className="text-[8px] font-tactical text-purple-200 uppercase font-bold mb-1">Total OPS</p>
               <p className="text-xl lg:text-2xl font-mono font-black text-orange-500">
                {totalOps.toLocaleString()}
               </p>
            </div>
         </div>
      </div>

      {/* Main Chart: Adaptive Height */}
      <div className="bg-white p-4 lg:p-8 border-t-2 lg:border-t-4 border-purple-600 shadow-xl relative">
        <div className="flex justify-between items-center mb-6 lg:mb-10">
           <h2 className="text-lg lg:text-2xl font-tactical font-black text-slate-800 uppercase tracking-tighter">Hot Index Top 10</h2>
           <span className="text-[8px] font-tactical font-black uppercase text-purple-600 tracking-widest">Target_ID</span>
        </div>

        {chartData.length === 0 ? (
          <div className="h-[350px] lg:h-[500px] flex items-center justify-center text-slate-400 font-tactical text-sm uppercase">
            No Data Available
          </div>
        ) : (
          <div className="h-[350px] lg:h-[500px]">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={chartData} layout="vertical" margin={{ left: 10, right: 50 }}>
                <XAxis type="number" hide domain={[0, 'auto']} />
                <YAxis type="category" hide />
                <Bar
                  dataKey="score"
                  radius={[0, 4, 4, 0]}
                  onClick={(data) => navigate(`/ip/${data.id}`)}
                  className="cursor-pointer"
                  minPointSize={80}
                  isAnimationActive={false}
                  onMouseLeave={() => setHoveredBarIndex(null)}
                >
                  <LabelList
                    dataKey="name"
                    position="insideLeft"
                    fill="#FFF"
                    fontSize={11}
                    fontFamily="Orbitron"
                    fontWeight="900"
                    style={{ textShadow: '0 1px 2px rgba(0,0,0,0.3)' }}
                  />
                  <LabelList
                    dataKey="score"
                    position="right"
                    fill="#64748b"
                    fontSize={10}
                    fontFamily="JetBrains Mono"
                    fontWeight="900"
                    formatter={(val: number) => val.toFixed(2)}
                  />
                  {chartData.map((entry, index) => (
                    <Cell
                      key={`cell-${index}`}
                      fill={index === 0 ? '#BCFF00' : index < 3 ? '#9147FF' : '#94A3B8'}
                      stroke={hoveredBarIndex === index ? '#64748b' : 'transparent'}
                      strokeWidth={hoveredBarIndex === index ? 3 : 0}
                      onMouseEnter={() => setHoveredBarIndex(index)}
                    />
                  ))}
                </Bar>
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>

      {/* Rankings: Grid stacks on mobile */}
      <div className="grid grid-cols-1 xl:grid-cols-2 gap-6 lg:gap-8">

        {/* Inflow List */}
        <div className="bg-white border-t-2 lg:border-t-4 border-lime-500 shadow-lg overflow-hidden flex flex-col">
          <div className="p-4 lg:p-6 border-b border-slate-50 flex items-center gap-3 bg-lime-50/20">
             <TrendingUp size={18} className="text-lime-500" />
             <h3 className="text-sm lg:text-xl font-tactical font-black text-slate-800 uppercase tracking-tighter">Inflow: Top 10</h3>
          </div>
          <div className="divide-y divide-slate-50">
            {inflowList.length === 0 ? (
              <div className="p-8 text-center text-slate-400 font-tactical text-xs uppercase">No Data</div>
            ) : (
              inflowList.map((item, idx) => (
                <div key={item.ip_id} onClick={() => navigate(`/ip/${item.ip_id}`)} className="flex items-center justify-between p-3 lg:p-4 hover:bg-lime-50 transition-all cursor-pointer">
                  <div className="flex items-center gap-3">
                    <span className="font-mono font-black text-sm text-slate-300 w-4">{idx + 1}</span>
                    <div className="w-10 h-10 lg:w-12 lg:h-12 bg-slate-900 border-2 border-slate-900 overflow-hidden shrink-0">
                      <img src={getImageUrl(item.ip_id, item.ip_name)} alt="" className="w-full h-full object-cover" />
                    </div>
                    <div className="flex flex-col">
                      <span className="text-[10px] lg:text-sm font-tactical font-black text-slate-800 truncate w-32">{item.ip_name}</span>
                      <span className="text-[8px] text-slate-400 font-mono font-bold uppercase">{item.ip_name_en}</span>
                    </div>
                  </div>
                  <div className="text-right">
                    <span className="text-sm lg:text-lg font-mono font-black text-lime-600">+{item.inflow}</span>
                  </div>
                </div>
              ))
            )}
          </div>
        </div>

        {/* Outflow List */}
        <div className="bg-white border-t-2 lg:border-t-4 border-purple-600 shadow-lg overflow-hidden flex flex-col">
          <div className="p-4 lg:p-6 border-b border-slate-50 flex items-center gap-3 bg-purple-50/20">
             <TrendingDown size={18} className="text-purple-600" />
             <h3 className="text-sm lg:text-xl font-tactical font-black text-slate-800 uppercase tracking-tighter">Outflow: Top 10</h3>
          </div>
          <div className="divide-y divide-slate-50">
            {outflowList.length === 0 ? (
              <div className="p-8 text-center text-slate-400 font-tactical text-xs uppercase">No Data</div>
            ) : (
              outflowList.map((item, idx) => (
                <div key={item.ip_id} onClick={() => navigate(`/ip/${item.ip_id}`)} className="flex items-center justify-between p-3 lg:p-4 hover:bg-purple-50 transition-all cursor-pointer">
                  <div className="flex items-center gap-3">
                    <span className="font-mono font-black text-sm text-slate-300 w-4">{idx + 1}</span>
                    <div className="w-10 h-10 lg:w-12 lg:h-12 bg-slate-900 border-2 border-slate-900 overflow-hidden shrink-0">
                      <img src={getImageUrl(item.ip_id, item.ip_name)} alt="" className="w-full h-full object-cover" />
                    </div>
                    <div className="flex flex-col">
                      <span className="text-[10px] lg:text-sm font-tactical font-black text-slate-800 truncate w-32">{item.ip_name}</span>
                      <span className="text-[8px] text-slate-400 font-mono font-bold uppercase">{item.ip_name_en}</span>
                    </div>
                  </div>
                  <div className="text-right">
                    <span className="text-sm lg:text-lg font-mono font-black text-purple-600">-{item.outflow}</span>
                  </div>
                </div>
              ))
            )}
          </div>
        </div>

      </div>
    </div>
  );
};

export default Dashboard;
