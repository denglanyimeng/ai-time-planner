'use strict';

/* ==========================================================================
 * AI 时间规划助手 —— 前端单页应用
 *
 * 设计要点：
 *   1. 纯原生 ES2020，无框架、无构建步骤、无任何外部网络请求（可完全离线）。
 *   2. 所有渲染到 DOM 的用户数据一律经过 escapeHtml()，避免 XSS。
 *   3. 计时的权威数据全部来自后端（elapsedSeconds）；前端只用
 *      「本地时间推进的差值」做纯展示，每 5 秒轮询一次 /api/dashboard 对齐真值。
 *   4. 时区约定：业务日期用本地时间手工拼 YYYY-MM-DD（不用 toISOString，
 *      那是 UTC，会在北京时间 0:00–8:00 算错一天）；后端返回的 RFC3339 UTC
 *      时间戳展示时统一转换为北京时间。
 * ========================================================================== */

/* ==========================================================================
 * 一、通用工具函数
 * ========================================================================== */

/** 转义 HTML，凡是要拼进 innerHTML 的用户数据都必须先过这里。 */
function escapeHtml(value) {
  if (value === null || value === undefined) return '';
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

/** 数字补零到两位。 */
function pad2(n) {
  return String(n).padStart(2, '0');
}

/**
 * 今天的业务日期，格式 YYYY-MM-DD。
 * 必须用本地时间手动拼接：toISOString() 返回的是 UTC 日期，
 * 在东八区 00:00–08:00 之间会算成昨天，导致查询到错误的计划日。
 */
function todayStr(date = new Date()) {
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
}

/**
 * 把后端返回的 RFC3339 UTC 时间戳格式化为北京时间 HH:MM。
 * 时间字段可能为 null（尚未排程），统一显示为 --。
 */
function fmtTime(iso) {
  if (!iso) return '--';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '--';
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });
}

/** 当前北京时间 HH:MM:SS（用于顶部大字时钟）。 */
function fmtClock(date = new Date()) {
  return `${pad2(date.getHours())}:${pad2(date.getMinutes())}:${pad2(date.getSeconds())}`;
}

/** 中文长日期，例如 2025年6月1日 星期日。 */
function fmtDateCn(date = new Date()) {
  const wd = ['星期日', '星期一', '星期二', '星期三', '星期四', '星期五', '星期六'][date.getDay()];
  return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日 ${wd}`;
}

/** 距零点分钟数 -> "HH:MM"（1380 -> "23:00"）。1440 表示当日 24:00。 */
function minToHHMM(min) {
  const v = Math.round(Number(min));
  if (!Number.isFinite(v)) return '--';
  if (v === 1440) return '24:00';
  const wrapped = ((v % 1440) + 1440) % 1440;
  return `${pad2(Math.floor(wrapped / 60))}:${pad2(wrapped % 60)}`;
}

/** "HH:MM" -> 距零点分钟数（"23:00" -> 1380）；格式非法返回 null。 */
function hhmmToMin(text) {
  const m = /^(\d{1,2}):(\d{2})$/.exec(String(text ?? '').trim());
  if (!m) return null;
  const h = Number(m[1]);
  const mi = Number(m[2]);
  if (h > 23 || mi > 59) return null;
  return h * 60 + mi;
}

/** 校验后端只接受 #rgb / #rrggbb；同时防止把任意字符串写进 style 属性。 */
function safeColor(color) {
  const c = String(color ?? '').trim();
  return /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(c) ? c : '#8b95ad';
}

/** 秒 -> "X 小时 Y 分 Z 秒" / "M 分 SS 秒"。 */
function fmtSeconds(totalSeconds) {
  const s = Math.max(0, Math.floor(Number(totalSeconds) || 0));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return `${h} 小时 ${m} 分 ${pad2(sec)} 秒`;
  return `${m} 分 ${pad2(sec)} 秒`;
}

/** 秒 -> 紧凑计时格式 "MM:SS" 或 "H:MM:SS"（标签页标题与悬浮窗用）。 */
function fmtClockSpan(totalSeconds) {
  const s = Math.max(0, Math.floor(Number(totalSeconds) || 0));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return h > 0 ? `${h}:${pad2(m)}:${pad2(sec)}` : `${pad2(m)}:${pad2(sec)}`;
}

/** 秒 -> "X 小时 Y 分钟"（长期目标累计时长用）。 */
function fmtHoursMinutes(totalSeconds) {
  const s = Math.max(0, Math.floor(Number(totalSeconds) || 0));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h} 小时 ${m} 分钟`;
  if (m > 0) return `${m} 分钟`;
  return `${s} 秒`;
}

/** 分钟 -> 简短文案。 */
function fmtMinutes(min) {
  const v = Math.max(0, Math.round(Number(min) || 0));
  if (v < 60) return `${v} 分钟`;
  const h = Math.floor(v / 60);
  const m = v % 60;
  return m ? `${h} 小时 ${m} 分` : `${h} 小时`;
}

/** 数字保留可读精度（负载比等）。 */
function fmtNumber(v, digits = 2) {
  const n = Number(v);
  if (!Number.isFinite(n)) return '--';
  return n.toFixed(digits);
}

const qs = (sel, root = document) => root.querySelector(sel);
const qsa = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const WEEKDAY_NAMES = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];

const STATUS_TEXT = {
  pending: '待开始',
  running: '进行中',
  paused: '已暂停',
  done: '已完成',
  undone: '未完成',
};

const SOURCE_TEXT = {
  ai: 'AI 生成',
  manual: '手动新增',
  recurring: '周期任务',
  carryover: '承接历史',
};

/** 频率枚举 -> 中文。 */
const FREQ_TEXT = {
  daily: '每天',
  every2days: '每 2 天',
  every3days: '每 3 天',
  weekly: '按星期',
};

/**
 * 计算任务的实时展示耗时（纯展示；真实数据以服务端 elapsedSeconds 为准）。
 *
 * 关键点：服务端的 elapsedSeconds 是「已累计秒数 + (服务端此刻 − 本轮开始时刻)」，
 * 也就是说它**已经包含了本轮已经过去的时间**。
 * 因此这里只需要补上「这份数据取回之后又过了多久」，绝不能再按 runStartedAt
 * 把本轮时长重算一遍 —— 那样本轮会被计两次，实时显示接近真实值的 2 倍，
 * 而点「完成」时服务端用的是正确值，两边就对不上了。
 */
function liveElapsedSeconds(task) {
  const base = Math.max(0, Math.floor(Number(task.elapsedSeconds) || 0));
  if (task.status !== 'running') return base;
  if (!state.dashFetchedAt) return base;
  // 只用本机时钟做差值，避免依赖服务端与本机的时钟是否一致
  const since = Math.max(0, (Date.now() - state.dashFetchedAt) / 1000);
  return base + Math.floor(since);
}

/* ==========================================================================
 * 二、API 层
 * ========================================================================== */

/**
 * 统一请求函数。
 * - 自动附加 Content-Type: application/json
 * - 自动解包成功信封里的 data
 * - ok 为 false（或 HTTP 非 2xx）时抛出带 error 文案的 Error，由调用方 catch 后提示
 */
async function request(method, path, body) {
  const init = { method, headers: {} };
  if (body !== undefined && body !== null) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }

  let res;
  try {
    res = await fetch(path, init);
  } catch (err) {
    throw new Error('网络请求失败，请确认后端服务是否正在运行');
  }

  const text = await res.text();
  let payload = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch (err) {
      payload = null;
    }
  }

  if (!res.ok) {
    const msg = payload && payload.error ? payload.error : `请求失败（HTTP ${res.status}）`;
    throw new Error(msg);
  }
  if (payload && payload.ok === false) {
    throw new Error(payload.error || '请求失败');
  }
  // 心跳等接口只返回 {"ok":true}，没有 data 字段，此时返回 null
  return payload && Object.prototype.hasOwnProperty.call(payload, 'data') ? payload.data : null;
}

const enc = encodeURIComponent;

/** 接口封装：路径与字段名严格对齐后端契约。 */
const API = {
  // 仪表盘
  dashboard: (date) => request('GET', `/api/dashboard?date=${enc(date)}`),

  // 任务
  tasks: (date) => request('GET', `/api/tasks?date=${enc(date)}`),
  createTask: (body) => request('POST', '/api/tasks', body),
  updateTask: (id, body) => request('PUT', `/api/tasks/${enc(id)}`, body),
  deleteTask: (id) => request('DELETE', `/api/tasks/${enc(id)}`),
  setActual: (id, seconds) => request('PUT', `/api/tasks/${enc(id)}/actual`, { seconds }),

  // 计时
  start: (id) => request('POST', `/api/tasks/${enc(id)}/start`),
  pause: (id) => request('POST', `/api/tasks/${enc(id)}/pause`),
  resume: (id) => request('POST', `/api/tasks/${enc(id)}/resume`),
  complete: (id) => request('POST', `/api/tasks/${enc(id)}/complete`),
  undone: (id) => request('POST', `/api/tasks/${enc(id)}/undone`),
  heartbeat: (id) => request('POST', `/api/tasks/${enc(id)}/heartbeat`),

  // 计划
  generate: (body) => request('POST', '/api/plan/generate', body),
  align: (planDate) => request('POST', '/api/plan/align', { planDate }),
  reorder: (planDate, orderedIds) => request('POST', '/api/plan/reorder', { planDate, orderedIds }),
  planConfig: (date) => request('GET', `/api/plan/config?date=${enc(date)}`),
  updatePlanConfig: (body) => request('PUT', '/api/plan/config', body),

  // 标签
  tags: () => request('GET', '/api/tags'),
  createTag: (body) => request('POST', '/api/tags', body),
  updateTag: (id, body) => request('PUT', `/api/tags/${enc(id)}`, body),
  deleteTag: (id) => request('DELETE', `/api/tags/${enc(id)}`),

  // 时间块
  timeblocks: () => request('GET', '/api/timeblocks'),
  createTimeBlock: (body) => request('POST', '/api/timeblocks', body),
  updateTimeBlock: (id, body) => request('PUT', `/api/timeblocks/${enc(id)}`, body),
  deleteTimeBlock: (id) => request('DELETE', `/api/timeblocks/${enc(id)}`),

  // 周期任务
  recurring: () => request('GET', '/api/recurring'),
  createRecurring: (body) => request('POST', '/api/recurring', body),
  updateRecurring: (id, body) => request('PUT', `/api/recurring/${enc(id)}`, body),
  deleteRecurring: (id) => request('DELETE', `/api/recurring/${enc(id)}`),
  materialize: (planDate) => request('POST', '/api/recurring/materialize', { planDate }),
  resolveConflict: (planDate, recurringId, action) =>
    request('POST', '/api/recurring/conflict', { planDate, recurringId, action }),

  // 未完成池
  pool: (date) => request('GET', `/api/pool?date=${enc(date)}`),
  adopt: (taskId, targetDate) => request('POST', '/api/pool/adopt', { taskId, targetDate }),

  // 长期目标
  goals: () => request('GET', '/api/goals'),
  createGoal: (body) => request('POST', '/api/goals', body),
  updateGoal: (id, body) => request('PUT', `/api/goals/${enc(id)}`, body),
  deleteGoal: (id) => request('DELETE', `/api/goals/${enc(id)}`),
  goalProgress: () => request('GET', '/api/goals/progress'),

  // 统计
  statsTrend: (days) => request('GET', `/api/stats/trend?days=${enc(days)}`),
  statsTags: (days) => request('GET', `/api/stats/tags?days=${enc(days)}`),
  analyze: (planDate) => request('POST', '/api/stats/analyze', { planDate }),

  // 数据管理
  cleanup: () => request('POST', '/api/admin/cleanup'),
  clearAll: () => request('POST', '/api/admin/clear', { confirm: 'DELETE' }),

  // 系统配置
  config: () => request('GET', '/api/config'),
  updateConfig: (body) => request('PUT', '/api/config', body),
};

/**
 * 导入备份：必须用 FormData 上传，且不能手动设置 Content-Type
 * （浏览器需要自己补上 multipart 边界）。
 */
async function uploadBackup(file) {
  const fd = new FormData();
  fd.append('file', file);
  let res;
  try {
    res = await fetch('/api/import/backup', { method: 'POST', body: fd });
  } catch (err) {
    throw new Error('上传失败，请确认后端服务是否正在运行');
  }
  const text = await res.text();
  let payload = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch (err) {
      payload = null;
    }
  }
  if (!res.ok || (payload && payload.ok === false)) {
    throw new Error((payload && payload.error) || `导入失败（HTTP ${res.status}）`);
  }
  return payload ? payload.data : null;
}

/* ==========================================================================
 * 三、Toast 提示组件
 * ========================================================================== */

/** 右上角自动消失的提示。所有接口错误都应经过这里暴露给用户。 */
function toast(message, type = 'info', timeout = 4200) {
  const host = qs('#toast-host');
  if (!host) return;

  const el = document.createElement('div');
  el.className = `toast toast-${type}`;
  el.innerHTML =
    `<span class="toast-text">${escapeHtml(message)}</span>`;
  host.appendChild(el);

  const remove = () => {
    if (!el.isConnected) return;
    el.classList.add('leaving');
    setTimeout(() => el.remove(), 220);
  };
  const timer = setTimeout(remove, timeout);
  el.addEventListener('click', () => {
    clearTimeout(timer);
    remove();
  });
}

/** 统一的「执行 + 成功提示 + 失败提示」包装，避免错误被吞掉。 */
async function attempt(fn, successMsg) {
  try {
    const data = await fn();
    if (successMsg) toast(successMsg, 'success');
    return { ok: true, data };
  } catch (err) {
    toast((err && err.message) || '操作失败', 'error');
    return { ok: false, error: err };
  }
}

/* ==========================================================================
 * 四、自定义弹窗（替代原生 confirm / prompt）
 * ========================================================================== */

let activeModal = null;

/**
 * 打开弹窗。
 * @param {object} opts
 *   title    标题（会转义）
 *   body     已构造好的 HTML（其中用户数据必须自行转义）
 *   footer   已构造好的 HTML 按钮区
 *   onClose  关闭回调
 */
