
import React, { useState, useMemo, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { Grid, List, ChevronRight, ShoppingCart, TrendingUp, Search, Loader2, AlertCircle } from 'lucide-react';
import { getIPs, getLeaderboard, IPMetadata, LeaderboardItem } from '../services/api';
import { TimeRange } from '../types';

interface IPMarketProps {
  timeRange: TimeRange;
}

const IPMarket: React.FC<IPMarketProps> = ({ timeRange }) => {
  const navigate = useNavigate();
  const [view, setView] = useState<'grid' | 'table'>('grid');
  const [category, setCategory] = useState<string>('All');
  const [searchTerm, setSearchTerm] = useState('');
  const [ips, setIps] = useState<IPMetadata[]>([]);
  const [statsMap, setStatsMap] = useState<Record<number, LeaderboardItem>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // TimeRange 直接作为 API hours 参数
  const apiHours = timeRange;

  useEffect(() => {
    const fetchData = async () => {
      setLoading(true);
      setError(null);
      try {
        const [ipsRes, hotRes] = await Promise.all([
          getIPs(),
          getLeaderboard('hot', apiHours, 100),
        ]);

        if (ipsRes.code === 0) {
          setIps(ipsRes.data || []);
        }

        if (hotRes.code === 0 && hotRes.data.items) {
          const map: Record<number, LeaderboardItem> = {};
          hotRes.data.items.forEach(item => {
            map[item.ip_id] = item;
          });
          setStatsMap(map);
        }
      } catch (err) {
        console.error('Failed to fetch data:', err);
        setError(err instanceof Error ? err.message : 'Failed to load data');
      } finally {
        setLoading(false);
      }
    };

    fetchData();
  }, [apiHours]);

  const filteredIPs = useMemo(() => {
    return ips.filter(ip => {
      const matchCategory = category === 'All' || ip.category === category;
      const matchSearch = ip.name.toLowerCase().includes(searchTerm.toLowerCase()) ||
                          ip.name_en?.toLowerCase().includes(searchTerm.toLowerCase()) ||
                          ip.name_cn?.toLowerCase().includes(searchTerm.toLowerCase());
      return matchCategory && matchSearch;
    });
  }, [ips, category, searchTerm]);

  const categories = useMemo(() => {
    const cats = new Set<string>();
    ips.forEach(ip => {
      if (ip.category) cats.add(ip.category);
    });
    return ['All', ...Array.from(cats).sort()];
  }, [ips]);

  // 生成占位图片 URL
  const getImageUrl = (ip: IPMetadata) => {
    if (ip.image_url) return ip.image_url;
    const seed = ip.name.substring(0, 2).toUpperCase();
    const colors = ['9147FF', 'BCFF00', 'FF8C00', '1A1A2E', 'FF85A1', '4FB0FF', 'FF4B4B', '33A1FD', '2C2C2C', 'FFD93D'];
    const colorIndex = ip.name.charCodeAt(0) % colors.length;
    return `https://api.dicebear.com/7.x/initials/svg?seed=${seed}&backgroundColor=${colors[colorIndex]}`;
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-96">
        <Loader2 className="w-8 h-8 animate-spin text-purple-600" />
        <span className="ml-3 text-sm font-tactical text-slate-500 uppercase">Loading Market Data...</span>
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex flex-col items-center justify-center h-96 text-center">
        <AlertCircle className="w-12 h-12 text-red-500 mb-4" />
        <p className="text-sm font-tactical text-slate-700 uppercase mb-2">Connection Error</p>
        <p className="text-xs text-slate-500">{error}</p>
      </div>
    );
  }

  return (
    <div className="space-y-8 animate-in fade-in duration-300 max-w-[1600px] mx-auto pb-20">
      <div className="flex flex-col lg:flex-row justify-between items-start lg:items-center gap-6">
        <div className="flex items-center gap-4">
           <div className="w-12 h-12 bg-purple-600 flex items-center justify-center text-white shadow-lg">
             <ShoppingCart size={24} />
           </div>
           <div>
              <h1 className="text-3xl font-tactical font-black text-slate-900 uppercase tracking-tighter">Sector Asset Market</h1>
              <p className="text-[10px] font-mono font-bold text-slate-400 uppercase tracking-widest">Global IP Liquidity Access</p>
           </div>
        </div>

        <div className="flex flex-wrap items-center gap-4 w-full lg:w-auto">
          <div className="relative flex-1 lg:flex-none">
            <Search className="absolute left-4 top-1/2 -translate-y-1/2 text-slate-400" size={16} />
            <input
              type="text"
              placeholder="FILTER SECTOR..."
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              className="bg-white border-2 border-slate-100 px-12 py-2 text-[10px] font-tactical focus:border-purple-600 outline-none w-full lg:w-64"
            />
          </div>
          <div className="flex bg-white shadow-sm border border-slate-100 p-1 overflow-x-auto">
            {categories.map(cat => (
              <button
                key={cat}
                onClick={() => setCategory(cat)}
                className={`px-4 py-1.5 text-[10px] font-tactical font-black transition-all whitespace-nowrap ${
                  category === cat
                  ? 'bg-purple-600 text-white'
                  : 'text-slate-400 hover:text-purple-600'
                }`}
              >
                {cat.toUpperCase()}
              </button>
            ))}
          </div>
          <div className="flex bg-white border border-slate-100 p-1">
            <button
              onClick={() => setView('grid')}
              className={`p-1.5 transition-all ${view === 'grid' ? 'bg-slate-100 text-purple-600' : 'text-slate-300'}`}
            >
              <Grid size={18} />
            </button>
            <button
              onClick={() => setView('table')}
              className={`p-1.5 transition-all ${view === 'table' ? 'bg-slate-100 text-purple-600' : 'text-slate-300'}`}
            >
              <List size={18} />
            </button>
          </div>
        </div>
      </div>

      {view === 'table' ? (
        <div className="bg-white border-2 border-slate-900 shadow-2xl overflow-hidden overflow-x-auto">
          <table className="w-full text-left border-collapse min-w-[700px]">
            <thead>
              <tr className="bg-slate-900 text-[9px] font-tactical font-black text-slate-400 uppercase tracking-widest border-b border-slate-800">
                <th className="px-8 py-4">Sector Identifier</th>
                <th className="px-8 py-4">Classification</th>
                <th className="px-8 py-4 text-right">Inflow Signal</th>
                <th className="px-8 py-4 text-right">Outflow Signal</th>
                <th className="px-8 py-4 text-right">Sync Rate</th>
                <th className="px-8 py-4"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-50">
              {filteredIPs.map(ip => {
                const s = statsMap[ip.id];
                return (
                  <tr
                    key={ip.id}
                    onClick={() => navigate(`/ip/${ip.id}`)}
                    className="hover:bg-purple-50/40 transition-colors cursor-pointer group"
                  >
                    <td className="px-8 py-5">
                      <div className="flex items-center gap-5">
                        <div className="w-12 h-12 bg-slate-100 overflow-hidden shadow-sm group-hover:scale-105 transition-transform border border-slate-100">
                          <img src={getImageUrl(ip)} alt="" className="w-full h-full object-cover" />
                        </div>
                        <div className="flex flex-col">
                           <span className="text-sm font-tactical font-black text-slate-800 group-hover:text-purple-600">{ip.name}</span>
                           <span className="text-[10px] text-slate-400 font-mono font-bold uppercase">{ip.name_en}</span>
                        </div>
                      </div>
                    </td>
                    <td className="px-8 py-5">
                      <span className="px-3 py-1 bg-slate-100 text-[8px] font-tactical font-black text-slate-500 uppercase tracking-widest border border-slate-200">
                        {ip.category || '-'}
                      </span>
                    </td>
                    <td className="px-8 py-5 text-right font-mono font-black text-lime-500">
                      {s ? `+${s.inflow}` : '-'}
                    </td>
                    <td className="px-8 py-5 text-right font-mono font-black text-purple-600">
                      {s ? `-${s.outflow}` : '-'}
                    </td>
                    <td className="px-8 py-5 text-right">
                       <span className="px-3 py-1 bg-slate-900 text-lime-400 font-mono font-black text-xs border border-lime-400/30">
                          {s ? s.score.toFixed(2) : '-'}
                       </span>
                    </td>
                    <td className="px-8 py-5 text-right">
                      <div className="w-8 h-8 flex items-center justify-center border border-slate-100 text-slate-300 group-hover:border-purple-600 group-hover:text-purple-600 transition-all">
                        <ChevronRight size={16} />
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5 gap-6">
          {filteredIPs.map(ip => {
            const s = statsMap[ip.id];
            return (
              <div
                key={ip.id}
                onClick={() => navigate(`/ip/${ip.id}`)}
                className="group bg-white border border-slate-100 hover:border-purple-600 shadow-md hover:shadow-2xl hover:-translate-y-1 transition-all cursor-pointer flex flex-col"
              >
                <div className="aspect-square relative overflow-hidden p-2">
                  <img src={getImageUrl(ip)} alt={ip.name} className="w-full h-full object-cover" />
                  <div className="absolute top-4 left-4">
                    <span className="bg-slate-900/80 backdrop-blur px-3 py-1 text-[8px] font-tactical font-black text-white uppercase tracking-widest border border-slate-700">
                      {ip.category || 'N/A'}
                    </span>
                  </div>
                </div>
                <div className="p-5 flex-1 flex flex-col justify-between">
                  <div className="mb-4">
                    <h3 className="font-tactical font-black text-md text-slate-900 group-hover:text-purple-600 transition-colors leading-tight uppercase tracking-tighter">{ip.name}</h3>
                    <p className="text-[9px] text-slate-400 font-mono font-bold uppercase mt-1">{ip.name_en}</p>
                  </div>
                  <div className="flex items-center justify-between pt-4 border-t border-slate-50">
                    <div className="flex flex-col">
                      <span className="text-[8px] font-tactical font-black text-slate-300 uppercase tracking-widest">Sync Index</span>
                      <span className="text-xl font-mono font-black text-purple-600 leading-none">
                        {s ? s.score.toFixed(1) : '-'}
                      </span>
                    </div>
                    <div className="w-10 h-10 flex items-center justify-center border-2 border-slate-100 text-slate-300 group-hover:border-purple-600 group-hover:text-purple-600 transition-all">
                      <TrendingUp size={20} />
                    </div>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {filteredIPs.length === 0 && (
        <div className="py-20 text-center border-2 border-dashed border-slate-100">
           <p className="text-xs font-tactical font-black text-slate-400 uppercase tracking-[0.5em]">Sector Search Negative // No Signals Detected</p>
        </div>
      )}
    </div>
  );
};

export default IPMarket;
