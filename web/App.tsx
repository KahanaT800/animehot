
import React, { useState, useEffect } from 'react';
import { Routes, Route, useNavigate, useLocation, useSearchParams } from 'react-router-dom';
import {
  Activity,
  Users,
  Radar,
  Settings,
  Search,
  Menu,
  ShieldAlert,
  Flame,
  Home,
  X
} from 'lucide-react';
import Dashboard from './pages/Dashboard';
import IPMarket from './pages/IPMarket';
import IPDetail from './pages/IPDetail';
import CharacterWiki from './pages/CharacterWiki';
import CharacterDetail from './pages/CharacterDetail';
import Analytics from './pages/Analytics';
import { TimeRange } from './types';

const App: React.FC = () => {
  const [isSidebarOpen, setIsSidebarOpen] = useState(true);
  const [searchParams, setSearchParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();

  // 从 URL 读取 timeRange，默认 24
  const getTimeRangeFromURL = (): TimeRange => {
    const hours = parseInt(searchParams.get('hours') || '24', 10);
    if (hours === 2 || hours === 24 || hours === 168) {
      return hours;
    }
    return 24;
  };

  const [timeRange, setTimeRangeState] = useState<TimeRange>(getTimeRangeFromURL);

  // 设置 timeRange 并更新 URL
  const setTimeRange = (hours: TimeRange) => {
    setTimeRangeState(hours);
    const newParams = new URLSearchParams(searchParams);
    newParams.set('hours', String(hours));
    setSearchParams(newParams, { replace: true });
  };

  // 监听 URL 变化（浏览器前进/后退）
  useEffect(() => {
    const urlTimeRange = getTimeRangeFromURL();
    if (urlTimeRange !== timeRange) {
      setTimeRangeState(urlTimeRange);
    }
  }, [searchParams]);

  const navItems = [
    { label: 'HOME', icon: <Home size={20} />, path: '/' },
    { label: 'MARKET', icon: <Radar size={20} />, path: '/market' },
    { label: 'WIKI', icon: <Users size={20} />, path: '/characters' },
    { label: 'DATA', icon: <Activity size={20} />, path: '/analytics' },
  ];

  return (
    <div className="flex flex-col lg:flex-row h-screen bg-slate-50 overflow-hidden font-sans relative">
      <div className="absolute inset-0 bg-tactical pointer-events-none opacity-40" />
      
      {/* Desktop Sidebar (Hidden on Mobile) */}
      <aside 
        className={`hidden lg:flex ${
          isSidebarOpen ? 'w-64' : 'w-20'
        } transition-all duration-300 ease-in-out bg-white border-r-4 border-purple-600/20 flex-col z-50 overflow-hidden shadow-2xl`}
      >
        <div className="p-6 flex items-center gap-4 bg-purple-600 text-white">
          <div className="w-10 h-10 rounded bg-white flex items-center justify-center text-purple-600">
            <Flame size={24} strokeWidth={2.5} />
          </div>
          {isSidebarOpen && (
            <div className="flex flex-col">
              <span className="font-tactical font-black text-lg tracking-tighter leading-none">AnimeHot</span>
            </div>
          )}
        </div>

        <nav className="flex-1 px-4 py-8 space-y-4">
          {navItems.map((item) => (
            <button
              key={item.path}
              onClick={() => navigate(item.path)}
              className={`w-full flex items-center gap-4 px-4 py-3.5 border-l-4 transition-all ${
                location.pathname === item.path 
                ? 'bg-purple-50 border-purple-600 text-purple-600 font-bold' 
                : 'text-slate-400 border-transparent hover:bg-slate-50 hover:text-slate-900'
              }`}
            >
              <span className="flex-shrink-0">{item.icon}</span>
              {isSidebarOpen && <span className="font-tactical text-[10px] tracking-widest uppercase">{item.label}</span>}
            </button>
          ))}
        </nav>

        <div className="p-4 m-4 bg-purple-900 text-white rounded-lg flex flex-col gap-2 relative overflow-hidden group">
           <div className="absolute -right-4 -top-4 w-12 h-12 bg-lime-400 rotate-45 group-hover:scale-150 transition-transform" />
           <div className="flex items-center gap-3 relative z-10">
              <div className="w-8 h-8 rounded-full border-2 border-lime-400 p-0.5">
                <img src="https://api.dicebear.com/7.x/bottts/svg?seed=Shinji" className="rounded-full" alt="avatar" />
              </div>
              {isSidebarOpen && (
                <div className="text-left">
                  <p className="text-[10px] font-black uppercase text-lime-400">Pilot Status</p>
                  <p className="text-xs font-bold font-mono">USER_01_SYNC</p>
                </div>
              )}
           </div>
        </div>
      </aside>

      {/* Main Content Area */}
      <main className="flex-1 flex flex-col relative overflow-hidden">
        {/* Responsive Header */}
        <header className="h-16 lg:h-20 flex items-center justify-between px-4 lg:px-8 bg-white/90 backdrop-blur-md z-40 border-b border-slate-100 shrink-0">
          <div className="flex items-center gap-4 lg:gap-6">
            <button 
              onClick={() => setIsSidebarOpen(!isSidebarOpen)}
              className="hidden lg:flex w-10 h-10 items-center justify-center rounded border-2 border-purple-100 text-purple-600 hover:bg-purple-600 hover:text-white transition-all shadow-sm"
            >
              <Menu size={20} />
            </button>
            <div className="lg:hidden w-8 h-8 rounded bg-purple-600 flex items-center justify-center text-white">
               <Flame size={18} />
            </div>
            <div className="relative group hidden sm:block">
              <Search className="absolute left-4 top-1/2 -translate-y-1/2 text-slate-400" size={16} />
              <input 
                type="text" 
                placeholder="SEARCH..." 
                className="bg-slate-100 border-2 border-transparent px-10 py-1.5 text-[10px] font-tactical w-48 focus:bg-white focus:border-purple-600 transition-all focus:w-64 outline-none"
              />
            </div>
          </div>

          <div className="flex items-center gap-2 lg:gap-6">
            {/* IP 详情页显示不同内容 */}
            {location.pathname.startsWith('/ip/') ? (
              <div className="flex items-center gap-2 bg-slate-900 px-3 lg:px-4 py-1.5 rounded border border-slate-700">
                <div className="w-2 h-2 bg-lime-400 rounded-full animate-pulse" />
                <span className="text-[9px] lg:text-[10px] font-tactical font-bold text-white uppercase tracking-wider">
                  Live Data
                </span>
              </div>
            ) : (
              <div className="flex items-center gap-0.5 bg-slate-100 p-1 rounded border border-slate-200">
                {[2, 24, 168].map((h) => (
                  <button
                    key={h}
                    onClick={() => setTimeRange(h as TimeRange)}
                    className={`px-2 lg:px-4 py-1 rounded text-[9px] lg:text-[10px] font-tactical font-bold transition-all ${
                      timeRange === h
                      ? 'bg-purple-600 text-white'
                      : 'text-slate-400'
                    }`}
                  >
                    {h === 168 ? '7D' : `${h}H`}
                  </button>
                ))}
              </div>
            )}
            <div className="flex items-center gap-2">
               <div className="w-8 h-8 lg:w-10 lg:h-10 flex items-center justify-center bg-lime-400 text-purple-900 rounded font-black text-[10px] shadow-sm">
                 400%
               </div>
               <button className="w-8 h-8 lg:w-10 lg:h-10 flex items-center justify-center bg-white border-2 border-orange-500 rounded text-orange-500 shadow-sm">
                 <ShieldAlert size={18} />
               </button>
            </div>
          </div>
        </header>

        {/* Scrollable Viewport */}
        <div className="flex-1 overflow-y-auto p-4 lg:p-8 pt-6 custom-scrollbar pb-24 lg:pb-8">
          <Routes>
            <Route path="/" element={<Dashboard timeRange={timeRange} />} />
            <Route path="/market" element={<IPMarket timeRange={timeRange} />} />
            <Route path="/ip/:id" element={<IPDetail />} />
            <Route path="/characters" element={<CharacterWiki />} />
            <Route path="/character/:id" element={<CharacterDetail />} />
            <Route path="/analytics" element={<Analytics />} />
            <Route path="*" element={<Dashboard timeRange={timeRange} />} />
          </Routes>
        </div>

        {/* Mobile Tactical Bottom Nav */}
        <nav className="lg:hidden fixed bottom-0 left-0 right-0 bg-white border-t-4 border-purple-600 flex items-center justify-around h-16 px-4 z-[100] shadow-[0_-10px_40px_rgba(0,0,0,0.1)]">
           {navItems.map((item) => (
             <button
                key={item.path}
                onClick={() => navigate(item.path)}
                className={`flex flex-col items-center justify-center w-1/4 h-full transition-all ${
                  location.pathname === item.path 
                  ? 'text-purple-600 bg-purple-50/50' 
                  : 'text-slate-400'
                }`}
             >
                {item.icon}
                <span className="text-[8px] font-tactical font-black mt-1 uppercase tracking-widest">{item.label}</span>
                {location.pathname === item.path && (
                  <div className="absolute top-0 w-8 h-1 bg-purple-600 rounded-full" />
                )}
             </button>
           ))}
        </nav>
      </main>
    </div>
  );
};

export default App;