function openModal(opts) {
  closeModal();
  const host = qs('#modal-host');

  const modal = document.createElement('div');
  modal.className = 'modal';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.innerHTML =
    `<div class="modal-head">` +
      `<span class="modal-title">${escapeHtml(opts.title || '提示')}</span>` +
      `<button type="button" class="modal-close" data-modal-close aria-label="关闭">×</button>` +
    `</div>` +
    `<div class="modal-body">${opts.body || ''}</div>` +
    (opts.footer ? `<div class="modal-foot">${opts.footer}</div>` : '');

  host.innerHTML = '';
  host.appendChild(modal);
  host.hidden = false;

  activeModal = { el: modal, onClose: opts.onClose };
  return activeModal;
}

function closeModal() {
  const host = qs('#modal-host');
  const prev = activeModal;
  activeModal = null;
  if (host) {
    host.hidden = true;
    host.innerHTML = '';
  }
  if (prev && typeof prev.onClose === 'function') prev.onClose();
}

// 关闭交互：点击遮罩、点击右上角 ×、按 Esc
qs('#modal-host').addEventListener('click', (e) => {
  if (e.target === e.currentTarget) closeModal();
  if (e.target.closest('[data-modal-close]')) closeModal();
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && activeModal) closeModal();
});

/**
 * 确认框。
 * @param {string} message 提示文案
 * @param {object} opts    title / okText / danger / requireText（需手动输入指定文本才能确认）
 * @returns {Promise<boolean>}
 */
function uiConfirm(message, opts = {}) {
  const { title = '请确认', okText = '确定', danger = false, requireText = null } = opts;
  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };

    const requireBlock = requireText
      ? `<label class="field" style="margin-top:14px">` +
          `<span class="field-label">请输入 <code>${escapeHtml(requireText)}</code> 以确认</span>` +
          `<input type="text" id="modal-require-input" autocomplete="off" placeholder="${escapeHtml(requireText)}">` +
        `</label>`
      : '';

    const modal = openModal({
      title,
      body: `<p class="modal-note">${escapeHtml(message).replace(/\n/g, '<br>')}</p>${requireBlock}`,
      footer:
        `<button type="button" class="btn" data-act="cancel">取消</button>` +
        `<button type="button" class="btn ${danger ? 'btn-danger' : 'btn-primary'}" data-act="ok"` +
        `${requireText ? ' disabled' : ''}>${escapeHtml(okText)}</button>`,
      onClose: () => finish(false),
    });

    const okBtn = qs('[data-act="ok"]', modal.el);
    const cancelBtn = qs('[data-act="cancel"]', modal.el);
    const input = requireText ? qs('#modal-require-input', modal.el) : null;

    if (input) {
      input.addEventListener('input', () => {
        okBtn.disabled = input.value.trim() !== requireText;
      });
      input.focus();
    } else {
      okBtn.focus();
    }

    okBtn.addEventListener('click', () => {
      finish(true);
      closeModal();
    });
    cancelBtn.addEventListener('click', () => {
      finish(false);
      closeModal();
    });
  });
}

/** 简单的信息弹窗（内容为已转义安全的 HTML）。 */
function uiInfo(title, bodyHtml) {
  openModal({
    title,
    body: bodyHtml,
    footer: '<button type="button" class="btn btn-primary" data-act="ok">知道了</button>',
  });
  const foot = qs('.modal [data-act="ok"]');
  if (foot) foot.addEventListener('click', closeModal);
}

/* ==========================================================================
 * 五、全局状态
 * ========================================================================== */

const state = {
  view: 'today',
  date: todayStr(),      // 当前业务日期（本地时间）
  dash: null,            // 最新一次 /api/dashboard 结果
  tags: [],              // 标签列表
  tasks: [],             // 当日任务（展示顺序）
  dragId: null,          // 正在拖拽的任务 ID
  goals: [],             // 长期目标原始列表（编辑时取用）
  editingTaskId: null,
  editingTagId: null,
  editingBlockId: null,
  editingRecurringId: null,
  editingGoalId: null,
  generating: false,
};

/* ==========================================================================
 * 六、标签选择器辅助
 * ========================================================================== */

/** 生成 <option> 列表；selectedId 为空表示「不设置标签」。 */
function tagOptionsHtml(selectedId, emptyLabel = '不设置标签') {
  const selected = selectedId === null || selectedId === undefined ? '' : String(selectedId);
  const head = `<option value=""${selected === '' ? ' selected' : ''}>${escapeHtml(emptyLabel)}</option>`;
  return head + state.tags.map((t) => {
    const id = String(t.id);
    return `<option value="${escapeHtml(id)}"${id === selected ? ' selected' : ''}>${escapeHtml(t.name)}</option>`;
  }).join('');
}

/** 按标签 ID 找标签对象。 */
function findTag(tagId) {
  if (tagId === null || tagId === undefined) return null;
  return state.tags.find((t) => String(t.id) === String(tagId)) || null;
}

/** 渲染标签胶囊。 */
function tagChipHtml(name, color) {
  if (!name) return '';
  return `<span class="tag-chip" style="--tag-color:${escapeHtml(safeColor(color))}">${escapeHtml(name)}</span>`;
}

/** 状态徽章。 */
function statusBadgeHtml(status) {
  const key = STATUS_TEXT[status] ? status : 'pending';
  return `<span class="badge badge-${escapeHtml(key)}">${escapeHtml(STATUS_TEXT[key])}</span>`;
}

/* ==========================================================================
 * 七、今日计划视图
 * ========================================================================== */

async function loadTags() {
  const r = await attempt(() => API.tags());
  if (r.ok) {
    state.tags = Array.isArray(r.data) ? r.data : [];
    refreshTagSelects();
  }
}

/** 刷新所有静态标签下拉框。 */
function refreshTagSelects() {
  const recTag = qs('#rec-tag');
  const goalTag = qs('#goal-tag');
  if (recTag) recTag.innerHTML = tagOptionsHtml(recTag.value, '不设置标签');
  if (goalTag) goalTag.innerHTML = tagOptionsHtml(goalTag.value, '不关联标签');
}

/** 拉取仪表盘并渲染今日视图。 */
async function loadToday() {
  const r = await attempt(() => API.dashboard(state.date));
  if (!r.ok) return;
  state.dash = r.data;
  state.tasks = Array.isArray(r.data?.tasks) ? r.data.tasks.slice() : [];
  // 记录数据取回的时刻：实时耗时以它为基准往后推，才能与 elapsedSeconds 的口径一致
  state.dashFetchedAt = Date.now();
  renderToday();
}

function renderToday() {
  const dash = state.dash;
  if (!dash) return;

  // 顶部概览
  qs('#glance-deadline').textContent = dash.deadline || '--';
  const av = dash.availability || {};
  qs('#glance-available').textContent = fmtMinutes(av.availableMin);
  qs('#glance-demand').textContent = fmtMinutes(av.demandMin);
  qs('#glance-ratio').textContent = Number.isFinite(Number(av.loadRatio))
    ? `${fmtNumber(av.loadRatio * 100, 0)}%`
    : '--';

  renderTodayAlerts(dash);
  renderTodayStats(dash);
  renderHero(dash);

  // 未完成池小红点
  const dot = qs('#nav-pool-dot');
  if (dot) dot.hidden = !(Number(dash.poolCount) > 0);

  // 拖拽进行中时不要重绘列表，否则会打断拖动
  if (state.dragId === null) renderTaskList();

  updateLiveElapsed();
}

/**
 * 更新首屏玻璃环上的进度弧。
 *
 * 环是纯 CSS 画的：外圈细弧用 conic-gradient，内圈刻度用重复锥形渐变，
 * 弧有多长只看一个自定义属性 --pct（0–100），由 @property 注册后可以平滑过渡。
 * 所以这里只需要写一个数值，不必再关心半径、周长或 SVG 路径长度。
 *
 * 只在数值真的变了才写 DOM：仪表盘每 5 秒轮询一次，
 * 无条件重设会让弧每 5 秒从头扫一遍，像在抽搐。
 */
function updateHeroRing(done, total) {
  const ratio = total > 0 ? Math.min(1, Math.max(0, done / total)) : 0;
  const pct = Math.round(ratio * 1000) / 10;   // 保留一位小数，弧更细腻

  const pctEl = qs('#hero-ring-pct');
  const arcEl = qs('#hero-arc');

  const text = `${Math.round(ratio * 100)}%`;
  if (pctEl && pctEl.textContent !== text) pctEl.textContent = text;
  if (arcEl) {
    const value = String(pct);
    if (arcEl.style.getPropertyValue('--pct') !== value) {
      arcEl.style.setProperty('--pct', value);
    }
  }
}

/**
 * 渲染首页欢迎区。
 *
 * 原来首屏只有时钟和卡片，没有任务时几乎是一片空白，看不出设计感。
 * 这里按时段问候，并把当日进展汇成一句话，让首屏即使空着也有内容、有出口。
 */
function renderHero(dash) {
  const greetEl = qs('#hero-greet');
  const titleEl = qs('#hero-title');
  const subEl = qs('#hero-sub');
  const actEl = qs('#hero-actions');
  if (!greetEl || !titleEl || !subEl || !actEl) return;

  const hour = new Date().getHours();
  const greeting =
    hour < 5 ? '夜深了' :
    hour < 11 ? '早上好' :
    hour < 14 ? '中午好' :
    hour < 18 ? '下午好' :
    hour < 23 ? '晚上好' : '夜深了';

  // 抬头行做成技术感的「日期 · 星期 · 问候」，比孤零零一句「早上好」更像一回事
  greetEl.textContent = `${fmtDateCn()} · ${greeting}`;

  const total = Number(dash.total) || 0;
  const done = Number(dash.done) || 0;
  const undone = Number(dash.undone) || 0;
  const pending = Number(dash.pending) || 0;
  const est = Number(dash.estimateMinutes) || 0;
  const actual = Number(dash.actualMinutes) || 0;
  const av = dash.availability || {};

  updateHeroRing(done, total);

  // 还没有任何任务：把首屏空白变成一个明确的入口
  if (!total) {
    titleEl.textContent = '今天还没有安排';
    subEl.textContent = '说说今天想做什么，我来帮你排成一张看得见的时间表。';
    actEl.innerHTML =
      `<button type="button" class="btn btn-primary" data-hero="generate">让 AI 生成今日计划</button>` +
      `<button type="button" class="btn" data-hero="add">手动添加一条</button>`;
    return;
  }

  // 数字单独包一层，好在标题里放大 134% 并染成青柠色
  titleEl.innerHTML = `今天有 <span class="hero-count">${escapeHtml(String(total))}</span> 件事`;

  const parts = [];
  if (est > 0) parts.push(`预计投入 ${fmtMinutes(est)}`);
  if (done > 0) parts.push(`已完成 ${done} 件`);
  if (pending > 0) parts.push(`待办 ${pending} 件`);
  if (undone > 0) parts.push(`未完成 ${undone} 件`);
  if (actual > 0) parts.push(`实际已投入 ${fmtMinutes(actual)}`);
  subEl.textContent = parts.join(' · ');

  // 负载预警在下方提示区已有完整说明，这里只做一个轻量提醒，避免重复堆叠
  actEl.innerHTML = av.overloaded
    ? `<span class="hero-tip hero-tip-warn">负载偏高，任务总量已超出今日可用时间</span>`
    : `<span class="hero-tip">点「开始」即可计时；拖动卡片可调整顺序</span>`;
}

function renderTodayAlerts(dash) {
  const host = qs('#today-alerts');
  const blocks = [];
  const av = dash.availability || {};

  if (av.overloaded) {
    const over = Math.max(0, Math.round((Number(av.demandMin) || 0) - (Number(av.availableMin) || 0)));
    blocks.push(
      `<div class="alert alert-danger">` +
        `<div class="alert-body">` +
          `<div class="alert-title">负载过高预警</div>` +
          `<p>今日任务约 ${escapeHtml(fmtMinutes(av.demandMin))}，超出可用 ${escapeHtml(fmtMinutes(av.availableMin))}` +
          `${over > 0 ? `（超出约 ${escapeHtml(fmtMinutes(over))}）` : ''}。</p>` +
          (Number(av.overflowCount) > 0
            ? `<p>按当前截止时间，有 ${escapeHtml(String(av.overflowCount))} 条任务排不进去，会进入未完成池。建议减少任务量、延长可用时间块，或点击「一键对齐北京时间」重新排程。</p>`
            : '<p>建议减少任务量或延长可用时间块。</p>') +
        `</div>` +
      `</div>`
    );
  }

  if (dash.overtimeRunning) {
    blocks.push(
      `<div class="alert alert-warn">` +
        `<div class="alert-body">` +
          `<div class="alert-title">已经超过今天的截止时间了</div>` +
          `<p>${escapeHtml(dash.overtimeNotice || '当前任务已超过当日结束时间，注意休息。')}</p>` +
        `</div>` +
      `</div>`
    );
  }

  if (dash.advice) {
    blocks.push(
      `<div class="alert alert-ok">` +
        `<div class="alert-body">` +
          `<div class="alert-title">AI 规划建议</div>` +
          `<p>${escapeHtml(dash.advice)}</p>` +
        `</div>` +
      `</div>`
    );
  }

  if (dash.summary) {
    blocks.push(
      `<div class="alert">` +
        `<div class="alert-body">` +
          `<div class="alert-title">AI 效率总结</div>` +
          `<p>${escapeHtml(dash.summary)}</p>` +
        `</div>` +
      `</div>`
    );
  }

  if (dash.constraint) {
    blocks.push(
      `<div class="alert">` +
        `<div class="alert-body"><p>本次生成使用的临时约束：${escapeHtml(dash.constraint)}</p></div>` +
      `</div>`
    );
  }

  if (dash.aiEnabled === false) {
    blocks.push(
      `<div class="alert">` +
        `<div class="alert-body"><p>尚未配置 AI 接口，<b>智能生成</b>将使用本地规则排程。可在「设置」中的「AI 配置」中填写。</p></div>` +
      `</div>`
    );
  }

  // 同上：内容没变就不动 DOM，避免提示条每 5 秒闪一次
  const html = blocks.join('');
  if (host.innerHTML !== html) host.innerHTML = html;
}

