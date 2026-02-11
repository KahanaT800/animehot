
import React from 'react';
import { useNavigate } from 'react-router-dom';
import { Construction, ArrowLeft, Sparkles } from 'lucide-react';

interface ComingSoonProps {
  title?: string;
  description?: string;
}

const ComingSoon: React.FC<ComingSoonProps> = ({
  title = 'Character Wiki',
  description = 'This feature is currently under development'
}) => {
  const navigate = useNavigate();

  return (
    <div className="min-h-[60vh] flex items-center justify-center animate-in fade-in duration-700">
      <div className="text-center max-w-md mx-auto px-6">
        <div className="relative inline-block mb-8">
          <div className="w-24 h-24 bg-purple-100 rounded-full flex items-center justify-center mx-auto">
            <Construction size={48} className="text-purple-600" />
          </div>
          <div className="absolute -top-2 -right-2 w-8 h-8 bg-lime-400 rounded-full flex items-center justify-center animate-bounce">
            <Sparkles size={16} className="text-purple-900" />
          </div>
        </div>

        <h1 className="text-3xl font-tactical font-black text-slate-900 uppercase tracking-tighter mb-4">
          {title}
        </h1>

        <div className="bg-purple-600 text-white px-4 py-2 inline-block mb-6">
          <span className="text-[10px] font-tactical font-black uppercase tracking-widest">
            Coming Soon
          </span>
        </div>

        <p className="text-sm text-slate-500 mb-8 font-mono">
          {description}
        </p>

        <div className="space-y-4">
          <button
            onClick={() => navigate('/')}
            className="w-full flex items-center justify-center gap-3 bg-slate-900 text-white px-8 py-4 font-tactical font-black text-sm uppercase tracking-widest hover:bg-purple-600 transition-all"
          >
            <ArrowLeft size={18} />
            Back to Dashboard
          </button>

          <p className="text-[10px] text-slate-400 font-mono uppercase">
            Stay tuned for updates
          </p>
        </div>
      </div>
    </div>
  );
};

export default ComingSoon;
