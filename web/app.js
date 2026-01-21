/**
 * Anime Hot - IP Liquidity Terminal
 * app.js - Main Application Logic
 */

// ============================================
// CONFIG
// ============================================
const API_BASE = window.location.origin;

// ============================================
// STATE
// ============================================
let ips = [];
let ipsWithData = [];
let leaderboardOutflow = [];
let leaderboardInflow = [];
let leaderboardHot = [];
let ipStats24h = {};  // 聚合数据 (用于 ALL IPs 表格和泡泡图)
let currentTimeRange = 24;  // 当前时间范围 (小时): 2, 24, 168
let selectedIP = null;
let bubbleChart = null;
let gapChart = null;
let priceChart = null;

// IP Table state
let ipTableSort = { field: 'hot', asc: false };
let ipTableFilter = '';

// ============================================
// INITIALIZATION
// ============================================
document.addEventListener('DOMContentLoaded', () => {
    // 从 URL 读取时间范围和 Tab
    initFromURL();

    checkHealth();
    loadOverviewData();
    setInterval(checkHealth, 30000);
    setInterval(loadOverviewData, 60000);

    // Handle browser back/forward and initial route
    window.addEventListener('hashchange', handleRoute);
    window.addEventListener('popstate', handlePopState);
    handleRoute();

    // Time range switcher
    document.querySelectorAll('.time-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            const hours = parseInt(btn.dataset.hours, 10);
            if (hours === currentTimeRange) return;

            // Update URL and state
            setTimeRange(hours);
        });
    });
});

// 从 URL 初始化时间范围和 Tab
function initFromURL() {
    const params = new URLSearchParams(window.location.search);

    // 初始化时间范围
    const hours = parseInt(params.get('hours'), 10);
    if ([2, 24, 168].includes(hours)) {
        currentTimeRange = hours;
    }

    // 更新时间按钮状态
    document.querySelectorAll('.time-btn').forEach(btn => {
        const btnHours = parseInt(btn.dataset.hours, 10);
        btn.classList.toggle('active', btnHours === currentTimeRange);
    });

    // 更新标签
    updatePeriodLabels();

    // 初始化 Tab
    const tab = params.get('tab');
    if (tab === 'iplist') {
        switchTab('iplist', false);
    }
}

// 设置时间范围并更新 URL
function setTimeRange(hours) {
    currentTimeRange = hours;

    // 更新 URL (保留 hash)
    const url = new URL(window.location);
    url.searchParams.set('hours', hours);
    window.history.pushState({}, '', url);

    // 更新按钮状态
    document.querySelectorAll('.time-btn').forEach(btn => {
        const btnHours = parseInt(btn.dataset.hours, 10);
        btn.classList.toggle('active', btnHours === hours);
    });

    // 重新加载数据
    loadLeaderboardData(hours);
}

// 更新时间范围标签
function updatePeriodLabels() {
    const periodLabel = currentTimeRange === 2 ? '2H' : currentTimeRange === 24 ? '24H' : '7D';
    const outflowEl = document.getElementById('outflowPeriod');
    const inflowEl = document.getElementById('inflowPeriod');
    const hotEl = document.getElementById('hotPeriod');
    if (outflowEl) outflowEl.textContent = periodLabel;
    if (inflowEl) inflowEl.textContent = periodLabel;
    if (hotEl) hotEl.textContent = periodLabel;
}

// 处理浏览器前进/后退 (时间范围和 Tab 变化)
function handlePopState() {
    const params = new URLSearchParams(window.location.search);

    // 处理时间范围变化
    const hours = parseInt(params.get('hours'), 10);
    const newRange = [2, 24, 168].includes(hours) ? hours : 24;

    if (newRange !== currentTimeRange) {
        currentTimeRange = newRange;

        // 更新按钮状态
        document.querySelectorAll('.time-btn').forEach(btn => {
            const btnHours = parseInt(btn.dataset.hours, 10);
            btn.classList.toggle('active', btnHours === newRange);
        });

        // 重新加载数据
        loadLeaderboardData(newRange);
    }

    // 处理 Tab 变化
    const tab = params.get('tab') || 'dashboard';
    const currentTab = document.querySelector('.tab-btn.active')?.dataset.tab;
    if (tab !== currentTab) {
        switchTab(tab, false);
    }
}