function renderTodayStats(dash) {
  const items = [
    ['任务总数', String(Number(dash.total) || 0), '条'],
    ['已完成', String(Number(dash.done) || 0), '条'],
    ['未完成', String(Number(dash.undone) || 0), '条'],
    ['待开始 / 进行中', String(Number(dash.pending) || 0), '条'],
    ['实际总耗时', fmtMinutes(Number(dash.actualMinutes) || 0), ''],
    ['预估总耗时', fmtMinutes(Number(dash.estimateMinutes) || 0), ''],
  ];
  const html = items.map(([label, value, unit]) =>
    `<div class="stat-item">` +
      `<span class="stat-label">${escapeHtml(label)}</span>` +
      `<span class="stat-value">${escapeHtml(value)}${unit ? `<small>${escapeHtml(unit)}</small>` : ''}</span>` +
    `</div>`
  ).join('');

  // 每 5 秒的轮询会重跑这里，内容没变就不要整片换掉 DOM
  const host = qs('#today-stats');
  if (host && host.innerHTML !== html) host.innerHTML = html;
}

/**
 * 空状态插画：一张还没写字的便签 + 一支铅笔。
 *
 * 在暗场玻璃这套语言里，插画只保留线稿 —— 细白线画轮廓，
 * 唯一的彩色是那道青柠色的勾。填充色会和玻璃面板打架，所以全部去掉。
 */
const EMPTY_ART =
  '<svg viewBox="0 0 190 140" fill="none" stroke-linecap="round" stroke-linejoin="round">' +
    '<g transform="rotate(-6 92 70)">' +
      '<rect x="52" y="25" width="80" height="92" rx="10" stroke="#8b93a1" stroke-width="1.4"/>' +
      '<g stroke="#8b93a1" stroke-width="1.4" opacity=".7">' +
        '<line x1="68" y1="25" x2="68" y2="34"/>' +
        '<line x1="86" y1="25" x2="86" y2="34"/>' +
        '<line x1="104" y1="25" x2="104" y2="34"/>' +
        '<line x1="121" y1="25" x2="121" y2="34"/>' +
      '</g>' +
      '<g stroke="#565e6b" stroke-width="1.4">' +
        '<line x1="68" y1="52" x2="116" y2="52"/>' +
        '<line x1="68" y1="67" x2="102" y2="67"/>' +
        '<line x1="99" y1="88" x2="116" y2="88"/>' +
      '</g>' +
      '<path d="M68 88l7 7 13-15" stroke="#c9ff4a" stroke-width="1.8"/>' +
    '</g>' +
    '<g transform="rotate(32 146 80)">' +
      '<rect x="140" y="37" width="13" height="52" rx="3" stroke="#8b93a1" stroke-width="1.4"/>' +
      '<path d="M140 89h13l-6.5 12z" stroke="#8b93a1" stroke-width="1.4"/>' +
      '<rect x="140" y="29" width="13" height="8" rx="2.5" stroke="#8b93a1" stroke-width="1.4"/>' +
    '</g>' +
    '<g stroke="#4b525e" stroke-width="1.4">' +
      '<path d="M36 42v8M32 46h8"/>' +
      '<path d="M156 18v6M153 21h6"/>' +
    '</g>' +
  '</svg>';

/**
 * 任务列表当前内容的指纹。
 *
 * 仪表盘每 5 秒轮询一次，而 renderTaskList 过去是无条件 innerHTML 重建 ——
 * 于是鼠标悬停状态、正在拖拽的手感、以及用户刚选中的文字，每 5 秒就被清空一次。
 * 这里只比较「会影响卡片长相」的字段，不含 actualSeconds：
 * 已耗时由 updateLiveElapsed 每秒单独写入某个 span，不需要靠重建整表来刷新。
 */
let lastTaskListSignature = '';

function taskListSignature(tasks) {
  return tasks
    .map((t) => [
      t.id, t.status, t.name, t.tagName || '', t.tagColor || '',
      t.plannedStart || '', t.plannedEnd || '', t.estimateMin,
      t.source || '', t.overtimeNow ? 1 : 0, t.isOvertime ? 1 : 0,
    ].join('\u001f'))
    .join('\u001e');
}

/** 渲染任务时间线。 */
function renderTaskList() {
  const list = qs('#task-list');
  const tasks = state.tasks;

  if (!tasks.length) {
    const emptyHtml =
      `<li><div class="empty empty-rich">` +
        `<div class="empty-art" aria-hidden="true">${EMPTY_ART}</div>` +
        `<strong>今天还是一张白纸</strong>` +
        `<p>把今天想做的事说给我听，我会按你的可用时间排成一张时间表；` +
        `也可以自己手动加一条，系统同样会自动计时与记录耗时。</p>` +
        `<div class="empty-actions">` +
          `<button type="button" class="btn btn-primary" data-empty="generate">让 AI 生成今日计划</button>` +
          `<button type="button" class="btn" data-empty="add">手动添加一条</button>` +
        `</div>` +
      `</div></li>`;

    if (lastTaskListSignature !== 'EMPTY' || list.innerHTML !== emptyHtml) {
      lastTaskListSignature = 'EMPTY';
      list.innerHTML = emptyHtml;
    }
    return;
  }

  const signature = taskListSignature(tasks);
  if (signature === lastTaskListSignature) return;
  lastTaskListSignature = signature;

  list.innerHTML = tasks.map((t, index) => taskCardHtml(t, index)).join('');
}

function taskCardHtml(t, index) {
  const status = STATUS_TEXT[t.status] ? t.status : 'pending';
  const startLabel = t.plannedStart ? fmtTime(t.plannedStart) : '未排';
  const elapsed = liveElapsedSeconds(t);

  // 开始时间已经画在左侧时间刻度上，这里只补结束时间与时长，
  // 避免同一个 09:00 在卡片里重复出现两遍。
  const meta = [];
  if (t.plannedEnd) meta.push(`<span>结束 ${escapeHtml(fmtTime(t.plannedEnd))}</span>`);
  meta.push(`<span>预估 ${escapeHtml(fmtMinutes(t.estimateMin))}</span>`);
  meta.push(
    `<span class="live" data-live-elapsed="${escapeHtml(String(t.id))}">实际 ${escapeHtml(fmtSeconds(elapsed))}</span>`
  );
  if (!t.plannedStart) meta.push('<span class="over">时间不足，未排入时间线</span>');

  const badges = [statusBadgeHtml(status)];
  if (t.source && SOURCE_TEXT[t.source]) {
    badges.push(`<span class="badge badge-soft">${escapeHtml(SOURCE_TEXT[t.source])}</span>`);
  }
  if (t.overtimeNow) badges.push('<span class="badge badge-warn">已超时</span>');
  if (t.isOvertime) badges.push('<span class="badge badge-warn">超时未确认</span>');

  const actions = [];
  if (t.status === 'pending') {
    actions.push(`<button type="button" class="btn btn-sm btn-primary" data-act="start" data-id="${escapeHtml(String(t.id))}">开始</button>`);
  } else if (t.status === 'running') {
    actions.push(`<button type="button" class="btn btn-sm" data-act="pause" data-id="${escapeHtml(String(t.id))}">暂停</button>`);
  } else if (t.status === 'paused') {
    actions.push(`<button type="button" class="btn btn-sm btn-primary" data-act="resume" data-id="${escapeHtml(String(t.id))}">继续</button>`);
  }
  if (t.status !== 'done') {
    actions.push(`<button type="button" class="btn btn-sm" data-act="complete" data-id="${escapeHtml(String(t.id))}">完成</button>`);
  }
  if (t.status !== 'done' && t.status !== 'undone') {
    actions.push(`<button type="button" class="btn btn-sm btn-ghost" data-act="undone" data-id="${escapeHtml(String(t.id))}">未完成</button>`);
  }
  actions.push(`<button type="button" class="btn btn-sm btn-ghost" data-act="edit" data-id="${escapeHtml(String(t.id))}">编辑</button>`);
  actions.push(`<button type="button" class="btn btn-sm btn-ghost" data-act="delete" data-id="${escapeHtml(String(t.id))}">删除</button>`);

  return (
    `<li class="task-card status-${escapeHtml(status)}" draggable="true" data-id="${escapeHtml(String(t.id))}">` +
      `<div class="task-rail" aria-hidden="true">` +
        `<span class="rail-time${t.plannedStart ? '' : ' is-empty'}">${escapeHtml(startLabel)}</span>` +
        `<span class="rail-node"></span>` +
      `</div>` +
      `<div class="task-main">` +
        `<div class="task-head">` +
          `<span class="drag-handle" title="按住拖动可调整顺序" aria-hidden="true"></span>` +
          `<span class="task-name">${escapeHtml(t.name)}</span>` +
          tagChipHtml(t.tagName, t.tagColor) +
          badges.join('') +
        `</div>` +
        `<div class="task-meta">${meta.join('')}</div>` +
      `</div>` +
      `<div class="task-actions">${actions.join('')}</div>` +
    `</li>`
  );
}

/** 每秒只更新时钟与正在计时任务的已耗时，不重绘整表。 */
function updateLiveElapsed() {
  qsa('[data-live-elapsed]').forEach((el) => {
    const id = el.getAttribute('data-live-elapsed');
    const task = state.tasks.find((t) => String(t.id) === String(id));
    if (!task) return;
    el.textContent = `实际 ${fmtSeconds(liveElapsedSeconds(task))}`;
  });
  updateTabTitle();
  renderFloatingTimer();
}

/**
 * 把正在计时的任务与已耗时写进标签页标题。
 * 浏览器切到后台、或标签被挤成一条时，也能从标签栏／任务栏瞥见进度。
 */
const APP_TITLE = '时间规划';
function updateTabTitle() {
  const running = state.tasks.find((t) => t.status === 'running');
  const next = running
    ? `${fmtClockSpan(liveElapsedSeconds(running))} · ${running.name}`
    : APP_TITLE;
  if (document.title !== next) document.title = next;
}

/* ==========================================================================
 * 悬浮计时窗
 *
 * 工作的时候不可能一直盯着页面看。这里用 Document Picture-in-Picture
 * （Chrome / Edge 116+）开一个**始终置顶**的小窗，把当前任务和已耗时
 * 浮在最上层，可以压住 IDE、文档、视频，瞥一眼就知道做了多久，
 * 并且可以直接在悬浮窗里暂停 / 继续 / 完成，不必切回主页面。
 *
 * 不支持该 API 的浏览器退回普通弹窗：不能保证置顶，但依然能独立显示。
 * ========================================================================== */

let floatWin = null;      // 悬浮窗的 window 引用
let floatTaskId = null;   // 悬浮窗当前盯着的任务（暂停后仍可从这里继续）

/** 悬浮窗应当显示的任务：优先正在计时的；否则沿用刚才那条已暂停的。 */
function floatingTask() {
  const running = state.tasks.find((t) => t.status === 'running');
  if (running) {
    floatTaskId = running.id;
    return running;
  }
  if (floatTaskId !== null) {
    const t = state.tasks.find((x) => String(x.id) === String(floatTaskId));
    if (t && t.status === 'paused') return t;
  }
  return null;
}

function openFloatingTimer() {
  const running = state.tasks.find((t) => t.status === 'running');

  if (floatWin && !floatWin.closed) {
    floatWin.focus();
    renderFloatingTimer();
    return;
  }
  if (!running) {
    toast('当前没有正在计时的任务，先点「开始」再打开悬浮窗', 'warn');
    return;
  }
  floatTaskId = running.id;

  // 不支持置顶小窗、或者置顶小窗被浏览器拒绝时，退回普通弹窗
  const openPopup = () => {
    const win = window.open(
      '',
      'planner-floating-timer',
      'width=304,height=208,menubar=no,toolbar=no,location=no,status=no,scrollbars=no,resizable=yes'
    );
    if (!win) {
      toast('浏览器拦截了弹窗，请允许本站弹出窗口后重试', 'warn');
      return;
    }
    setupFloatingTimer(win);
  };

  if ('documentPictureInPicture' in window) {
    documentPictureInPicture
      .requestWindow({ width: 304, height: 208 })
      .then(setupFloatingTimer)
      .catch(() => {
        // 以前这里只弹一个 toast 就结束了，等于「悬浮窗按钮坏了」。
        // 实际上置顶小窗失败是很常见的（非用户手势、被策略禁用），
        // 只要退回普通弹窗，功能依然是可用的。
        openPopup();
      });
    return;
  }

  openPopup();
}

/* ---------- 悬浮窗：样式与结构 ----------
   小窗是一个独立 document，拿不到主页面的样式表，
   所以配色靠「把 CSS 变量复制过去」这一招。变量名必须和 app.css 同步 ——
   上一版还留着 --primary / --text 这套早已改名的变量，
   结果「完成」按钮取不到 --primary，白字压在透明底上，整窗基本不可读。 */

// 圆角跟着主题走：玻璃是胶囊，网格与海报是方角
function floatRadius() {
  const r = getComputedStyle(document.documentElement).getPropertyValue('--r').trim();
  return (r === '0' || r === '0px') ? '0' : '999px';
}

const FLOAT_TOKENS = [
  '--bg', '--txt', '--txt-2', '--txt-3',
  '--accent', '--accent-ink', '--accent-soft',
  '--edge', '--edge-2', '--warn', '--ok', '--font',
];

