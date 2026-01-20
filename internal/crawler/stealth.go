package crawler

// enhancedStealthJS 增强版反检测脚本
// 补充 go-rod/stealth 可能未覆盖的检测点
const enhancedStealthJS = `
(function() {
    'use strict';

    // 1. 确保 navigator.webdriver 被正确隐藏
    // 某些 Cloudflare 版本会在 defineProperty 之后再次检测
    try {
        const originalQuery = window.navigator.permissions.query;
        window.navigator.permissions.query = (parameters) => (
            parameters.name === 'notifications' ?
                Promise.resolve({ state: Notification.permission }) :
                originalQuery(parameters)
        );
    } catch (e) {}

    // 2. 隐藏自动化特征
    try {
        // 移除 Chrome 自动化控制相关属性
        delete Object.getPrototypeOf(navigator).webdriver;
        
        // 重写 navigator.plugins 使其看起来像真实浏览器
        Object.defineProperty(navigator, 'plugins', {
            get: () => {
                const plugins = [
                    { name: 'Chrome PDF Plugin', filename: 'internal-pdf-viewer' },
                    { name: 'Chrome PDF Viewer', filename: 'mhjfbmdgcfjbbpaeojofohoefgiehjai' },
                    { name: 'Native Client', filename: 'internal-nacl-plugin' }
                ];
                plugins.length = 3;
                return plugins;
            }
        });
    } catch (e) {}

    // 3. 隐藏无头模式特征
    try {
        // 确保 window.chrome 存在且有正确的属性
        if (!window.chrome) {
            window.chrome = {};
        }
        if (!window.chrome.runtime) {
            window.chrome.runtime = {};
        }
        
        // 添加 chrome.csi 和 chrome.loadTimes（真实 Chrome 有这些）
        if (!window.chrome.csi) {
            window.chrome.csi = function() { return {}; };
        }
        if (!window.chrome.loadTimes) {
            window.chrome.loadTimes = function() { return {}; };
        }
    } catch (e) {}

    // 4. 模拟真实的 WebGL 渲染器信息
    try {
        const getParameter = WebGLRenderingContext.prototype.getParameter;
        WebGLRenderingContext.prototype.getParameter = function(parameter) {
            // UNMASKED_VENDOR_WEBGL
            if (parameter === 37445) {
                return 'Intel Inc.';
            }
            // UNMASKED_RENDERER_WEBGL
            if (parameter === 37446) {
                return 'Intel Iris OpenGL Engine';
            }
            return getParameter.call(this, parameter);
        };
    } catch (e) {}

    // 5. 隐藏 Selenium/Puppeteer 痕迹
    try {
        // 删除可能的 __selenium、__nightmare、__puppeteer 等属性
        const props = ['__webdriver_script_fn', '__driver_evaluate', '__webdriver_evaluate',
                       '__selenium_evaluate', '__fxdriver_evaluate', '__driver_unwrapped',
                       '__webdriver_unwrapped', '__selenium_unwrapped', '__fxdriver_unwrapped',
                       '_Selenium_IDE_Recorder', '_selenium', 'calledSelenium',
                       '_WEBDRIVER_ELEM_CACHE', 'ChromeDriverw', 'driver-evaluate',
                       'webdriver-evaluate', 'selenium-evaluate', 'webdriverCommand',
                       'webdriver-evaluate-response', '__webdriverFunc', '__$webdriverAsyncExecutor',
                       '__lastWatirAlert', '__lastWatirConfirm', '__lastWatirPrompt',
                       '_phantom', '__nightmare', '_puppeteer'];
        
        for (const prop of props) {
            try {
                if (window.hasOwnProperty(prop)) {
                    delete window[prop];
                }
                if (document.hasOwnProperty(prop)) {
                    delete document[prop];
                }
            } catch (e) {}
        }
    } catch (e) {}

    // 6. 修复 iframe contentWindow 检测
    try {
        const originalContentWindow = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'contentWindow');
        Object.defineProperty(HTMLIFrameElement.prototype, 'contentWindow', {
            get: function() {
                const iframe = originalContentWindow.get.call(this);
                try {
                    iframe.chrome = window.chrome;
                } catch (e) {}
                return iframe;
            }
        });
    } catch (e) {}

    // 7. 防止 toString 检测（某些网站会检查函数是否被修改）
    try {
        const oldToString = Function.prototype.toString;
        Function.prototype.toString = function() {
            if (this === window.navigator.permissions.query) {
                return 'function query() { [native code] }';
            }
            return oldToString.call(this);
        };
    } catch (e) {}
})();
`

// boolToGauge 将布尔值转换为 Prometheus Gauge 值
func boolToGauge(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