// ============================================
// ROUTING
// ============================================
function handleRoute() {
    const hash = window.location.hash;
    const match = hash.match(/^#\/ip\/(\d+)$/);

    if (match) {
        const ipId = parseInt(match[1], 10);
        // Wait for data to load if not ready
        if (ipsWithData.length === 0) {
            setTimeout(() => handleRoute(), 100);
            return;
        }
        selectIPDirect(ipId);
    } else {
        showPage('overview');
        selectedIP = null;
    }
}

// ============================================
// API FUNCTIONS
// ============================================
async function checkHealth() {
    try {
        const res = await fetch(`${API_BASE}/health`);
        if (res.ok) {
            document.getElementById('statusDot').classList.remove('offline');
            document.getElementById('statusText').textContent = 'ONLINE';
        } else {
            throw new Error('Health check failed');
        }
    } catch (e) {
        document.getElementById('statusDot').classList.add('offline');
        document.getElementById('statusText').textContent = 'OFFLINE';
    }
    document.getElementById('lastUpdate').textContent = new Date().toLocaleTimeString('ja-JP', { hour: '2-digit', minute: '2-digit' });
}

// 加载排行榜数据 (支持切换时间范围)
async function loadLeaderboardData(hours) {
    try {
        const [lbOutflowRes, lbInflowRes, lbHotRes, allStatsRes] = await Promise.all([
            fetch(`${API_BASE}/api/v1/leaderboard?type=outflow&hours=${hours}&limit=10`),
            fetch(`${API_BASE}/api/v1/leaderboard?type=inflow&hours=${hours}&limit=10`),
            fetch(`${API_BASE}/api/v1/leaderboard?type=hot&hours=${hours}&limit=10`),
            fetch(`${API_BASE}/api/v1/leaderboard?type=hot&hours=${hours}&limit=100`)
        ]);

        const lbOutflowData = await lbOutflowRes.json();
        const lbInflowData = await lbInflowRes.json();
        const lbHotData = await lbHotRes.json();
        const allStatsData = await allStatsRes.json();

        // Store leaderboard data
        const mapLeaderboard = (data) => (data.data?.items || []).map(item => ({
            id: item.ip_id,
            name: item.ip_name,
            name_en: item.ip_name_en,
            score: item.score,
            rank: item.rank,
            inflow: item.inflow,
            outflow: item.outflow
        }));

        leaderboardOutflow = mapLeaderboard(lbOutflowData);
        leaderboardInflow = mapLeaderboard(lbInflowData);
        leaderboardHot = mapLeaderboard(lbHotData);

        // 存储所有 IP 的聚合数据
        ipStats24h = {};
        (allStatsData.data?.items || []).forEach(item => {
            ipStats24h[item.ip_id] = {
                inflow: item.inflow,
                outflow: item.outflow,
                score: item.score
            };
        });

        // 更新时间范围标签
        updatePeriodLabels();

        // 更新 UI
        updateMetricCards();
        renderLeaderboards();
        renderBubbleChart();

        // 如果 IP 列表 tab 激活，也更新它
        const iplistTab = document.getElementById('tab-iplist');
        if (iplistTab && iplistTab.classList.contains('active')) {
            renderIPTable();
        }
    } catch (e) {
        console.error('Failed to load leaderboard data:', e);
    }
}

async function loadOverviewData() {
    try {
        // Load IPs and all three leaderboards in parallel
        const hours = currentTimeRange;
        const [ipsRes, lbOutflowRes, lbInflowRes, lbHotRes, allStatsRes] = await Promise.all([
            fetch(`${API_BASE}/api/v1/ips?page_size=100`),
            fetch(`${API_BASE}/api/v1/leaderboard?type=outflow&hours=${hours}&limit=10`),
            fetch(`${API_BASE}/api/v1/leaderboard?type=inflow&hours=${hours}&limit=10`),
            fetch(`${API_BASE}/api/v1/leaderboard?type=hot&hours=${hours}&limit=10`),
            fetch(`${API_BASE}/api/v1/leaderboard?type=hot&hours=${hours}&limit=100`)
        ]);

        const ipsData = await ipsRes.json();
        const lbOutflowData = await lbOutflowRes.json();
        const lbInflowData = await lbInflowRes.json();
        const lbHotData = await lbHotRes.json();
        const allStatsData = await allStatsRes.json();

        if (ipsData.code !== 0) throw new Error(ipsData.message);
        ips = ipsData.data || [];

        // Store leaderboard data
        const mapLeaderboard = (data) => (data.data?.items || []).map(item => ({
            id: item.ip_id,
            name: item.ip_name,
            name_en: item.ip_name_en,
            score: item.score,
            rank: item.rank,
            inflow: item.inflow,
            outflow: item.outflow
        }));

        leaderboardOutflow = mapLeaderboard(lbOutflowData);
        leaderboardInflow = mapLeaderboard(lbInflowData);
        leaderboardHot = mapLeaderboard(lbHotData);

        // 存储所有 IP 的聚合数据 (用于 ALL IPs 表格和泡泡图)
        ipStats24h = {};
        (allStatsData.data?.items || []).forEach(item => {
            ipStats24h[item.ip_id] = {
                inflow: item.inflow,
                outflow: item.outflow,
                score: item.score
            };
        });

        // Load liquidity for each IP (for bubble chart and metrics)
        ipsWithData = await Promise.all(
            ips.map(async (ip) => {
                try {
                    const liqRes = await fetch(`${API_BASE}/api/v1/ips/${ip.id}/liquidity`);
                    const liqData = await liqRes.json();
                    return { ...ip, liquidity: liqData.data || {} };
                } catch {
                    return { ...ip, liquidity: {} };
                }
            })
        );

        // 更新时间范围标签 (确保与 currentTimeRange 同步)
        updatePeriodLabels();

        updateMetricCards();
        renderLeaderboards();
        renderBubbleChart();

        // Update IP count badge
        const badge = document.getElementById('ipCountBadge');
        if (badge) badge.textContent = ipsWithData.length;

        // If IP list tab is active, render the table
        const iplistTab = document.getElementById('tab-iplist');
        if (iplistTab && iplistTab.classList.contains('active')) {
            renderIPTable();
        }

    } catch (e) {
        console.error('Failed to load overview data:', e);
        ['outflow', 'inflow', 'hot'].forEach(id => {
            const el = document.getElementById(`leaderboard-${id}`);
            if (el) el.innerHTML = `
                <div class="empty-state">
                    <div class="empty-icon">⚠️</div>
                    <p class="empty-text">FAILED TO LOAD</p>
                </div>
            `;
        });
    }
}

// ============================================
// OVERVIEW PAGE
// ============================================
// 计算 HOT 分数: (outflow+1)/(inflow+1) × log(outflow+1)
function calculateHotScore(inflow, outflow) {
    const ratio = (outflow + 1) / (inflow + 1);
    const scale = Math.log(outflow + 1);
    return ratio * scale;
}

function updateMetricCards() {
    // 使用聚合数据 (ipStats24h) 计算 INFLOW/OUTFLOW 总量
    let totalInflow = 0, totalOutflow = 0;
    Object.values(ipStats24h).forEach(stats => {
        totalInflow += stats.inflow || 0;
        totalOutflow += stats.outflow || 0;
    });

    document.getElementById('totalInflow').textContent = totalInflow;
    document.getElementById('totalOutflow').textContent = totalOutflow;

    // HOT 中位数
    const hotScores = Object.values(ipStats24h)
        .map(s => s.score)
        .filter(s => s > 0);

    let medianHot = 0;
    if (hotScores.length > 0) {
        hotScores.sort((a, b) => a - b);
        const mid = Math.floor(hotScores.length / 2);
        medianHot = hotScores.length % 2 !== 0
            ? hotScores[mid]
            : (hotScores[mid - 1] + hotScores[mid]) / 2;
    }
    document.getElementById('medianHot').textContent = medianHot.toFixed(2);
}

function renderLeaderboards() {
    renderSingleLeaderboard('outflow', leaderboardOutflow, 'outflow');
    renderSingleLeaderboard('inflow', leaderboardInflow, 'inflow');
    renderSingleLeaderboard('hot', leaderboardHot, 'hot');
}

function renderSingleLeaderboard(type, data, containerId) {
    const container = document.getElementById(`leaderboard-${containerId}`);
    if (!container) return;

    if (data.length === 0) {
        container.innerHTML = `<div class="empty-state"><p class="empty-text">NO DATA</p></div>`;
        return;
    }

    const items = data.map((lb, idx) => {
        const ip = ipsWithData.find(i => i.id === lb.id) || {};
        const score = lb.score || 0;
        const name = ip.name || lb.name || '--';

        let scoreDisplay;
        switch (type) {
            case 'inflow':
                scoreDisplay = `+${Math.round(score)}`;
                break;
            case 'hot':
                scoreDisplay = score.toFixed(2);
                break;
            default:
                scoreDisplay = Math.round(score);
        }

        return `
            <div class="lb-item" onclick="selectIP(${lb.id})">
                <span class="lb-rank ${idx < 3 ? 'top' : ''}">${idx + 1}</span>
                <span class="lb-name">${escapeHtml(name)}</span>
                <span class="lb-score ${type}">${scoreDisplay}</span>
            </div>
        `;
    });

    container.innerHTML = items.join('');
}

// Force-directed bubble chart state
let forceSimulation = null;
let bubbleNodes = [];
let bubblePositionCache = {};  // Cache positions: { ipId: { x, y } }

function renderBubbleChart() {
    const container = document.getElementById('bubbleChartContainer');
    const svg = d3.select('#bubbleChart');

    // Clear previous content
    svg.selectAll('*').remove();
    if (forceSimulation) {
        forceSimulation.stop();
        forceSimulation = null;
    }

    // Get dimensions
    const rect = container.getBoundingClientRect();
    const width = rect.width - 40;  // padding
    const height = rect.height - 40;
    const margin = { top: 30, right: 30, bottom: 30, left: 30 };
    const innerWidth = width - margin.left - margin.right;
    const innerHeight = height - margin.top - margin.bottom;

    svg.attr('width', width).attr('height', height);

    // Create main group with margin
    const g = svg.append('g')
        .attr('transform', `translate(${margin.left}, ${margin.top})`);

    // Calculate median HOT score for dynamic sizing
    const allScores = Object.values(ipStats24h)
        .map(s => s.score)
        .filter(s => s > 0)
        .sort((a, b) => a - b);

    let medianScore = 0.1;  // fallback
    if (allScores.length > 0) {
        const mid = Math.floor(allScores.length / 2);
        medianScore = allScores.length % 2 !== 0
            ? allScores[mid]
            : (allScores[mid - 1] + allScores[mid]) / 2;
    }

    // Prepare data with dynamic radius based on HOT score
    // radius = baseRadius * (1 + k * log2(score / median))
    // - score = median → log2(1) = 0 → radius = baseRadius
    // - score > median → positive → larger
    // - score < median → negative → smaller
    const baseRadius = 55;
    const minRadius = 22;
    const maxRadius = 95;
    const scaleFactor = 0.5;  // controls size variance

    const data = ipsWithData.map(ip => {
        const stats = ipStats24h[ip.id] || { inflow: 0, outflow: 0, score: 0 };
        const score = stats.score || 0.001;

        // Dynamic radius: log scale relative to median
        const ratio = score / medianScore;
        const logScale = Math.log2(Math.max(ratio, 0.01));  // avoid log of 0
        const radius = Math.min(maxRadius, Math.max(minRadius, baseRadius * (1 + scaleFactor * logScale)));

        return {
            id: ip.id,
            label: ip.name,
            inflow: stats.inflow,
            outflow: stats.outflow,
            score: stats.score,
            radius: radius,
            imageUrl: ip.image_url
        };
    });

    if (data.length === 0) return;

    // Create scales
    const maxInflow = d3.max(data, d => d.inflow) || 100;
    const maxOutflow = d3.max(data, d => d.outflow) || 100;

    const xScale = d3.scaleLinear()
        .domain([0, maxInflow * 1.1])
        .range([0, innerWidth]);

    const yScale = d3.scaleLinear()
        .domain([0, maxOutflow * 1.1])
        .range([innerHeight, 0]);  // Inverted for SVG coordinates

    // Set target positions based on data
    data.forEach(d => {
        d.targetX = xScale(d.inflow);
        d.targetY = yScale(d.outflow);

        // Use cached position if available, otherwise initialize near target
        const cached = bubblePositionCache[d.id];
        if (cached) {
            d.x = cached.x;
            d.y = cached.y;
        } else {
            d.x = d.targetX + (Math.random() - 0.5) * 20;
            d.y = d.targetY + (Math.random() - 0.5) * 20;
        }
    });

    bubbleNodes = data;

    // Save positions to cache when simulation settles
    const savePositions = () => {
        data.forEach(d => {
            bubblePositionCache[d.id] = { x: d.x, y: d.y };
        });
    };

    // Color function
    const getColor = (d, opacity = 0.5) => {
        if (d.imageUrl) return `rgba(30, 41, 59, ${opacity})`;
        if (d.outflow > d.inflow) return `rgba(244, 63, 94, ${opacity})`;  // red - more outflow
        if (d.inflow > d.outflow) return `rgba(16, 185, 129, ${opacity})`;  // green - more inflow
        return `rgba(245, 158, 11, ${opacity})`;  // amber - balanced
    };

    const getBorderColor = (d) => {
        if (d.imageUrl) return '#475569';
        if (d.outflow > d.inflow) return '#f43f5e';
        if (d.inflow > d.outflow) return '#10b981';
        return '#f59e0b';
    };

    // Create defs for patterns (images)
    const defs = svg.append('defs');

    data.forEach(d => {
        if (d.imageUrl) {
            defs.append('pattern')
                .attr('id', `img-${d.id}`)
                .attr('width', 1)
                .attr('height', 1)
                .append('image')
                .attr('href', d.imageUrl)
                .attr('width', d.radius * 2)
                .attr('height', d.radius * 2)
                .attr('preserveAspectRatio', 'xMidYMid slice');
        }
    });

    // Create bubble groups
    const bubbleGroups = g.selectAll('.bubble-group')
        .data(data)
        .join('g')
        .attr('class', 'bubble-group')
        .style('cursor', 'pointer');

    // Add circles
    const circles = bubbleGroups.append('circle')
        .attr('class', 'bubble-node')
        .attr('r', d => d.radius)
        .attr('fill', d => d.imageUrl ? `url(#img-${d.id})` : getColor(d, 0.4))
        .attr('stroke', d => getBorderColor(d))
        .attr('stroke-width', 2);

    // Add labels (initially hidden, shown on hover)
    const labels = bubbleGroups.append('text')
        .attr('class', 'bubble-label')
        .attr('dy', d => d.radius + 14)
        .text(d => d.label)
        .style('opacity', 0);

    // Tooltip
    const tooltip = d3.select('body').append('div')
        .attr('class', 'bubble-tooltip')
        .style('position', 'absolute')
        .style('background', '#111827')
        .style('border', '1px solid #1e293b')
        .style('border-radius', '8px')
        .style('padding', '10px 14px')
        .style('font-family', "'JetBrains Mono', monospace")
        .style('font-size', '11px')
        .style('color', '#e2e8f0')
        .style('pointer-events', 'none')
        .style('opacity', 0)
        .style('z-index', 1000)
        .style('box-shadow', '0 4px 12px rgba(0,0,0,0.4)');

    // Force simulation with "柔性定位"
    forceSimulation = d3.forceSimulation(data)
        .force('x', d3.forceX(d => d.targetX).strength(0.15))
        .force('y', d3.forceY(d => d.targetY).strength(0.15))
        .force('collide', d3.forceCollide(d => d.radius + 2).strength(0.8))
        .force('center', null)
        .alphaDecay(0.02)
        .velocityDecay(0.3)
        .on('tick', () => {
            bubbleGroups.attr('transform', d => {
                // Constrain to bounds
                d.x = Math.max(d.radius, Math.min(innerWidth - d.radius, d.x));
                d.y = Math.max(d.radius, Math.min(innerHeight - d.radius, d.y));
                return `translate(${d.x}, ${d.y})`;
            });
        })
        .on('end', savePositions);  // Cache positions when simulation settles

    // Interaction handlers
    let hoveredNode = null;

    bubbleGroups
        .on('mouseenter', function(event, d) {
            hoveredNode = d;

            // Show label
            d3.select(this).select('.bubble-label').style('opacity', 1);

            // Highlight this bubble
            d3.select(this).select('.bubble-node')
                .attr('stroke-width', 3)
                .attr('fill', d.imageUrl ? `url(#img-${d.id})` : getColor(d, 0.6));

            // Increase collision strength to push others away
            forceSimulation
                .force('collide', d3.forceCollide(node => {
                    if (node === d) return node.radius + 15;  // Hovered node has larger collision radius
                    return node.radius + 2;
                }).strength(0.9).iterations(3))
                .alpha(0.3)
                .restart();

            // Show tooltip
            tooltip
                .html(`<div style="font-family: 'Noto Sans JP', sans-serif; font-size: 12px; margin-bottom: 6px;">${d.label}</div>
                       <div>Inflow: <span style="color: #10b981;">${d.inflow}</span></div>
                       <div>Outflow: <span style="color: #f43f5e;">${d.outflow}</span></div>
                       <div>HOT: <span style="color: #a855f7;">${(d.score || 0).toFixed(2)}</span></div>
                       <div style="margin-top: 6px; color: #64748b; font-size: 10px;">Click to view details</div>`)
                .style('opacity', 1)
                .style('left', (event.pageX + 15) + 'px')
                .style('top', (event.pageY - 10) + 'px');
        })
        .on('mousemove', function(event) {
            tooltip
                .style('left', (event.pageX + 15) + 'px')
                .style('top', (event.pageY - 10) + 'px');
        })
        .on('mouseleave', function(event, d) {
            hoveredNode = null;

            // Hide label
            d3.select(this).select('.bubble-label').style('opacity', 0);

            // Reset bubble style
            d3.select(this).select('.bubble-node')
                .attr('stroke-width', 2)
                .attr('fill', d.imageUrl ? `url(#img-${d.id})` : getColor(d, 0.4));

            // Reset collision force
            forceSimulation
                .force('collide', d3.forceCollide(d => d.radius + 2).strength(0.8))
                .alpha(0.2)
                .restart();

            // Hide tooltip
            tooltip.style('opacity', 0);
        })
        .on('click', function(event, d) {
            event.stopPropagation();
            tooltip.style('opacity', 0);
            selectIP(d.id);
        });

    // Drag behavior
    const drag = d3.drag()
        .on('start', function(event, d) {
            if (!event.active) forceSimulation.alphaTarget(0.3).restart();
            d.fx = d.x;
            d.fy = d.y;
            d3.select(this).select('.bubble-node').classed('dragging', true);

            // Strong collision during drag
            forceSimulation.force('collide', d3.forceCollide(node => {
                if (node === d) return node.radius + 25;
                return node.radius + 2;
            }).strength(1).iterations(4));
        })
        .on('drag', function(event, d) {
            d.fx = Math.max(d.radius, Math.min(innerWidth - d.radius, event.x));
            d.fy = Math.max(d.radius, Math.min(innerHeight - d.radius, event.y));
        })
        .on('end', function(event, d) {
            if (!event.active) forceSimulation.alphaTarget(0);
            // Release fixed position - let it drift back toward target
            d.fx = null;
            d.fy = null;
            d3.select(this).select('.bubble-node').classed('dragging', false);

            // Reset collision
            forceSimulation.force('collide', d3.forceCollide(d => d.radius + 2).strength(0.8));
        });

    bubbleGroups.call(drag);

    // Draw subtle grid lines
    const gridGroup = g.insert('g', ':first-child').attr('class', 'grid');

    // X grid lines
    const xTicks = xScale.ticks(5);
    gridGroup.selectAll('.grid-x')
        .data(xTicks)
        .join('line')
        .attr('class', 'grid-x')
        .attr('x1', d => xScale(d))
        .attr('x2', d => xScale(d))
        .attr('y1', 0)
        .attr('y2', innerHeight)
        .attr('stroke', '#1e293b')
        .attr('stroke-width', 1)
        .attr('stroke-opacity', 0.3);

    // Y grid lines
    const yTicks = yScale.ticks(5);
    gridGroup.selectAll('.grid-y')
        .data(yTicks)
        .join('line')
        .attr('class', 'grid-y')
        .attr('x1', 0)
        .attr('x2', innerWidth)
        .attr('y1', d => yScale(d))
        .attr('y2', d => yScale(d))
        .attr('stroke', '#1e293b')
        .attr('stroke-width', 1)
        .attr('stroke-opacity', 0.3);
}

// ============================================
// DETAIL PAGE
// ============================================
async function selectIP(id) {
    // Update URL hash (triggers hashchange, but we handle it gracefully)
    window.location.hash = `#/ip/${id}`;
}

// Direct selection without updating hash (called from router)
async function selectIPDirect(id) {
    selectedIP = ipsWithData.find(ip => ip.id === id);
    if (!selectedIP) return;

    showPage('detail');
    updateDetailHeader();
    await Promise.all([
        loadHourlyStats(id),
        loadItems(id)
    ]);
}

function updateDetailHeader() {
    const ip = selectedIP;
    const liq = ip.liquidity || {};

    // Update avatar with image or fallback to first character
    const avatarEl = document.getElementById('detailAvatar');
    if (ip.image_url) {
        const img = document.createElement('img');
        img.src = ip.image_url;
        img.alt = ip.name || '';
        img.onerror = () => { avatarEl.textContent = ip.name?.charAt(0) || '?'; };
        avatarEl.innerHTML = '';
        avatarEl.appendChild(img);
    } else {
        avatarEl.innerHTML = `<span id="detailAvatarText">${escapeHtml(ip.name?.charAt(0) || '?')}</span>`;
    }
    document.getElementById('detailName').textContent = ip.name || '--';
    document.getElementById('detailSubtitle').textContent = `${ip.name_en || ''} • Weight: ${ip.weight || 1.0}`;

    const tagsContainer = document.getElementById('detailTags');
    const tags = ip.tags || [];
    tagsContainer.innerHTML = (ip.category ? [`<span class="tag">${escapeHtml(ip.category)}</span>`] : [])
        .concat(tags.map(t => `<span class="tag">${escapeHtml(t)}</span>`))
        .join('');

    // 使用 24h 聚合数据
    const stats = ipStats24h[ip.id] || { inflow: 0, outflow: 0, score: 0 };
    document.getElementById('detailScore').textContent = stats.score.toFixed(2);
}

async function loadHourlyStats(id) {
    try {
        const res = await fetch(`${API_BASE}/api/v1/ips/${id}/stats/hourly?limit=12`);
        const data = await res.json();
        if (data.code !== 0) throw new Error(data.message);

        const stats = data.data.stats || [];
        renderGapChart(stats);
        renderPriceChart(stats);
    } catch (e) {
        console.error('Failed to load hourly stats:', e);
        renderGapChart([]);
        renderPriceChart([]);
    }
}

function renderGapChart(stats) {
    const ctx = document.getElementById('gapChart').getContext('2d');
    if (gapChart) gapChart.destroy();

    // Sort by time ascending (old on left, new on right)
    const sortedStats = [...stats].sort((a, b) => new Date(a.hour_bucket) - new Date(b.hour_bucket));

    const labels = sortedStats.map(s => {
        const d = new Date(s.hour_bucket);
        return d.toLocaleTimeString('ja-JP', { hour: '2-digit', minute: '2-digit' });
    });

    gapChart = new Chart(ctx, {
        type: 'line',
        data: {
            labels,
            datasets: [
                {
                    label: 'Inflow',
                    data: sortedStats.map(s => s.inflow || 0),
                    borderColor: '#10b981',
                    backgroundColor: '#10b98120',
                    borderWidth: 2,
                    pointRadius: 4,
                    pointBackgroundColor: '#10b981',
                    pointBorderColor: '#0a0e17',
                    pointBorderWidth: 2,
                    tension: 0.3,
                    fill: true,
                    yAxisID: 'yInflow'
                },
                {
                    label: 'Outflow',
                    data: sortedStats.map(s => s.outflow || 0),
                    borderColor: '#f43f5e',
                    backgroundColor: '#f43f5e20',
                    borderWidth: 2,
                    pointRadius: 4,
                    pointBackgroundColor: '#f43f5e',
                    pointBorderColor: '#0a0e17',
                    pointBorderWidth: 2,
                    tension: 0.3,
                    fill: true,
                    yAxisID: 'yOutflow'
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: { intersect: false, mode: 'index' },
            plugins: {
                legend: { display: false },
                tooltip: {
                    backgroundColor: '#111827',
                    borderColor: '#1e293b',
                    borderWidth: 1,
                    titleFont: { family: "'JetBrains Mono', monospace", size: 11 },
                    bodyFont: { family: "'JetBrains Mono', monospace", size: 11 },
                    padding: 10
                }
            },
            scales: {
                x: {
                    grid: { color: '#1e293b40' },
                    ticks: { color: '#475569', font: { family: "'JetBrains Mono', monospace", size: 9 }, maxRotation: 0 }
                },
                yInflow: {
                    type: 'linear',
                    position: 'left',
                    beginAtZero: true,
                    grid: { color: '#1e293b40' },
                    ticks: { color: '#10b981', font: { family: "'JetBrains Mono', monospace", size: 10 } },
                    title: {
                        display: true,
                        text: 'INFLOW',
                        color: '#10b981',
                        font: { family: "'JetBrains Mono', monospace", size: 10, weight: 'bold' }
                    }
                },
                yOutflow: {
                    type: 'linear',
                    position: 'right',
                    beginAtZero: true,
                    grid: { display: false },
                    ticks: { color: '#f43f5e', font: { family: "'JetBrains Mono', monospace", size: 10 } },
                    title: {
                        display: true,
                        text: 'OUTFLOW',
                        color: '#f43f5e',
                        font: { family: "'JetBrains Mono', monospace", size: 10, weight: 'bold' }
                    }
                }
            }
        }
    });
}

function renderPriceChart(stats) {
    const ctx = document.getElementById('priceChart').getContext('2d');
    if (priceChart) priceChart.destroy();

    // Sort by time ascending (old on left, new on right)
    const sortedStats = [...stats].sort((a, b) => new Date(a.hour_bucket) - new Date(b.hour_bucket));

    const labels = sortedStats.map(s => {
        const d = new Date(s.hour_bucket);
        return d.toLocaleTimeString('ja-JP', { hour: '2-digit', minute: '2-digit' });
    });

    // Store min/max data with item info for custom drawing and tooltips
    const priceRangeData = sortedStats.map(s => ({
        min: s.min_price || 0,
        max: s.max_price || 0,
        minItem: s.min_price_item || null,
        maxItem: s.max_price_item || null
    }));

    // Custom tooltip element for min/max items
    let itemTooltip = document.getElementById('priceItemTooltip');
    if (!itemTooltip) {
        itemTooltip = document.createElement('div');
        itemTooltip.id = 'priceItemTooltip';
        itemTooltip.className = 'price-item-tooltip';
        document.body.appendChild(itemTooltip);
    }

    // Custom plugin to draw vertical price range lines
    const priceRangePlugin = {
        id: 'priceRangeLines',
        afterDatasetsDraw: (chart) => {
            const ctx = chart.ctx;
            const xAxis = chart.scales.x;
            const yAxis = chart.scales.y;

            // Store positions for hover detection
            chart.priceRangePositions = [];

            priceRangeData.forEach((range, index) => {
                if (range.min === 0 && range.max === 0) return;

                const x = xAxis.getPixelForValue(index);
                const yMin = yAxis.getPixelForValue(range.min);
                const yMax = yAxis.getPixelForValue(range.max);

                // Store positions for hover detection
                chart.priceRangePositions.push({
                    index,
                    x,
                    yMin,
                    yMax,
                    minItem: range.minItem,
                    maxItem: range.maxItem,
                    minPrice: range.min,
                    maxPrice: range.max
                });

                // Draw vertical line
                ctx.save();
                ctx.beginPath();
                ctx.moveTo(x, yMin);
                ctx.lineTo(x, yMax);
                ctx.strokeStyle = '#06b6d4';
                ctx.lineWidth = 3;
                ctx.lineCap = 'round';
                ctx.stroke();

                // Draw end caps (small horizontal lines)
                const capWidth = 6;
                ctx.beginPath();
                ctx.moveTo(x - capWidth, yMin);
                ctx.lineTo(x + capWidth, yMin);
                ctx.moveTo(x - capWidth, yMax);
                ctx.lineTo(x + capWidth, yMax);
                ctx.strokeStyle = '#06b6d4';
                ctx.lineWidth = 2;
                ctx.stroke();
                ctx.restore();
            });
        }
    };

    // Handle mouse move for custom tooltip
    const chartCanvas = document.getElementById('priceChart');
    chartCanvas.onmousemove = (e) => {
        if (!priceChart || !priceChart.priceRangePositions) return;

        const rect = chartCanvas.getBoundingClientRect();
        const mouseX = e.clientX - rect.left;
        const mouseY = e.clientY - rect.top;
        const hitRadius = 15;

        let found = null;
        let isMin = false;

        for (const pos of priceChart.priceRangePositions) {
            // Check if near min point
            if (pos.minItem && Math.abs(mouseX - pos.x) < hitRadius && Math.abs(mouseY - pos.yMin) < hitRadius) {
                found = pos.minItem;
                isMin = true;
                break;
            }
            // Check if near max point
            if (pos.maxItem && Math.abs(mouseX - pos.x) < hitRadius && Math.abs(mouseY - pos.yMax) < hitRadius) {
                found = pos.maxItem;
                isMin = false;
                break;
            }
        }

        if (found) {
            const label = isMin ? 'MIN' : 'MAX';
            const labelClass = isMin ? 'min' : 'max';
            itemTooltip.innerHTML = `
                <div class="tooltip-label ${labelClass}">${label}</div>
                <img src="${escapeHtml(found.image_url || '')}" alt="" onerror="this.style.display='none'">
                <div class="tooltip-title">${escapeHtml(found.title || 'Unknown')}</div>
                <div class="tooltip-price">¥${(found.price || 0).toLocaleString()}</div>
            `;
            itemTooltip.style.display = 'block';

            // 边界检测：防止悬浮窗超出浏览器边界
            const tooltipWidth = 250; // max-width from CSS
            const tooltipHeight = 200; // approximate height
            const viewportWidth = window.innerWidth;
            const viewportHeight = window.innerHeight;

            let left = e.clientX + 15;
            let top = e.clientY - 10;

            // 右边界检测
            if (left + tooltipWidth > viewportWidth - 10) {
                left = e.clientX - tooltipWidth - 15;
            }
            // 下边界检测
            if (top + tooltipHeight > viewportHeight - 10) {
                top = viewportHeight - tooltipHeight - 10;
            }
            // 上边界检测
            if (top < 10) {
                top = 10;
            }

            itemTooltip.style.left = left + 'px';
            itemTooltip.style.top = top + 'px';
            chartCanvas.style.cursor = found.item_url ? 'pointer' : 'default';
            chartCanvas.dataset.tooltipUrl = found.item_url || '';
        } else {
            itemTooltip.style.display = 'none';
            chartCanvas.style.cursor = 'default';
            chartCanvas.dataset.tooltipUrl = '';
        }
    };

    chartCanvas.onmouseleave = () => {
        itemTooltip.style.display = 'none';
        chartCanvas.style.cursor = 'default';
    };

    chartCanvas.onclick = (e) => {
        const url = chartCanvas.dataset.tooltipUrl;
        if (url) {
            window.open(url, '_blank');
        }
    };

    priceChart = new Chart(ctx, {
        type: 'line',
        plugins: [priceRangePlugin],
        data: {
            labels,
            datasets: [
                {
                    label: 'Median Price',
                    data: sortedStats.map(s => s.median_price || 0),
                    borderColor: '#f59e0b',
                    borderWidth: 2,
                    borderDash: [6, 4],
                    pointRadius: 4,
                    pointBackgroundColor: '#f59e0b',
                    pointBorderColor: '#0a0e17',
                    pointBorderWidth: 2,
                    tension: 0.3,
                    fill: false
                },
                {
                    // Hidden dataset to set Y axis scale including min/max
                    label: 'Max',
                    data: sortedStats.map(s => s.max_price || 0),
                    borderColor: 'transparent',
                    pointRadius: 0,
                    fill: false
                },
                {
                    label: 'Min',
                    data: sortedStats.map(s => s.min_price || 0),
                    borderColor: 'transparent',
                    pointRadius: 0,
                    fill: false
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            interaction: { intersect: false, mode: 'index' },
            plugins: {
                legend: { display: false },
                tooltip: {
                    backgroundColor: '#111827',
                    borderColor: '#1e293b',
                    borderWidth: 1,
                    titleFont: { family: "'JetBrains Mono', monospace", size: 11 },
                    bodyFont: { family: "'JetBrains Mono', monospace", size: 11 },
                    padding: 10,
                    filter: (item) => item.dataset.label === 'Median Price',
                    callbacks: {
                        afterBody: (contexts) => {
                            const index = contexts[0]?.dataIndex;
                            if (index !== undefined && priceRangeData[index]) {
                                const { min, max } = priceRangeData[index];
                                return [`Range: ¥${min.toLocaleString()} - ¥${max.toLocaleString()}`];
                            }
                            return [];
                        }
                    }
                }
            },
            scales: {
                x: {
                    grid: { color: '#1e293b40' },
                    ticks: { color: '#475569', font: { family: "'JetBrains Mono', monospace", size: 9 }, maxRotation: 0 }
                },
                y: {
                    beginAtZero: false,
                    grid: { color: '#1e293b40' },
                    ticks: {
                        color: '#475569',
                        font: { family: "'JetBrains Mono', monospace", size: 10 },
                        callback: v => '¥' + v.toLocaleString()
                    }
                }
            }
        }
    });
}

async function loadItems(id) {
    const grid = document.getElementById('itemsGrid');
    grid.innerHTML = '<div class="loading">LOADING</div>';

    try {
        const res = await fetch(`${API_BASE}/api/v1/ips/${id}/items?page_size=10`);
        const data = await res.json();
        if (data.code !== 0) throw new Error(data.message);

        // API returns data directly as array or as data.items
        const items = Array.isArray(data.data) ? data.data : (data.data?.items || []);

        if (items.length === 0) {
            grid.innerHTML = `
                <div class="empty-state" style="grid-column: 1 / -1;">
                    <div class="empty-icon">📦</div>
                    <p class="empty-text">NO ITEMS FOUND</p>
                </div>
            `;
            return;
        }

        grid.innerHTML = items.map(item => `
            <a href="${escapeHtml(item.item_url)}" target="_blank" class="item-card">
                <img class="item-image" src="${escapeHtml(item.image_url)}" alt="" loading="lazy"
                    onerror="this.src='data:image/svg+xml,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 1 1%22><rect fill=%22%231a2332%22 width=%221%22 height=%221%22/></svg>'">
                <div class="item-info">
                    <div class="item-title">${escapeHtml(item.title)}</div>
                    <div class="item-meta">
                        <span class="item-price">¥${item.price?.toLocaleString() || '---'}</span>
                        <span class="item-status ${item.status}">${item.status === 'sold' ? 'SOLD' : 'ON SALE'}</span>
                    </div>
                </div>
            </a>
        `).join('');

    } catch (e) {
        grid.innerHTML = `
            <div class="empty-state" style="grid-column: 1 / -1;">
                <div class="empty-icon">⚠️</div>
                <p class="empty-text">FAILED TO LOAD ITEMS</p>
            </div>
        `;
    }
}

// ============================================
// NAVIGATION
// ============================================
function showPage(page) {
    document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
    document.getElementById(`page-${page}`).classList.add('active');
    window.scrollTo({ top: 0, behavior: 'smooth' });
}

function goBack() {
    // Use hash navigation for proper browser history
    window.location.hash = '';
}

// ============================================
// UTILITIES
// ============================================
function showToast(message, type = '') {
    const toast = document.getElementById('toast');
    toast.textContent = message;
    toast.className = `toast show ${type}`;
    setTimeout(() => toast.classList.remove('show'), 3000);
}

function escapeHtml(str) {
    if (!str) return '';
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

// ============================================
// TAB NAVIGATION
// ============================================
function switchTab(tabName, updateURL = true) {
    // Update tab buttons
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tabName);
    });

    // Update tab content
    document.querySelectorAll('.tab-content').forEach(content => {
        content.classList.toggle('active', content.id === `tab-${tabName}`);
    });

    // Render IP table when switching to iplist tab
    if (tabName === 'iplist' && ipsWithData.length > 0) {
        renderIPTable();
    }

    // Update URL
    if (updateURL) {
        const url = new URL(window.location);
        if (tabName === 'dashboard') {
            url.searchParams.delete('tab');
        } else {
            url.searchParams.set('tab', tabName);
        }
        window.history.pushState({}, '', url);
    }
}

// ============================================
// IP TABLE
// ============================================
function renderIPTable() {
    const tbody = document.getElementById('ipTableBody');
    if (!tbody) return;

    // Update badge
    const badge = document.getElementById('ipCountBadge');
    if (badge) badge.textContent = ipsWithData.length;

    // Filter
    let filtered = ipsWithData;
    if (ipTableFilter) {
        const query = ipTableFilter.toLowerCase();
        filtered = ipsWithData.filter(ip =>
            (ip.name || '').toLowerCase().includes(query) ||
            (ip.name_en || '').toLowerCase().includes(query) ||
            (ip.name_cn || '').toLowerCase().includes(query) ||
            (ip.category || '').toLowerCase().includes(query)
        );
    }

    // Sort (使用 24h 聚合数据)
    filtered.sort((a, b) => {
        let valA, valB;
        const statsA = ipStats24h[a.id] || { inflow: 0, outflow: 0, score: 0 };
        const statsB = ipStats24h[b.id] || { inflow: 0, outflow: 0, score: 0 };

        switch (ipTableSort.field) {
            case 'name':
                valA = (a.name || '').toLowerCase();
                valB = (b.name || '').toLowerCase();
                break;
            case 'inflow':
                valA = statsA.inflow;
                valB = statsB.inflow;
                break;
            case 'outflow':
                valA = statsA.outflow;
                valB = statsB.outflow;
                break;
            case 'hot':
                valA = statsA.score;
                valB = statsB.score;
                break;
            default:
                valA = 0;
                valB = 0;
        }

        if (valA < valB) return ipTableSort.asc ? -1 : 1;
        if (valA > valB) return ipTableSort.asc ? 1 : -1;
        return 0;
    });

    // Update sort indicators
    document.querySelectorAll('.iplist-table th.sortable').forEach(th => {
        th.classList.remove('asc', 'desc');
        if (th.dataset.sort === ipTableSort.field) {
            th.classList.add(ipTableSort.asc ? 'asc' : 'desc');
        }
    });

    // Render rows
    if (filtered.length === 0) {
        tbody.innerHTML = '<tr><td colspan="4" class="no-data">NO MATCHING IPs</td></tr>';
        return;
    }

    tbody.innerHTML = filtered.map(ip => {
        // 使用 24h 聚合数据
        const stats = ipStats24h[ip.id] || { inflow: 0, outflow: 0, score: 0 };

        return `
            <tr onclick="selectIP(${ip.id})">
                <td>
                    <div class="ip-name-cell">
                        <div class="ip-avatar">
                            ${ip.image_url
                                ? `<img src="${escapeHtml(ip.image_url)}" alt="" onerror="this.parentElement.innerHTML='<span class=ip-avatar-text>${escapeHtml(ip.name?.charAt(0) || '?')}</span>'">`
                                : `<span class="ip-avatar-text">${escapeHtml(ip.name?.charAt(0) || '?')}</span>`
                            }
                        </div>
                        <div>
                            <div class="ip-name">${escapeHtml(ip.name)}</div>
                            ${ip.name_en ? `<div class="ip-name-en">${escapeHtml(ip.name_en)}</div>` : ''}
                        </div>
                    </div>
                </td>
                <td class="num value-inflow">+${stats.inflow}</td>
                <td class="num value-outflow">${stats.outflow}</td>
                <td class="num value-hot">${stats.score.toFixed(2)}</td>
            </tr>
        `;
    }).join('');
}

function sortIPTable(field) {
    if (ipTableSort.field === field) {
        ipTableSort.asc = !ipTableSort.asc;
    } else {
        ipTableSort.field = field;
        ipTableSort.asc = field === 'name';
    }
    renderIPTable();
}

function filterIPTable() {
    const input = document.getElementById('ipSearchInput');
    ipTableFilter = input ? input.value : '';
    renderIPTable();
}