const FLOAT_CSS = [
  ':root { color-scheme: dark; }',
  '* { box-sizing: border-box; }',
  'html, body { height: 100%; margin: 0; }',
  'body {',
  '  display: flex; align-items: center; justify-content: center;',
  '  padding: 14px 16px; overflow: hidden;',
  '  background: var(--bg, #08090c);',
  '  color: var(--txt, #eceef2);',
  '  font-family: var(--font, "Segoe UI", "Microsoft YaHei", sans-serif);',
  '  -webkit-font-smoothing: antialiased;',
  '  user-select: none; -webkit-user-select: none;',
  '}',
  '.ft { width: 100%; display: flex; flex-direction: column; gap: 9px; }',

  '.ft-head { display: flex; align-items: center; gap: 8px; min-width: 0; }',
  '.ft-dot {',
  '  width: 6px; height: 6px; flex: none; border-radius: 50%;',
  '  background: var(--accent, #c9ff4a);',
  '  box-shadow: 0 0 10px var(--accent, #c9ff4a);',
  '}',
  '.ft-dot.is-idle { background: var(--warn, #ffc247); box-shadow: none; }',
  '.ft-name {',
  '  flex: 1; min-width: 0; font-size: 12px; letter-spacing: .01em;',
  '  color: var(--txt-2, #a8afba);',
  '  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;',
  '}',
  '.ft-state {',
  '  flex: none; font-size: 10px; letter-spacing: .14em;',
  '  color: var(--txt-3, #767e8b);',
  '}',

  '.ft-time {',
  '  font-size: 42px; font-weight: 200; line-height: 1;',
  '  letter-spacing: -.045em; font-variant-numeric: tabular-nums;',
  '  color: var(--txt, #eceef2);',
  '  transition: color .2s ease;',
  '}',
  '.ft-time.is-idle { color: var(--txt-2, #a8afba); }',

  '.ft-bar {',
  '  height: 3px; border-radius: 999px;',
  '  background: var(--edge, rgba(255,255,255,.12));',
  '  overflow: hidden;',
  '}',
  '.ft-bar[hidden] { display: none; }',
  '.ft-bar-fill {',
  '  display: block; height: 100%; width: 0; border-radius: inherit;',
  '  background: var(--accent, #c9ff4a);',
  '  box-shadow: 0 0 10px var(--accent-soft, rgba(201,255,74,.35));',
  '  transition: width .5s linear, background .2s ease;',
  '}',
  '.ft-bar-fill.is-over { background: var(--warn, #ffc247); }',

  '.ft-meta {',
  '  display: flex; align-items: baseline; justify-content: space-between; gap: 10px;',
  '  font-size: 11px; color: var(--txt-3, #767e8b);',
  '  font-variant-numeric: tabular-nums;',
  '}',
  '.ft-meta b { color: var(--txt-2, #a8afba); font-weight: 500; }',

  '.ft-actions { display: flex; gap: 8px; margin-top: 1px; }',
  '.ft-actions button {',
  '  flex: 1; padding: 8px 0;',
  '  font: inherit; font-size: 12px; font-weight: 500;',
  '  color: var(--txt, #eceef2);',
  '  background: transparent;',
  '  border: 1px solid var(--edge-2, rgba(255,255,255,.2));',
  '  border-radius: var(--ft-radius, 999px);',
  '  cursor: pointer;',
  '  transition: background .16s ease, color .16s ease, border-color .16s ease;',
  '}',
  '.ft-actions button:hover:not(:disabled) { background: var(--edge, rgba(255,255,255,.1)); }',
  '.ft-actions button:disabled { opacity: .45; cursor: default; }',
  '#ft-done {',
  '  background: var(--accent, #c9ff4a);',
  '  border-color: var(--accent, #c9ff4a);',
  '  color: var(--accent-ink, #080a04);',
  '  font-weight: 650;',
  '}',
  '#ft-done:hover:not(:disabled) { background: var(--accent, #c9ff4a); filter: brightness(1.06); }',
].join('\n');

const FLOAT_HTML = [
  '<div class="ft">',
  '  <div class="ft-head">',
  '    <span class="ft-dot" id="ft-dot"></span>',
  '    <span class="ft-name" id="ft-name">载入中…</span>',
  '    <span class="ft-state" id="ft-state">计时中</span>',
  '  </div>',
  '  <div class="ft-time" id="ft-time">--:--</div>',
  '  <div class="ft-bar" id="ft-bar" hidden><span class="ft-bar-fill" id="ft-bar-fill"></span></div>',
  '  <div class="ft-meta"><span id="ft-meta">—</span><span id="ft-meta-right"></span></div>',
  '  <div class="ft-actions">',
  '    <button type="button" id="ft-toggle">暂停</button>',
  '    <button type="button" id="ft-done">完成</button>',
  '  </div>',
  '</div>',
].join('');

let floatTimer = null;      // 小窗自己的心跳（挂在它的 window 上）
let floatRepaint = null;    // 小窗内的重绘函数，供主页面触发

/** 主题一变就把新的配色推给小窗（它用的是复制过去的变量副本）。 */
function syncFloatTheme() {
  if (!floatWin || floatWin.closed) return;
  const doc = floatWin.document;
  if (!doc || !doc.documentElement) return;

  const cs = getComputedStyle(document.documentElement);
  FLOAT_TOKENS.forEach((key) => {
    doc.documentElement.style.setProperty(key, cs.getPropertyValue(key).trim());
  });
  doc.documentElement.style.setProperty('--ft-radius', floatRadius());
  doc.documentElement.setAttribute('data-theme', document.documentElement.getAttribute('data-theme') || 'glass');
}

function setupFloatingTimer(win) {
  floatWin = win;
  const doc = win.document;

  doc.head.innerHTML = '<meta charset="utf-8"><title>计时 · 时间规划</title>';
  const style = doc.createElement('style');
  style.textContent = FLOAT_CSS;
  doc.head.appendChild(style);
  doc.body.innerHTML = FLOAT_HTML;

  const pick = (id) => doc.getElementById(id);
  const timeEl = pick('ft-time');
  const nameEl = pick('ft-name');
  const stateEl = pick('ft-state');
  const metaEl = pick('ft-meta');
  const metaRightEl = pick('ft-meta-right');
  const barEl = pick('ft-bar');
  const fillEl = pick('ft-bar-fill');
  const dotEl = pick('ft-dot');
  const toggleEl = pick('ft-toggle');
  const doneEl = pick('ft-done');

  const paint = () => {
    const task = floatingTask();
    if (!task) {
      // 任务被完成或删除，小窗没有必要继续挂着
      floatWin = null;
      try { win.close(); } catch (e) { /* 已被用户关掉 */ }
      return;
    }

    const secs = liveElapsedSeconds(task);
    const running = task.status === 'running';

    timeEl.textContent = fmtClockSpan(secs);
    timeEl.classList.toggle('is-idle', !running);
    dotEl.className = running ? 'ft-dot' : 'ft-dot is-idle';
    stateEl.textContent = running ? '计时中' : (task.status === 'paused' ? '已暂停' : '未开始');
    toggleEl.textContent = running ? '暂停' : '继续';

    nameEl.textContent = task.name;
    nameEl.title = task.name;

    const est = Number(task.estimateMin) || 0;
    if (est > 0) {
      const target = est * 60;
      barEl.hidden = false;
      fillEl.style.width = Math.min(100, (secs / target) * 100).toFixed(2) + '%';
      fillEl.classList.toggle('is-over', secs > target);
      metaEl.innerHTML = secs > target
        ? `超出 <b>${escapeHtml(fmtClockSpan(secs - target))}</b>`
        : `剩余 <b>${escapeHtml(fmtClockSpan(target - secs))}</b>`;
      metaRightEl.textContent = `预估 ${fmtMinutes(est)}`;
    } else {
      barEl.hidden = true;
      metaEl.textContent = running ? '未设预估时长' : '已暂停';
      metaRightEl.textContent = '';
    }
  };

  toggleEl.addEventListener('click', async () => {
    const task = floatingTask();
    if (!task) return;
    toggleEl.disabled = true;
    await handleTaskAction(task.status === 'running' ? 'pause' : 'resume', task.id);
    toggleEl.disabled = false;
    paint();
  });

  doneEl.addEventListener('click', async () => {
    const task = floatingTask();
    if (!task) return;
    doneEl.disabled = true;
    await handleTaskAction('complete', task.id);
    floatWin = null;
    try { win.close(); } catch (e) { /* 忽略 */ }
  });

  win.addEventListener('pagehide', () => {
    if (floatTimer !== null) {
      try { win.clearInterval(floatTimer); } catch (e) { /* 忽略 */ }
      floatTimer = null;
    }
    floatRepaint = null;
    if (floatWin === win) floatWin = null;
  });

  syncFloatTheme();

  /* 关键修复：让**小窗自己**走表。
     以前是靠主页面每秒重绘去推动它，可主标签页一旦切到后台，
     浏览器就把 setInterval 节流到一分钟一次 —— 悬浮窗正好在那一刻"冻住"，
     而它存在的意义恰恰是「主页面不在前台时也能看见」。
     这个定时器挂在 win 上，只要小窗本身可见就不会被节流。 */
  if (floatTimer !== null) {
    try { win.clearInterval(floatTimer); } catch (e) { /* 忽略 */ }
  }
  floatTimer = win.setInterval(paint, 500);

  floatRepaint = paint;
  paint();
}

/** 刷新悬浮窗（主页面每秒 / 每次操作后调用；小窗自己也在走表）。 */
function renderFloatingTimer() {
  if (!floatWin || floatWin.closed) return;
  if (typeof floatRepaint === 'function') floatRepaint();
}

/* 主题切换后同步小窗配色 */
if (typeof MutationObserver === 'function') {
  new MutationObserver(() => {
    syncFloatTheme();
    renderFloatingTimer();
  }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
}

/** 任务行内操作。 */
async function handleTaskAction(act, id) {
  const task = state.tasks.find((t) => String(t.id) === String(id));
  if (!task) return;

  switch (act) {
    case 'start':
      await attempt(() => API.start(id), '已开始计时');
      break;
    case 'pause':
      await attempt(() => API.pause(id), '已暂停计时');
      break;
    case 'resume':
      await attempt(() => API.resume(id), '已继续计时');
      break;
    case 'complete':
      await attempt(() => API.complete(id), '已标记为完成');
      break;
    case 'undone':
      await attempt(() => API.undone(id), '已标记为未完成');
      break;
    case 'edit':
      openTaskEditModal(task);
      return;
    case 'delete': {
      const yes = await uiConfirm(`确定删除任务「${task.name}」吗？\n删除后无法恢复。`, {
        title: '删除任务',
        okText: '删除',
        danger: true,
      });
      if (!yes) return;
      await attempt(() => API.deleteTask(id), '任务已删除');
      break;
    }
    default:
      return;
  }
  await loadToday();
}

/* ---------- 新增 / 编辑任务弹窗 ---------- */

function openTaskCreateModal() {
  openModal({
    title: '添加任务',
    body:
      `<label class="field">` +
        `<span class="field-label">任务名称</span>` +
        `<input type="text" id="nt-name" placeholder="例如：复习高数第三章" maxlength="120">` +
      `</label>` +
      `<label class="field">` +
        `<span class="field-label">标签</span>` +
        `<select id="nt-tag">${tagOptionsHtml(null, '不设置标签')}</select>` +
      `</label>` +
      `<label class="field">` +
        `<span class="field-label">预估耗时（分钟）</span>` +
        `<input type="number" id="nt-estimate" min="1" max="1440" step="5" value="30">` +
      `</label>`,
    footer:
      `<button type="button" class="btn" data-act="cancel">取消</button>` +
      `<button type="button" class="btn btn-primary" data-act="ok">添加</button>`,
  });

  const nameInput = qs('#nt-name');
  if (nameInput) nameInput.focus();

  qs('.modal [data-act="cancel"]').addEventListener('click', closeModal);
  qs('.modal [data-act="ok"]').addEventListener('click', async () => {
    const name = qs('#nt-name').value.trim();
    const tagValue = qs('#nt-tag').value;
    const estimateMin = Number(qs('#nt-estimate').value);

    if (!name) {
      toast('请填写任务名称', 'warn');
      return;
    }
    if (!Number.isFinite(estimateMin) || estimateMin <= 0) {
      toast('预估耗时必须是大于 0 的分钟数', 'warn');
      return;
    }

    const body = { planDate: state.date, name, estimateMin: Math.round(estimateMin) };
    if (tagValue) body.tagId = Number(tagValue);

    const r = await attempt(() => API.createTask(body), '任务已添加');
    if (r.ok) {
      closeModal();
      await loadToday();
    }
  });
}

function openTaskEditModal(task) {
  const statusOptions = ['pending', 'running', 'paused', 'done', 'undone']
    .map((s) => `<option value="${escapeHtml(s)}"${task.status === s ? ' selected' : ''}>${escapeHtml(STATUS_TEXT[s])}</option>`)
    .join('');

  const userEstimate = task.userEstimateMin === null || task.userEstimateMin === undefined
    ? ''
    : String(task.userEstimateMin);

  openModal({
    title: `编辑任务 #${task.id}`,
    body:
      `<label class="field">` +
        `<span class="field-label">任务名称</span>` +
        `<input type="text" id="et-name" maxlength="120" value="${escapeHtml(task.name)}">` +
      `</label>` +
      `<div class="form-grid">` +
        `<label class="field">` +
          `<span class="field-label">标签</span>` +
          `<select id="et-tag">${tagOptionsHtml(task.tagId, '不设置标签')}</select>` +
        `</label>` +
        `<label class="field">` +
          `<span class="field-label">状态</span>` +
          `<select id="et-status">${statusOptions}</select>` +
        `</label>` +
        `<label class="field">` +
          `<span class="field-label">AI 预估（分钟）</span>` +
          `<input type="number" id="et-ai" min="0" max="1440" step="5" value="${escapeHtml(String(task.aiEstimateMin ?? 0))}">` +
        `</label>` +
        `<label class="field">` +
          `<span class="field-label">用户预估（分钟）<em>留空 = 清除</em></span>` +
          `<input type="number" id="et-user" min="0" max="1440" step="5" value="${escapeHtml(userEstimate)}" placeholder="留空则回退到 AI 预估">` +
        `</label>` +
        `<label class="field">` +
          `<span class="field-label">顺序</span>` +
          `<input type="number" id="et-order" min="0" step="1" value="${escapeHtml(String(task.orderIndex ?? 0))}">` +
        `</label>` +
        `<label class="field">` +
          `<span class="field-label">实际耗时（分钟）<em>手动修正</em></span>` +
          `<input type="number" id="et-actual" min="0" max="1440" step="1" value="${escapeHtml(String(Math.round((Number(task.elapsedSeconds) || 0) / 60)))}">` +
        `</label>` +
      `</div>` +
      `<p class="hint">实际耗时以秒为单位提交给后端；这里按分钟填写，保存时会自动换算。计时中的任务会被先结算再覆写。</p>`,
    footer:
      `<button type="button" class="btn" data-act="cancel">取消</button>` +
      `<button type="button" class="btn btn-primary" data-act="ok">保存修改</button>`,
  });

  qs('.modal [data-act="cancel"]').addEventListener('click', closeModal);
  qs('.modal [data-act="ok"]').addEventListener('click', async () => {
    const name = qs('#et-name').value.trim();
    if (!name) {
      toast('任务名称不能为空', 'warn');
      return;
    }

    const patch = {
      name,
      status: qs('#et-status').value,
      aiEstimateMin: Math.max(0, Math.round(Number(qs('#et-ai').value) || 0)),
      orderIndex: Math.max(0, Math.round(Number(qs('#et-order').value) || 0)),
    };

    const tagValue = qs('#et-tag').value;
    if (tagValue) patch.tagId = Number(tagValue);
    else patch.clearTag = true; // 置空标签要用 clearTag 显式表达

    const userRaw = qs('#et-user').value.trim();
    if (userRaw === '') patch.clearUserEstimate = true;
    else patch.userEstimateMin = Math.max(0, Math.round(Number(userRaw) || 0));

    const r1 = await attempt(() => API.updateTask(task.id, patch), '任务已更新');

    // 实际耗时按分钟输入，换算成秒提交
    const actualRaw = qs('#et-actual').value.trim();
    const actualMin = Number(actualRaw);
    if (r1.ok && actualRaw !== '' && Number.isFinite(actualMin) && actualMin >= 0) {
      const seconds = Math.round(actualMin * 60);
      if (seconds !== Math.round((Number(task.elapsedSeconds) || 0))) {
        await attempt(() => API.setActual(task.id, seconds), '实际耗时已修正');
      }
    }

    if (r1.ok) {
      closeModal();
      await loadToday();
    }
  });
}

/* ---------- 拖拽排序 ---------- */

const taskListEl = qs('#task-list');

taskListEl.addEventListener('click', (e) => {
  // 空状态里的引导按钮：让首屏的空白也能直接通向下一步
  const quick = e.target.closest('button[data-empty]');
  if (quick) {
    if (quick.dataset.empty === 'generate') switchView('generate');
    else if (quick.dataset.empty === 'add') openTaskCreateModal();
    return;
  }

  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  handleTaskAction(btn.dataset.act, btn.dataset.id);
});

taskListEl.addEventListener('dragstart', (e) => {
  const card = e.target.closest('.task-card');
  if (!card) return;
  state.dragId = card.dataset.id;
  card.classList.add('dragging');
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', state.dragId);
  }
});

taskListEl.addEventListener('dragover', (e) => {
  if (state.dragId === null) return;
  const card = e.target.closest('.task-card');
  if (!card) return;
  e.preventDefault();
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';

  const rect = card.getBoundingClientRect();
  const above = e.clientY < rect.top + rect.height / 2;
  qsa('.task-card', taskListEl).forEach((el) => el.classList.remove('drop-above', 'drop-below'));
  if (card.dataset.id !== state.dragId) card.classList.add(above ? 'drop-above' : 'drop-below');
});

taskListEl.addEventListener('dragleave', (e) => {
  // 只有真正离开整个列表时才清除插入位置指示，避免划过子元素时闪烁
  if (taskListEl.contains(e.relatedTarget)) return;
  qsa('.task-card', taskListEl).forEach((el) => el.classList.remove('drop-above', 'drop-below'));
});

taskListEl.addEventListener('drop', async (e) => {
  const card = e.target.closest('.task-card');
  const dragId = state.dragId;
  clearDragState();
  if (!card || dragId === null) return;
  e.preventDefault();

  const targetId = card.dataset.id;
  if (targetId === dragId) return;

  // 先把拖拽项从当前顺序移除，再插入目标位置，得到新的本地顺序
  const ids = state.tasks.map((t) => String(t.id));
  const from = ids.indexOf(String(dragId));
  if (from < 0) return;
  const moved = state.tasks.splice(from, 1)[0];

  const rect = card.getBoundingClientRect();
  const above = e.clientY < rect.top + rect.height / 2;
  let to = state.tasks.findIndex((t) => String(t.id) === String(targetId));
  if (to < 0) {
    state.tasks.splice(from, 0, moved);
    return;
  }
  if (!above) to += 1;
  state.tasks.splice(to, 0, moved);

  renderTaskList();

  // 只提交顺序：必须包含当日全部任务（含已完成），否则后端会拒绝
  const orderedIds = state.tasks.map((t) => t.id);
  const r = await attempt(() => API.reorder(state.date, orderedIds), '顺序已更新');
  if (!r.ok) await loadToday(); // 失败则回到服务端的真实顺序
});

taskListEl.addEventListener('dragend', clearDragState);

function clearDragState() {
  state.dragId = null;
  qsa('.task-card', taskListEl).forEach((el) =>
    el.classList.remove('dragging', 'drop-above', 'drop-below'));
}

/* ---------- 今日视图的按钮 ---------- */

qs('#btn-add-task').addEventListener('click', openTaskCreateModal);

// 欢迎区的引导按钮，与空状态里的两个出口行为一致
qs('#today-hero').addEventListener('click', (e) => {
  const btn = e.target.closest('button[data-hero]');
  if (!btn) return;
  if (btn.dataset.hero === 'generate') switchView('generate');
  else if (btn.dataset.hero === 'add') openTaskCreateModal();
});

qs('#btn-float-timer').addEventListener('click', openFloatingTimer);

/**
 * 首屏视差：鼠标在欢迎区里移动时，进度环朝反方向轻微偏移（最多 7px）。
 *
 * 这只是景深暗示，不承载任何信息 —— 所以关掉它就什么都没有损失：
 * 触屏（没有 hover）和「减弱动效」偏好下直接不绑定。
 */
(function initHeroParallax() {
  const hero = qs('#today-hero');
  const ring = qs('.hero-ring');
  if (!hero || !ring) return;
  if (window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
  if (window.matchMedia && window.matchMedia('(hover: none)').matches) return;

  const MAX = 7;
  let frame = 0;

  hero.addEventListener('mousemove', (e) => {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      const r = hero.getBoundingClientRect();
      if (!r.width || !r.height) return;
      const dx = (e.clientX - r.left) / r.width - 0.5;
      const dy = (e.clientY - r.top) / r.height - 0.5;
      ring.style.transform = `translate3d(${(-dx * MAX).toFixed(2)}px, ${(-dy * MAX).toFixed(2)}px, 0)`;
    });
  });

  hero.addEventListener('mouseleave', () => { ring.style.transform = ''; });
})();

qs('#btn-align').addEventListener('click', async () => {
  const r = await attempt(() => API.align(state.date), '已按当前北京时间重新排列并刷新时间戳');
  if (r.ok) await loadToday();
});

/* ==========================================================================
 * 八、智能生成视图
 * ========================================================================== */

qs('#btn-generate').addEventListener('click', async () => {
  if (state.generating) return;

  const input = qs('#gen-input').value.trim();
  const constraint = qs('#gen-constraint').value.trim();
  const replace = qs('#gen-replace').checked;

  if (!input) {
    toast('请先用一句话描述今天要做的事', 'warn');
    return;
  }

  const btn = qs('#btn-generate');
  const status = qs('#gen-status');
  state.generating = true;
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> 生成中…';
  status.textContent = 'AI 正在排程，通常需要 10～30 秒，请勿关闭页面。';
  qs('#gen-result').innerHTML = '';

  const r = await attempt(() => API.generate({
    planDate: state.date,
    input,
    constraint,
    replace,
  }));

  state.generating = false;
  btn.disabled = false;
  btn.textContent = '生成计划';
  status.textContent = '';

  if (!r.ok) {
    renderGenerateResult(null, (r.error && r.error.message) || '生成失败');
    return;
  }

  renderGenerateResult(r.data, null);
  await loadToday();
});

function renderGenerateResult(data, errorMsg) {
  const host = qs('#gen-result');

  if (errorMsg) {
    host.innerHTML =
      `<div class="alert alert-danger">` +
        `<div class="alert-body"><div class="alert-title">生成失败</div><p>${escapeHtml(errorMsg)}</p></div>` +
      `</div>`;
    return;
  }

  const d = data || {};
  const schedule = d.schedule || {};
  const blocks = [];

  if (d.source === 'local') {
    blocks.push(
      `<div class="alert alert-warn">` +
        `<div class="alert-body">` +
          `<div class="alert-title">当前为本地规则生成（未配置 AI 或 AI 调用失败）</div>` +
          `<p>计划已按你的任务列表与可用时间块排出；配置 AI 后可获得更贴合语意的拆解与建议。</p>` +
        `</div>` +
      `</div>`
    );
  } else if (d.source === 'ai') {
    blocks.push(
      `<div class="alert alert-ok">` +
        `<div class="alert-body"><div class="alert-title">已由 AI 生成计划</div></div>` +
      `</div>`
    );
  }

  if (d.notice) {
    blocks.push(
      `<div class="alert alert-warn">` +
        `<div class="alert-body"><div class="alert-title">排程提示</div><p>${escapeHtml(d.notice)}</p></div>` +
      `</div>`
    );
  }

  if (d.advice) {
    blocks.push(
      `<div class="alert">` +
        `<div class="alert-body"><div class="alert-title">规划建议</div><p>${escapeHtml(d.advice)}</p></div>` +
      `</div>`
    );
  }

  const overflowCount = Array.isArray(schedule.overflow) ? schedule.overflow.length : 0;
  const stats = [
    ['新增任务', `${Number(d.createdCount) || 0} 条`],
    ['成功排入时间线', `${Number(schedule.placed) || 0} 条`],
    ['超出截止时间', `${overflowCount} 条`],
    ['当日可用时长', fmtMinutes(schedule.availableMin)],
    ['任务总需求', fmtMinutes(schedule.demandMin)],
  ];

  blocks.push(
    `<div class="card">` +
      `<div class="card-head"><h2>排程结果</h2></div>` +
      `<div class="stat-grid">${stats.map(([label, value]) =>
        `<div class="stat-item"><span class="stat-label">${escapeHtml(label)}</span>` +
        `<span class="stat-value">${escapeHtml(value)}</span></div>`).join('')}</div>` +
      (schedule.overloaded
        ? `<div class="alert alert-warn" style="margin-top:14px">` +
            `<div class="alert-body"><div class="alert-title">负载过高</div>` +
            `<p>任务需求超过当日可用时长，${escapeHtml(String(overflowCount))} 条任务未能排入，已进入未完成池，可在「未完成池」中承接或手动调整。</p></div>` +
          `</div>`
        : '') +
    `</div>`
  );

  host.innerHTML = blocks.join('');
}

/* ==========================================================================
 * 九、未完成任务池
 * ========================================================================== */

async function loadPool() {
  const host = qs('#pool-list');
  host.innerHTML = '<div class="empty">正在载入…</div>';

  const r = await attempt(() => API.pool(state.date));
  if (!r.ok) {
    host.innerHTML = '<div class="empty"><strong>载入失败</strong>请稍后重试。</div>';
    return;
  }

  const items = Array.isArray(r.data) ? r.data : [];
  if (!items.length) {
    host.innerHTML =
      '<div class="empty"><strong>太棒了，没有积压</strong>历史未完成的任务都会汇集到这里。</div>';
    return;
  }

  host.innerHTML = items.map((it) => {
    const tags = [tagChipHtml(it.tagName, it.tagColor)];
    if (it.adopted) tags.push('<span class="badge badge-soft">已承接</span>');
    if (it.isOvertime) tags.push('<span class="badge badge-warn">超时未确认</span>');

    return (
      `<div class="item-row">` +
        `<div class="item-main">` +
          `<div class="item-title"><span>${escapeHtml(it.name)}</span>${tags.join('')}</div>` +
          `<div class="item-sub">` +
            `原计划日期 ${escapeHtml(it.planDate)} · 预估 ${escapeHtml(fmtMinutes(it.estimateMin))} · ` +
            `已投入 ${escapeHtml(fmtSeconds(it.actualSeconds))}` +
          `</div>` +
        `</div>` +
        `<div class="item-actions">` +
          `<button type="button" class="btn btn-sm btn-primary" data-pool-act="adopt" data-id="${escapeHtml(String(it.taskId))}">加入今日计划</button>` +
          `<button type="button" class="btn btn-sm btn-ghost" data-pool-act="delete" data-id="${escapeHtml(String(it.taskId))}">删除</button>` +
        `</div>` +
      `</div>`
    );
  }).join('');
}

qs('#pool-list').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-pool-act]');
  if (!btn) return;
  const id = Number(btn.dataset.id);
  const act = btn.dataset.poolAct;

  if (act === 'adopt') {
    const r = await attempt(() => API.adopt(id, state.date), '已加入今日计划');
    if (r.ok) {
      await loadPool();
      await loadToday();
    }
    return;
  }

  if (act === 'delete') {
    const yes = await uiConfirm('确定删除这条历史未完成任务吗？删除后无法恢复。', {
      title: '删除任务',
      okText: '删除',
      danger: true,
    });
    if (!yes) return;
    const r = await attempt(() => API.deleteTask(id), '任务已删除');
    if (r.ok) await loadPool();
  }
});

qs('#btn-pool-refresh').addEventListener('click', loadPool);

/* ==========================================================================
 * 十、周期任务
 * ========================================================================== */

async function loadRecurring() {
  const host = qs('#rec-list');
  host.innerHTML = '<div class="empty">正在载入…</div>';

  if (!state.tags.length) await loadTags();

  const r = await attempt(() => API.recurring());
  if (!r.ok) {
    host.innerHTML = '<div class="empty"><strong>载入失败</strong>请稍后重试。</div>';
    return;
  }

  const rows = Array.isArray(r.data) ? r.data : [];
  if (!rows.length) {
    host.innerHTML =
      '<div class="empty"><strong>还没有周期任务</strong>添加后，每天打开页面都会自动排进当日计划。</div>';
    return;
  }

  host.innerHTML = rows.map((row) => {
    const tag = findTag(row.tagId);
    const chips = [tagChipHtml(tag ? tag.name : '', tag ? tag.color : null)];
    chips.push(`<span class="badge badge-soft">${escapeHtml(FREQ_TEXT[row.frequency] || row.frequency || '')}</span>`);
    if (!row.enabled) chips.push('<span class="badge badge-pending">已停用</span>');

    const weekdayText = row.frequency === 'weekly'
      ? ` · 星期：${escapeHtml(weekdayListText(row.weekdays))}`
      : '';
    const range = `${escapeHtml(row.effectiveFrom)} 起${row.effectiveTo ? ` 至 ${escapeHtml(row.effectiveTo)}` : '（无限期）'}`;

    return (
      `<div class="item-row">` +
        `<div class="item-main">` +
          `<div class="item-title"><span>${escapeHtml(row.name)}</span>${chips.join('')}</div>` +
          `<div class="item-sub">单次 ${escapeHtml(fmtMinutes(row.durationMin))} · ${range}${weekdayText}</div>` +
        `</div>` +
        `<div class="item-actions">` +
          `<button type="button" class="btn btn-sm" data-rec-act="edit" data-id="${escapeHtml(String(row.id))}">编辑</button>` +
          `<button type="button" class="btn btn-sm btn-ghost" data-rec-act="delete" data-id="${escapeHtml(String(row.id))}">删除</button>` +
        `</div>` +
      `</div>`
    );
  }).join('');
}

/** "1,3,5" -> "周一、周三、周五"。 */
function weekdayListText(text) {
  const list = String(text || '')
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '')
    .map((s) => WEEKDAY_NAMES[Number(s)] || s);
  return list.length ? list.join('、') : '未指定';
}

/** 频率切换时显示/隐藏星期多选。 */
function syncRecurringWeekdayVisibility() {
  const isWeekly = qs('#rec-freq').value === 'weekly';
  qs('#rec-weekdays-field').hidden = !isWeekly;
}
qs('#rec-freq').addEventListener('change', syncRecurringWeekdayVisibility);

function readRecurringWeekdays() {
  return qsa('#rec-weekdays input:checked').map((el) => el.value).join(',');
}

function setRecurringWeekdays(text) {
  const set = new Set(String(text || '').split(',').map((s) => s.trim()).filter(Boolean));
  qsa('#rec-weekdays input').forEach((el) => {
    el.checked = set.has(el.value);
  });
}

function resetRecurringForm() {
  state.editingRecurringId = null;
  qs('#rec-form-title').textContent = '新增周期任务';
  qs('#rec-name').value = '';
  qs('#rec-tag').value = '';
  qs('#rec-duration').value = '30';
  qs('#rec-freq').value = 'daily';
  qs('#rec-from').value = state.date;
  qs('#rec-to').value = '';
  qs('#rec-enabled').checked = true;
  setRecurringWeekdays('');
  qs('#btn-rec-cancel').hidden = true;
  syncRecurringWeekdayVisibility();
}

qs('#btn-rec-save').addEventListener('click', async () => {
  const name = qs('#rec-name').value.trim();
  const frequency = qs('#rec-freq').value;
  const durationMin = Math.round(Number(qs('#rec-duration').value) || 0);
  const effectiveFrom = qs('#rec-from').value;
  const effectiveTo = qs('#rec-to').value;
  const weekdays = frequency === 'weekly' ? readRecurringWeekdays() : '';
  const tagValue = qs('#rec-tag').value;

  if (!name) {
    toast('请填写任务内容', 'warn');
    return;
  }
  if (!effectiveFrom) {
    toast('请选择生效日期', 'warn');
    return;
  }
  if (!Number.isFinite(durationMin) || durationMin < 0 || durationMin > 1440) {
    toast('单次时长必须在 0 到 1440 分钟之间', 'warn');
    return;
  }
  if (frequency === 'weekly' && !weekdays) {
    toast('按星期循环时至少要选择一个星期', 'warn');
    return;
  }
  if (effectiveTo && effectiveTo < effectiveFrom) {
    toast('结束日期不能早于生效日期', 'warn');
    return;
  }

  // 后端 PUT 是全量更新，因此两种操作提交相同的完整字段
  const body = {
    name,
    durationMin,
    frequency,
    weekdays,
    effectiveFrom,
    effectiveTo: effectiveTo || null,
    enabled: qs('#rec-enabled').checked,
  };
  if (tagValue) body.tagId = Number(tagValue);

  const editingId = state.editingRecurringId;
  const r = editingId
    ? await attempt(() => API.updateRecurring(editingId, body), '周期任务已更新')
    : await attempt(() => API.createRecurring(body), '周期任务已创建');

  if (r.ok) {
    resetRecurringForm();
    await loadRecurring();
    await materializeRecurring();
    await loadToday();
  }
});

qs('#btn-rec-cancel').addEventListener('click', resetRecurringForm);

qs('#rec-list').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-rec-act]');
  if (!btn) return;
  const id = Number(btn.dataset.id);

  if (btn.dataset.recAct === 'delete') {
    const yes = await uiConfirm('确定删除这条周期任务吗？已生成的当日任务不会受影响。', {
      title: '删除周期任务',
      okText: '删除',
      danger: true,
    });
    if (!yes) return;
    const r = await attempt(() => API.deleteRecurring(id), '周期任务已删除');
    if (r.ok) {
      if (String(state.editingRecurringId) === String(id)) resetRecurringForm();
      await loadRecurring();
    }
    return;
  }

  // 编辑：把该行数据回填到表单
  const r = await attempt(() => API.recurring());
  if (!r.ok) return;
  const row = (Array.isArray(r.data) ? r.data : []).find((x) => String(x.id) === String(id));
  if (!row) {
    toast('未找到该周期任务，可能已被删除', 'error');
    return;
  }

  state.editingRecurringId = row.id;
  qs('#rec-form-title').textContent = `编辑周期任务 #${row.id}`;
  qs('#rec-name').value = row.name || '';
  qs('#rec-tag').value = row.tagId === null || row.tagId === undefined ? '' : String(row.tagId);
  qs('#rec-duration').value = String(row.durationMin ?? 30);
  qs('#rec-freq').value = row.frequency || 'daily';
  qs('#rec-from').value = row.effectiveFrom || state.date;
  qs('#rec-to').value = row.effectiveTo || '';
  qs('#rec-enabled').checked = row.enabled !== false;
  setRecurringWeekdays(row.weekdays);
  qs('#btn-rec-cancel').hidden = false;
  syncRecurringWeekdayVisibility();
  qs('#rec-name').focus();
});

/* ---------- 周期任务物化与冲突处理 ---------- */

async function materializeRecurring() {
  const r = await attempt(() => API.materialize(state.date));
  if (!r.ok) return;

  const out = r.data || {};
  const conflicts = Array.isArray(out.conflicts) ? out.conflicts : [];

  if (Number(out.created) > 0) {
    toast(`已自动排入 ${Number(out.created)} 条周期任务`, 'success');
  }
  if (conflicts.length) {
    await showConflictModal(conflicts, state.date);
  }
}

/**
 * 冲突弹窗：逐条或统一选择「覆盖 / 跳过」。
 * 不使用原生 confirm，选择结果通过 /api/recurring/conflict 提交。
 */
function showConflictModal(conflicts, planDate) {
  return new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      resolve();
    };

    // 默认「跳过」：不动用户已有任务更安全
    const choices = new Map();
    conflicts.forEach((c) => choices.set(String(c.recurringId), 'skip'));

    const rowsHtml = conflicts.map((c) => {
      const recid = String(c.recurringId);
      return (
        `<div class="item-row" data-conflict="${escapeHtml(recid)}">` +
          `<div class="item-main">` +
            `<div class="item-title">${escapeHtml(c.name || '未命名周期任务')}</div>` +
            `<div class="item-sub">周期任务 #${escapeHtml(recid)} · 时长 ${escapeHtml(fmtMinutes(c.durationMin))} · ` +
            `与已有任务 #${escapeHtml(String(c.existingTaskId ?? ''))} 冲突</div>` +
          `</div>` +
          `<div class="item-actions">` +
            `<button type="button" class="btn btn-sm" data-pick="overwrite" data-recid="${escapeHtml(recid)}">覆盖</button>` +
            `<button type="button" class="btn btn-sm" data-pick="skip" data-recid="${escapeHtml(recid)}">跳过</button>` +
          `</div>` +
        `</div>`
      );
    }).join('');

    const modal = openModal({
      title: '周期任务与已有任务冲突',
      body:
        `<p class="modal-note">今天已经存在同名任务。<b>覆盖</b>会用周期任务的时长重写已有任务的预估，` +
        `<b>跳过</b>则保留已有任务不动。可以逐条选择，也可以一键统一处理。</p>` +
        `<div class="row row-wrap" style="margin:14px 0">` +
          `<button type="button" class="btn btn-sm" data-bulk="overwrite">全部覆盖</button>` +
          `<button type="button" class="btn btn-sm" data-bulk="skip">全部跳过</button>` +
        `</div>` +
        `<div class="stack">${rowsHtml}</div>`,
      footer:
        `<button type="button" class="btn" data-act="cancel">稍后处理</button>` +
        `<button type="button" class="btn btn-primary" data-act="apply">应用选择</button>`,
      onClose: finish,
    });

    /** 按当前 choices 刷新按钮高亮。 */
    const paint = () => {
      qsa('[data-pick]', modal.el).forEach((btn) => {
        const chosen = choices.get(btn.dataset.recid) === btn.dataset.pick;
        btn.classList.toggle('btn-primary', chosen);
      });
    };
    paint();

    modal.el.addEventListener('click', async (e) => {
      const pickBtn = e.target.closest('[data-pick]');
      if (pickBtn) {
        choices.set(pickBtn.dataset.recid, pickBtn.dataset.pick);
        paint();
        return;
      }

      const bulkBtn = e.target.closest('[data-bulk]');
      if (bulkBtn) {
        conflicts.forEach((c) => choices.set(String(c.recurringId), bulkBtn.dataset.bulk));
        paint();
        return;
      }

      if (e.target.closest('[data-act="cancel"]')) {
        finish();
        closeModal();
        return;
      }

      if (e.target.closest('[data-act="apply"]')) {
        const applyBtn = qs('[data-act="apply"]', modal.el);
        applyBtn.disabled = true;
        applyBtn.innerHTML = '<span class="spinner"></span> 处理中…';

        let done = 0;
        let failed = 0;
        // 逐条提交，保证任何一条失败都不影响其它条目
        for (const c of conflicts) {
          const action = choices.get(String(c.recurringId)) || 'skip';
          try {
            await API.resolveConflict(planDate, c.recurringId, action);
            done += 1;
          } catch (err) {
            failed += 1;
            toast(`处理冲突「${c.name}」失败：${(err && err.message) || '未知错误'}`, 'error');
          }
        }

        finish();
        closeModal();
        if (done > 0) toast(`已处理 ${done} 条冲突${failed ? `，${failed} 条失败` : ''}`, failed ? 'warn' : 'success');
        await loadToday();
      }
    });
  });
}

/* ==========================================================================
 * 十一、长期目标
 * ========================================================================== */

async function loadGoals() {
  const host = qs('#goal-list');
  host.innerHTML = '<div class="empty">正在载入…</div>';

  if (!state.tags.length) await loadTags();

  const [goalsRes, progressRes] = await Promise.all([
    attempt(() => API.goals()),
    attempt(() => API.goalProgress()),
  ]);

  if (!goalsRes.ok || !progressRes.ok) {
    host.innerHTML = '<div class="empty"><strong>载入失败</strong>请稍后重试。</div>';
    return;
  }

  const goals = Array.isArray(goalsRes.data) ? goalsRes.data : [];
  const progress = Array.isArray(progressRes.data) ? progressRes.data : [];

  if (!goals.length) {
    host.innerHTML =
      '<div class="empty"><strong>还没有长期目标</strong>添加目标并绑定标签后，就能看到累计投入时长了。</div>';
    return;
  }

  // 进度条以「投入最多的目标」为满格基准，便于横向对比
  const maxSeconds = progress.reduce((m, p) => Math.max(m, Number(p.totalSeconds) || 0), 0) || 1;

  host.innerHTML = progress.map((p) => {
    const goal = goals.find((g) => String(g.id) === String(p.goalId)) || {};
    const color = safeColor(p.tagColor || (findTag(goal.tagId) || {}).color);
    const total = Number(p.totalSeconds) || 0;
    const percent = Math.max(2, Math.round((total / maxSeconds) * 100));

    const chips = [tagChipHtml(p.tagName, p.tagColor)];
    if (!p.tagId) chips.push('<span class="badge badge-soft">未绑定标签</span>');

    return (
      `<div class="item-row" data-goal="${escapeHtml(String(p.goalId))}">` +
        `<div class="item-main">` +
          `<div class="item-title"><span>${escapeHtml(p.content)}</span>${chips.join('')}</div>` +
          `<div class="item-sub">累计投入 ${escapeHtml(fmtHoursMinutes(total))} · ` +
            `涉及任务 ${escapeHtml(String(Number(p.taskCount) || 0))} 条 · ` +
            `活跃 ${escapeHtml(String(Number(p.activeDays) || 0))} 天` +
            `${!p.tagId ? '（绑定标签后才会累计时长）' : ''}` +
          `</div>` +
          `<div class="goal-bar" style="--goal-color:${escapeHtml(color)}">` +
            `<i style="width:${escapeHtml(String(percent))}%"></i>` +
          `</div>` +
        `</div>` +
        `<div class="item-actions">` +
          `<button type="button" class="btn btn-sm" data-goal-act="edit" data-id="${escapeHtml(String(p.goalId))}">编辑</button>` +
          `<button type="button" class="btn btn-sm btn-ghost" data-goal-act="delete" data-id="${escapeHtml(String(p.goalId))}">删除</button>` +
        `</div>` +
      `</div>`
    );
  }).join('');

  // 缓存目标原文，编辑时直接取用
  state.goals = goals;
}

function resetGoalForm() {
  state.editingGoalId = null;
  qs('#goal-form-title').textContent = '新增长期目标';
  qs('#goal-content').value = '';
  qs('#goal-tag').value = '';
  qs('#btn-goal-cancel').hidden = true;
}

qs('#btn-goal-save').addEventListener('click', async () => {
  const content = qs('#goal-content').value.trim();
  const tagValue = qs('#goal-tag').value;

  if (!content) {
    toast('请填写目标内容', 'warn');
    return;
  }

  const editingId = state.editingGoalId;
  let r;
  if (editingId) {
    const body = { content };
    if (tagValue) body.tagId = Number(tagValue);
    else body.clearTag = true;
    r = await attempt(() => API.updateGoal(editingId, body), '目标已更新');
  } else {
    const body = { content };
    if (tagValue) body.tagId = Number(tagValue);
    r = await attempt(() => API.createGoal(body), '目标已创建');
  }

  if (r.ok) {
    resetGoalForm();
    await loadGoals();
  }
});

qs('#btn-goal-cancel').addEventListener('click', resetGoalForm);

qs('#goal-list').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-goal-act]');
  if (!btn) return;
  const id = btn.dataset.id;

  if (btn.dataset.goalAct === 'delete') {
    const yes = await uiConfirm('确定删除这个长期目标吗？（已统计的历史时长不会丢失）', {
      title: '删除长期目标',
      okText: '删除',
      danger: true,
    });
    if (!yes) return;
    const r = await attempt(() => API.deleteGoal(id), '目标已删除');
    if (r.ok) {
      if (String(state.editingGoalId) === String(id)) resetGoalForm();
      await loadGoals();
    }
    return;
  }

  const goal = (state.goals || []).find((g) => String(g.id) === String(id));
  if (!goal) {
    toast('未找到该目标，可能已被删除', 'error');
    return;
  }
  state.editingGoalId = goal.id;
  qs('#goal-form-title').textContent = `编辑长期目标 #${goal.id}`;
  qs('#goal-content').value = goal.content || '';
  qs('#goal-tag').value = goal.tagId === null || goal.tagId === undefined ? '' : String(goal.tagId);
  qs('#btn-goal-cancel').hidden = false;
  qs('#goal-content').focus();
});

/* ==========================================================================
 * 十二、统计视图
 * ========================================================================== */

async function loadStats() {
  const trendHost = qs('#trend-chart');
  const tagHost = qs('#tag-chart');
  trendHost.innerHTML = '<div class="empty">正在载入…</div>';
  tagHost.innerHTML = '<div class="empty">正在载入…</div>';

  const [trendRes, tagRes] = await Promise.all([
    attempt(() => API.statsTrend(21)),
    attempt(() => API.statsTags(21)),
  ]);

  if (trendRes.ok) renderTrendChart(Array.isArray(trendRes.data) ? trendRes.data : []);
  else trendHost.innerHTML = '<div class="empty"><strong>趋势数据载入失败</strong>请稍后重试。</div>';

  if (tagRes.ok) renderTagChart(Array.isArray(tagRes.data) ? tagRes.data : []);
  else tagHost.innerHTML = '<div class="empty"><strong>标签数据载入失败</strong>请稍后重试。</div>';
}

/** 用内联 SVG 画柱状图，不依赖任何图表库。 */
function renderTrendChart(points) {
  const host = qs('#trend-chart');

  if (!points.length) {
    host.innerHTML = '<div class="empty"><strong>还没有统计数据</strong>完成几次任务后这里就会出现趋势图。</div>';
    return;
  }

  const W = 680;
  const H = 240;
  const padTop = 24;
  const padBottom = 46;
  const padX = 8;
  const plotH = H - padTop - padBottom;
  const slot = (W - padX * 2) / points.length;
  const barW = Math.max(6, slot - 8);
  const today = todayStr();

  const maxSeconds = points.reduce((m, p) => Math.max(m, Number(p.totalSeconds) || 0), 0);
  const maxMin = Math.max(10, Math.ceil(maxSeconds / 60));

  const bars = points.map((p, i) => {
    const seconds = Math.max(0, Number(p.totalSeconds) || 0);
    const minutes = seconds / 60;
    const h = seconds > 0 ? Math.max(3, (minutes / maxMin) * plotH) : 0;
    const x = padX + i * slot + (slot - barW) / 2;
    const y = padTop + plotH - h;
    const isToday = p.planDate === today;
    const cls = seconds > 0 ? (isToday ? 'trend-bar is-today' : 'trend-bar') : 'trend-bar is-empty';
    const label = p.planDate ? p.planDate.slice(5) : '';

    const valueLabel = seconds > 0
      ? `<text class="trend-value" x="${(x + barW / 2).toFixed(1)}" y="${(y - 4).toFixed(1)}" text-anchor="middle">${escapeHtml(String(Math.round(minutes)))}</text>`
      : '';

    const title = `${p.planDate || ''}：${fmtHoursMinutes(seconds)}，任务 ${Number(p.taskCount) || 0} 条，完成 ${Number(p.completedCount) || 0} 条`;

    return (
      `<g>` +
        `<title>${escapeHtml(title)}</title>` +
        (seconds > 0
          ? `<rect class="${cls}" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${barW.toFixed(1)}" height="${h.toFixed(1)}" rx="3"></rect>`
          : `<rect class="${cls}" x="${x.toFixed(1)}" y="${(padTop + plotH - 3).toFixed(1)}" width="${barW.toFixed(1)}" height="3" rx="1.5"></rect>`) +
        valueLabel +
        `<text class="trend-axis" x="${(x + barW / 2).toFixed(1)}" y="${(padTop + plotH + 12).toFixed(1)}" ` +
          `text-anchor="end" transform="rotate(-45 ${(x + barW / 2).toFixed(1)} ${(padTop + plotH + 12).toFixed(1)})">${escapeHtml(label)}</text>` +
      `</g>`
    );
  }).join('');

  host.innerHTML =
    `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="最近 ${points.length} 天投入趋势柱状图">` +
      // 顶部量程与基线，便于估读
      `<line class="trend-grid" x1="${padX}" y1="${padTop}" x2="${W - padX}" y2="${padTop}"></line>` +
      `<text class="trend-axis" x="${padX}" y="${padTop - 9}">${escapeHtml(String(maxMin))} 分钟</text>` +
      `<line class="trend-grid" x1="${padX}" y1="${padTop + plotH}" x2="${W - padX}" y2="${padTop + plotH}"></line>` +
      `<text class="trend-axis" x="${padX}" y="${padTop + plotH - 4}">0</text>` +
      bars +
    `</svg>`;
}

/** 横向条形图展示标签时长排行。 */
function renderTagChart(stats) {
  const host = qs('#tag-chart');

  const rows = stats.filter((s) => Number(s.totalSeconds) > 0 || Number(s.taskCount) > 0);
  if (!rows.length) {
    host.innerHTML = '<div class="empty"><strong>还没有标签统计</strong>给任务打上标签并开始计时，这里就会显示分布。</div>';
    return;
  }

  const maxSeconds = rows.reduce((m, r) => Math.max(m, Number(r.totalSeconds) || 0), 0) || 1;

  host.innerHTML = rows.map((r) => {
    const total = Number(r.totalSeconds) || 0;
    const percent = Math.max(2, Math.round((total / maxSeconds) * 100));
    const color = safeColor(r.color);

    return (
      `<div class="tag-bar-row">` +
        `<div class="tag-bar-name" title="${escapeHtml(r.tagName)}">` +
          `<i class="tag-bar-dot" style="--tag-color:${escapeHtml(color)}"></i>` +
          `<span>${escapeHtml(r.tagName || '未分类')}</span>` +
        `</div>` +
        `<div class="tag-bar-track" title="${escapeHtml(fmtHoursMinutes(total))}">` +
          `<i style="--tag-color:${escapeHtml(color)};width:${escapeHtml(String(percent))}%"></i>` +
        `</div>` +
        `<div class="tag-bar-value">${escapeHtml(fmtHoursMinutes(total))} · ${escapeHtml(String(Number(r.taskCount) || 0))} 条</div>` +
      `</div>`
    );
  }).join('');
}

qs('#btn-stats-refresh').addEventListener('click', loadStats);

qs('#btn-analyze').addEventListener('click', async () => {
  const btn = qs('#btn-analyze');
  const host = qs('#analyze-result');
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> 分析中…';
  host.innerHTML = '<div class="alert">' +
    '<div class="alert-body"><p>AI 正在分析今天的预估与实际偏差，通常需要 10～30 秒。</p></div></div>';

  const r = await attempt(() => API.analyze(state.date));

  btn.disabled = false;
  btn.textContent = '开始分析';

  if (!r.ok) {
    const msg = (r.error && r.error.message) || '分析失败';
    host.innerHTML =
      `<div class="alert alert-warn">` +
        `<div class="alert-body"><div class="alert-title">暂时无法分析</div><p>${escapeHtml(msg)}</p></div>` +
      `</div>`;
    return;
  }

  const d = r.data || {};
  const suggestions = Array.isArray(d.suggestions) ? d.suggestions : [];

  host.innerHTML =
    `<div class="alert alert-ok">` +
      `<div class="alert-body"><div class="alert-title">效率总结</div>` +
      `<p>${escapeHtml(d.summary || '（本次没有返回总结）')}</p></div>` +
    `</div>` +
    (suggestions.length
      ? `<div class="card"><div class="card-head"><h2>改进建议</h2></div><ol class="suggestion-list">` +
        suggestions.map((s) => `<li>${escapeHtml(s)}</li>`).join('') +
        `</ol></div>`
      : '');
});

/* ==========================================================================
 * 十三、设置视图
 * ========================================================================== */

async function loadSettings() {
  await loadTags();
  await Promise.all([loadPlanConfig(), loadTimeBlocks(), loadSystemConfig()]);
  renderTagList();
}

/* ---------- 当日计划设置 ---------- */

async function loadPlanConfig() {
  const r = await attempt(() => API.planConfig(state.date));
  if (!r.ok) return;
  const cfg = r.data || {};

  qs('#cfg-deadline').value = minToHHMM(cfg.deadlineMin);
  qs('#cfg-break').value = String(Number(cfg.breakMin) || 0);
  qs('#plan-cfg-hint').textContent = cfg.isDefault
    ? `当前日期（${state.date}）尚未单独设置，显示的是系统默认值；保存后会为这一天单独生效。`
    : `正在显示 ${state.date} 的当日设置。`;
}

qs('#btn-save-plan-config').addEventListener('click', async () => {
  const deadlineMin = hhmmToMin(qs('#cfg-deadline').value);
  const breakMin = Math.round(Number(qs('#cfg-break').value));

  if (deadlineMin === null || deadlineMin <= 0 || deadlineMin >= 1440) {
    toast('截止时间必须介于 00:01 与 23:59 之间', 'warn');
    return;
  }
  if (!Number.isFinite(breakMin) || breakMin < 0 || breakMin > 120) {
    toast('休息间隔必须在 0 到 120 分钟之间', 'warn');
    return;
  }

  const r = await attempt(
    () => API.updatePlanConfig({ planDate: state.date, deadlineMin, breakMin }),
    '当日设置已保存'
  );
  if (r.ok) {
    await loadPlanConfig();
    await loadToday();
  }
});

/* ---------- 时间块 ---------- */

async function loadTimeBlocks() {
  const host = qs('#tb-list');
  host.innerHTML = '<div class="empty">正在载入…</div>';

  const r = await attempt(() => API.timeblocks());
  if (!r.ok) {
    host.innerHTML = '<div class="empty"><strong>载入失败</strong>请稍后重试。</div>';
    return;
  }

  const rows = Array.isArray(r.data) ? r.data : [];
  if (!rows.length) {
    host.innerHTML =
      '<div class="empty"><strong>还没有时间块</strong>添加后，负载计算与自动排程才知道你什么时候有空。</div>';
    return;
  }

  host.innerHTML = rows.map((b) => {
    const dayText = Number(b.weekday) === -1 ? '每天通用' : (WEEKDAY_NAMES[Number(b.weekday)] || `未知(${escapeHtml(String(b.weekday))})`);
    return (
      `<div class="item-row">` +
        `<div class="item-main">` +
          `<div class="item-title"><span>${escapeHtml(dayText)} ${escapeHtml(minToHHMM(b.startMin))} – ${escapeHtml(minToHHMM(b.endMin))}</span>` +
          (b.enabled ? '' : '<span class="badge badge-pending">已停用</span>') +
          `</div>` +
          `<div class="item-sub">共 ${escapeHtml(fmtMinutes(Number(b.endMin) - Number(b.startMin)))}</div>` +
        `</div>` +
        `<div class="item-actions">` +
          `<button type="button" class="btn btn-sm" data-tb-act="edit" data-id="${escapeHtml(String(b.id))}">编辑</button>` +
          `<button type="button" class="btn btn-sm btn-ghost" data-tb-act="delete" data-id="${escapeHtml(String(b.id))}">删除</button>` +
        `</div>` +
      `</div>`
    );
  }).join('');
}

function resetTimeBlockForm() {
  state.editingBlockId = null;
  qs('#tb-form-title').textContent = '新增时间块';
  qs('#tb-weekday').value = '-1';
  qs('#tb-start').value = '09:00';
  qs('#tb-end').value = '12:00';
  qs('#tb-enabled').checked = true;
  qs('#btn-tb-cancel').hidden = true;
}

qs('#btn-tb-save').addEventListener('click', async () => {
  const weekday = Number(qs('#tb-weekday').value);
  const startMin = hhmmToMin(qs('#tb-start').value);
  const endMin = hhmmToMin(qs('#tb-end').value);

  if (startMin === null) {
    toast('请填写正确的开始时间', 'warn');
    return;
  }
  if (endMin === null) {
    toast('请填写正确的结束时间', 'warn');
    return;
  }
  if (endMin <= startMin) {
    toast('结束时间必须晚于开始时间（暂不支持跨零点的时间块）', 'warn');
    return;
  }

  const body = { weekday, startMin, endMin, enabled: qs('#tb-enabled').checked };
  const editingId = state.editingBlockId;
  const r = editingId
    ? await attempt(() => API.updateTimeBlock(editingId, body), '时间块已更新')
    : await attempt(() => API.createTimeBlock(body), '时间块已创建');

  if (r.ok) {
    resetTimeBlockForm();
    await loadTimeBlocks();
    await loadToday();
  }
});

qs('#btn-tb-cancel').addEventListener('click', resetTimeBlockForm);

qs('#tb-list').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-tb-act]');
  if (!btn) return;
  const id = Number(btn.dataset.id);

  if (btn.dataset.tbAct === 'delete') {
    const yes = await uiConfirm('确定删除该时间块吗？', { title: '删除时间块', okText: '删除', danger: true });
    if (!yes) return;
    const r = await attempt(() => API.deleteTimeBlock(id), '时间块已删除');
    if (r.ok) {
      if (String(state.editingBlockId) === String(id)) resetTimeBlockForm();
      await loadTimeBlocks();
      await loadToday();
    }
    return;
  }

  const r = await attempt(() => API.timeblocks());
  if (!r.ok) return;
  const row = (Array.isArray(r.data) ? r.data : []).find((x) => String(x.id) === String(id));
  if (!row) {
    toast('未找到该时间块，可能已被删除', 'error');
    return;
  }
  state.editingBlockId = row.id;
  qs('#tb-form-title').textContent = `编辑时间块 #${row.id}`;
  qs('#tb-weekday').value = String(row.weekday);
  qs('#tb-start').value = minToHHMM(row.startMin);
  qs('#tb-end').value = minToHHMM(row.endMin);
  qs('#tb-enabled').checked = row.enabled !== false;
  qs('#btn-tb-cancel').hidden = false;
});

/* ---------- 标签管理 ---------- */

function renderTagList() {
  const host = qs('#tag-list');
  if (!state.tags.length) {
    host.innerHTML = '<div class="empty"><strong>还没有标签</strong>用标签区分学习、工作、运动等类别，统计会更直观。</div>';
    return;
  }

  host.innerHTML = state.tags.map((t) => (
    `<div class="item-row">` +
      `<div class="item-main">` +
        `<div class="item-title">${tagChipHtml(t.name, t.color)}</div>` +
        `<div class="item-sub">颜色 ${escapeHtml(safeColor(t.color))}</div>` +
      `</div>` +
      `<div class="item-actions">` +
        `<button type="button" class="btn btn-sm" data-tag-act="edit" data-id="${escapeHtml(String(t.id))}">编辑</button>` +
        `<button type="button" class="btn btn-sm btn-ghost" data-tag-act="delete" data-id="${escapeHtml(String(t.id))}">删除</button>` +
      `</div>` +
    `</div>`
  )).join('');
}

function resetTagForm() {
  state.editingTagId = null;
  qs('#tag-form-title').textContent = '新增标签';
  qs('#tag-name').value = '';
  qs('#tag-color').value = '#5d78d2';
  qs('#btn-tag-cancel').hidden = true;
}

qs('#btn-tag-save').addEventListener('click', async () => {
  const name = qs('#tag-name').value.trim();
  const color = qs('#tag-color').value;

  if (!name) {
    toast('请填写标签名称', 'warn');
    return;
  }
  if (!/^#([0-9a-fA-F]{6})$/.test(color)) {
    toast('颜色必须是 #rrggbb 形式', 'warn');
    return;
  }

  const editingId = state.editingTagId;
  const r = editingId
    ? await attempt(() => API.updateTag(editingId, { name, color }), '标签已更新')
    : await attempt(() => API.createTag({ name, color }), '标签已创建');

  if (r.ok) {
    resetTagForm();
    await loadTags();
    renderTagList();
    await loadToday();
  }
});

qs('#btn-tag-cancel').addEventListener('click', resetTagForm);

qs('#tag-list').addEventListener('click', async (e) => {
  const btn = e.target.closest('button[data-tag-act]');
  if (!btn) return;
  const id = Number(btn.dataset.id);

  if (btn.dataset.tagAct === 'delete') {
    const yes = await uiConfirm('确定删除该标签吗？引用它的任务会自动解绑（任务本身不会被删除）。', {
      title: '删除标签',
      okText: '删除',
      danger: true,
    });
    if (!yes) return;
    const r = await attempt(() => API.deleteTag(id), '标签已删除');
    if (r.ok) {
      if (String(state.editingTagId) === String(id)) resetTagForm();
      await loadTags();
      renderTagList();
      await loadGoals();
      await loadToday();
    }
    return;
  }

  const tag = findTag(id);
  if (!tag) {
    toast('未找到该标签', 'error');
    return;
  }
  state.editingTagId = tag.id;
  qs('#tag-form-title').textContent = `编辑标签 #${tag.id}`;
  qs('#tag-name').value = tag.name || '';
  qs('#tag-color').value = safeColor(tag.color);
  qs('#btn-tag-cancel').hidden = false;
  qs('#tag-name').focus();
});

/* ---------- AI 与系统配置 ---------- */

async function loadSystemConfig() {
  const r = await attempt(() => API.config());
  if (!r.ok) return;
  const cfg = r.data || {};

  qs('#ai-base-url').value = cfg.aiBaseUrl || '';
  qs('#ai-model').value = cfg.aiModel || '';
  // 后端不会回传密钥明文，因此输入框始终保持为空，只用占位提示表达当前状态
  const keyInput = qs('#ai-key');
  keyInput.value = '';
  keyInput.placeholder = cfg.aiKeyConfigured ? '已配置（留空则不修改）' : '尚未配置，请填写 API Key';
  qs('#ai-key-hint').textContent = cfg.aiKeyConfigured
    ? '当前状态：已配置 API Key。留空保存不会改动已保存的密钥。'
    : '当前状态：尚未配置 API Key。';

  qs('#cfg-default-deadline').value = minToHHMM(cfg.defaultDeadlineMin);
  qs('#cfg-default-break').value = String(Number(cfg.defaultBreakMin) || 0);
  qs('#cfg-retention').value = String(Number(cfg.retentionDays) || 21);
}

qs('#btn-save-ai').addEventListener('click', async () => {
  const body = {
    aiBaseUrl: qs('#ai-base-url').value.trim(),
    aiModel: qs('#ai-model').value.trim(),
  };
  // 只有用户真的输入了新值才提交密钥，避免用空串覆盖已保存的密钥
  const key = qs('#ai-key').value.trim();
  if (key) body.aiApiKey = key;

  const r = await attempt(() => API.updateConfig(body), 'AI 配置已保存');
  if (r.ok) {
    await loadSystemConfig();
    await loadToday();
  }
});

qs('#btn-save-system').addEventListener('click', async () => {
  const deadlineMin = hhmmToMin(qs('#cfg-default-deadline').value);
  const breakMin = Math.round(Number(qs('#cfg-default-break').value));
  const retentionDays = Math.round(Number(qs('#cfg-retention').value));

  if (deadlineMin === null || deadlineMin <= 0 || deadlineMin >= 1440) {
    toast('默认截止时间必须介于 00:01 与 23:59 之间', 'warn');
    return;
  }
  if (!Number.isFinite(breakMin) || breakMin < 0 || breakMin > 120) {
    toast('默认休息间隔必须在 0 到 120 分钟之间', 'warn');
    return;
  }
  if (!Number.isFinite(retentionDays) || retentionDays < 1 || retentionDays > 3650) {
    toast('数据保留天数必须在 1 到 3650 之间', 'warn');
    return;
  }

  const r = await attempt(
    () => API.updateConfig({ defaultDeadlineMin: deadlineMin, defaultBreakMin: breakMin, retentionDays }),
    '系统默认值已保存'
  );
  if (r.ok) await loadSystemConfig();
});

/* ---------- 数据管理 ---------- */

// 导出：浏览器直接跳转下载，无需经过 fetch
qs('#btn-export-csv').addEventListener('click', () => {
  window.location.href = `/api/export/tasks.csv?date=${enc(state.date)}`;
});

qs('#btn-export-backup').addEventListener('click', () => {
  window.location.href = '/api/export/backup';
});

qs('#btn-import').addEventListener('click', async () => {
  const input = qs('#import-file');
  const file = input.files && input.files[0];
  if (!file) {
    toast('请先选择要导入的备份文件（zip）', 'warn');
    return;
  }

  const yes = await uiConfirm(
    '导入备份会把文件中的数据写入当前数据库，可能与现有数据合并。建议先导出一次当前备份。是否继续？',
    { title: '导入备份', okText: '开始导入' }
  );
  if (!yes) return;

  const btn = qs('#btn-import');
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> 导入中…';

  const r = await attempt(() => uploadBackup(file), '备份导入完成');

  btn.disabled = false;
  btn.textContent = '上传并导入';

  if (r.ok) {
    input.value = '';
    const d = r.data || {};
    uiInfo('导入结果', `<p class="modal-note">${escapeHtml(
      Object.entries(d).map(([k, v]) => `${k}：${Array.isArray(v) ? v.length : v}`).join('；') || '导入已完成'
    )}</p>`);
    await loadSettings();
    await loadToday();
  }
});

qs('#btn-cleanup').addEventListener('click', async () => {
  const yes = await uiConfirm('将删除超出保留期的历史明细数据（聚合统计会保留）。是否继续？', {
    title: '清理过期数据',
    okText: '立即清理',
  });
  if (!yes) return;

  const r = await attempt(() => API.cleanup());
  if (!r.ok) return;
  const d = r.data || {};
  uiInfo(
    '清理完成',
    `<p class="modal-note">` +
      `清理截止日期：${escapeHtml(d.cutoffDate || '--')}<br>` +
      `删除任务：${escapeHtml(String(Number(d.deletedTasks) || 0))} 条<br>` +
      `删除每日配置：${escapeHtml(String(Number(d.deletedConfigs) || 0))} 条<br>` +
      `归集统计天数：${escapeHtml(String(Number(d.rolledUpDays) || 0))} 天` +
    `</p>`
  );
});

qs('#btn-clear-data').addEventListener('click', async () => {
  const yes = await uiConfirm(
    '此操作会清空全部任务、标签、周期任务、长期目标与统计数据，且无法撤销。\n请输入 DELETE 以确认。',
    { title: '清空全部数据', okText: '确认清空', danger: true, requireText: 'DELETE' }
  );
  if (!yes) return;

  const r = await attempt(() => API.clearAll());
  if (!r.ok) return;
  const d = r.data || {};
  toast(`已清空全部业务数据（删除 ${Number(d.deleted) || 0} 项）`, 'success');
  await loadSettings();
  await loadToday();
  resetTimeBlockForm();
  resetTagForm();
  resetRecurringForm();
  resetGoalForm();
});

/* ==========================================================================
 * 十四、视图切换与定时器
 * ========================================================================== */

const VIEW_LOADERS = {
  today: loadToday,
  generate: async () => {},
  pool: loadPool,
  recurring: loadRecurring,
  goals: loadGoals,
  stats: loadStats,
  settings: loadSettings,
};

/**
 * 从 URL 的 #view=xxx 读视图名。
 *
 * 以前切换视图只改内存里的 state.view，地址栏一动不动 ——
 * 刷新一下就回到「今日计划」，把链接发给别人也定位不到同一页。
 * 现在视图跟着 hash 走：可以收藏、可以后退、可以直接分享到某一页。
 */
function viewFromHash() {
  const matched = /^#view=([a-z]+)$/.exec(location.hash || '');
  const name = matched ? matched[1] : '';
  return VIEW_LOADERS[name] ? name : null;
}

function switchView(name, opts) {
  const fromHash = Boolean(opts && opts.fromHash);
  if (!VIEW_LOADERS[name]) name = 'today';

  state.view = name;

  qsa('.nav-btn').forEach((btn) => {
    const active = btn.dataset.view === name;
    btn.classList.toggle('is-active', active);
    // 读屏软件靠 aria-current 知道当前在哪一页，而不是靠一个视觉上的类名
    if (active) btn.setAttribute('aria-current', 'page');
    else btn.removeAttribute('aria-current');
  });
  qsa('.view').forEach((el) => el.classList.toggle('is-active', el.id === `view-${name}`));

  // 用 replaceState 而不是给 location.hash 赋值：
  // 后者会让每次切换都压一条历史记录，后退键就要按好几次才退得出去。
  if (!fromHash) {
    const hash = `#view=${name}`;
    if (location.hash !== hash) {
      try { history.replaceState(null, '', hash); } catch (e) { /* file:// 下会被拒绝，忽略 */ }
    }
  }

  const loader = VIEW_LOADERS[name];
  if (loader) loader();
}

window.addEventListener('hashchange', () => {
  const name = viewFromHash();
  if (name && name !== state.view) switchView(name, { fromHash: true });
});

qs('#nav').addEventListener('click', (e) => {
  const btn = e.target.closest('.nav-btn');
  if (!btn) return;
  switchView(btn.dataset.view);
});

/** 每秒刷新顶部时钟（只更新这一个显示，不重绘列表）。 */
function tickClock() {
  qs('#live-clock').textContent = fmtClock();
  qs('#live-date').textContent = fmtDateCn();
  updateLiveElapsed();

  // 跨天时自动切到新的业务日期，并重新物化周期任务
  const today = todayStr();
  if (today !== state.date) {
    state.date = today;
    resetRecurringForm();
    materializeRecurring().then(() => {
      loadToday();
      if (state.view === 'pool') loadPool();
    });
  }
}

/**
 * 每 5 秒轮询一次仪表盘，保证耗时、负载、AI 提示始终是后端真值。
 * 轮询失败时不弹 toast（否则后端离线会每 5 秒刷屏一次），只在状态翻转时提示一次。
 */
let pollFailureNotified = false;

async function pollDashboard() {
  if (document.visibilityState !== 'visible') return;

  let data;
  try {
    data = await API.dashboard(state.date);
  } catch (err) {
    if (!pollFailureNotified) {
      pollFailureNotified = true;
      toast(`自动刷新失败：${(err && err.message) || '未知错误'}`, 'error');
    }
    return;
  }
  pollFailureNotified = false;

  state.dash = data;
  state.dashFetchedAt = Date.now();
  // 拖拽过程中不要打断用户操作
  if (state.dragId === null) {
    state.tasks = Array.isArray(data?.tasks) ? data.tasks.slice() : [];
  }
  if (state.view === 'today') renderToday();
  else {
    const dot = qs('#nav-pool-dot');
    if (dot) dot.hidden = !(Number(data?.poolCount) > 0);
  }
}

/**
 * 心跳：当且仅当存在 status === "running" 的任务时，
 * 每 30 秒调用一次该任务的心跳接口。
 * 后端据此判断网页是否还开着；超过 90 秒无心跳会自动结算并暂停计时。
 */
async function sendHeartbeat() {
  const running = state.tasks.find((t) => t.status === 'running');
  if (!running) return;
  try {
    await API.heartbeat(running.id);
  } catch (err) {
    // 心跳失败不打扰用户，只记录，避免刷屏提示
    console.warn('心跳上报失败：', err && err.message);
  }
}

/** 页面关闭 / 切到后台时尽力补一次心跳。 */
function sendFinalHeartbeat() {
  const running = state.tasks.find((t) => t.status === 'running');
  if (!running) return;
  const url = `/api/tasks/${enc(running.id)}/heartbeat`;
  if (navigator.sendBeacon) {
    try {
      navigator.sendBeacon(url, new Blob(['{}'], { type: 'application/json' }));
      return;
    } catch (err) {
      // 落到下面的 fetch 兜底
    }
  }
  fetch(url, { method: 'POST', keepalive: true }).catch(() => {});
}

window.addEventListener('pagehide', sendFinalHeartbeat);
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'hidden') sendFinalHeartbeat();
});

/* ==========================================================================
 * 十五、启动
 * ========================================================================== */

async function init() {
  state.date = todayStr();

  resetRecurringForm();
  qs('#rec-from').value = state.date;

  tickClock();
  setInterval(tickClock, 1000);          // 时钟：每秒
  setInterval(pollDashboard, 5000);      // 仪表盘：每 5 秒
  setInterval(sendHeartbeat, 30000);     // 心跳：每 30 秒（内部会判断是否有 running 任务）

  await loadTags();

  // 每次加载页面都物化一次周期任务（后端保证幂等），必要时弹出冲突处理
  await materializeRecurring();

  await loadToday();

  // 打开形如 http://…/#view=stats 的链接时，直接落到对应页面
  const initialView = viewFromHash();
  if (initialView && initialView !== 'today') switchView(initialView, { fromHash: true });
}

init().catch((err) => {
  toast(`初始化失败：${(err && err.message) || '未知错误'}`, 'error');
});
