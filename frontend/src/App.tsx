import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  Bell,
  Bot,
  Boxes,
  Check,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  Clock3,
  CodeXml,
  Download,
  ExternalLink,
  FileClock,
  Folder,
  FolderCheck,
  Link2,
  LoaderCircle,
  Info,
  MessageSquareText,
  Minus,
  PanelLeftClose,
  Play,
  Power,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Settings,
  ShieldAlert,
  SlidersHorizontal,
  Trash2,
  Trophy,
  Unplug,
  Waypoints,
  X,
} from "lucide-react";
import * as AppService from "../bindings/github.com/Hans2573/OpenCode-Handoff/appservice";
import type {
  Dashboard,
  ExecutionRunView,
  ExecutionSessionView,
  EventView,
  IntegrationView,
  ProjectView,
  SessionView,
  SettingsInput,
  SettingsView,
} from "../bindings/github.com/Hans2573/OpenCode-Handoff/internal/desktop/models";

type Page = "overview" | "projects" | "sessions" | "agents" | "channels" | "events" | "settings";

const navigation: Array<{ id: Page; label: string; icon: typeof Activity }> = [
  { id: "overview", label: "总览", icon: Boxes },
  { id: "projects", label: "项目接入", icon: FolderCheck },
  { id: "sessions", label: "Sessions", icon: Activity },
  { id: "agents", label: "Agents", icon: Bot },
  { id: "channels", label: "渠道", icon: Waypoints },
  { id: "events", label: "事件记录", icon: FileClock },
  { id: "settings", label: "设置", icon: Settings },
];

const emptyDashboard: Dashboard = {
  generatedAt: new Date().toISOString(),
  service: {
    state: "loading",
    message: "正在连接本地服务",
    engineRunning: false,
    openCodeOnline: false,
    feishuConnected: false,
	feishuState: "connecting",
	feishuMessage: "正在连接飞书 WebSocket",
    configValid: true,
    openCodeUrl: "http://127.0.0.1:4096",
  },
  summary: { connectedProjects: 0, runningSessions: 0, pendingActions: 0, connectedChannels: 0 },
  projects: [],
  sessions: [],
  executionRuns: [],
  executionSessions: [],
  executionRetentionDays: 30,
  agents: [],
  channels: [],
};

function App() {
  const [page, setPage] = useState<Page>("overview");
  const [dashboard, setDashboard] = useState<Dashboard>(emptyDashboard);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [toast, setToast] = useState("");
  const polling = useRef(false);

  const showToast = useCallback((message: string) => {
    setToast(message);
    window.setTimeout(() => setToast(""), 3200);
  }, []);

  const loadDashboard = useCallback(async (quiet = false) => {
    if (polling.current) return;
    polling.current = true;
    if (!quiet) setLoading(true);
    try {
      const value = await AppService.GetDashboard();
      setDashboard(normaliseDashboard(value));
      setError("");
    } catch (reason) {
      setError(errorMessage(reason));
    } finally {
      polling.current = false;
      if (!quiet) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadDashboard();
    const timer = window.setInterval(() => void loadDashboard(true), 5000);
    return () => window.clearInterval(timer);
  }, [loadDashboard]);

  const changeRoute = async (projectID: string, enabled: boolean) => {
    try {
      const value = await AppService.SetProjectRoute(projectID, enabled);
      setDashboard(normaliseDashboard(value));
      showToast(enabled ? "项目已接入飞书渠道" : "项目已停止向飞书转发新事件");
    } catch (reason) {
      showToast(`操作失败：${errorMessage(reason)}`);
    }
  };

  const refreshProjects = async () => {
    setLoading(true);
    try {
      const value = await AppService.RefreshProjects();
      setDashboard(normaliseDashboard(value));
      setError("");
      showToast("已刷新 OpenCode 项目");
    } catch (reason) {
      setError(errorMessage(reason));
    } finally {
      setLoading(false);
    }
  };

  const retryService = async () => {
    try {
      const value = await AppService.RetryService();
      setDashboard(normaliseDashboard(value));
      showToast("已重新尝试启动 Handoff 服务");
    } catch (reason) {
      showToast(`重试失败：${errorMessage(reason)}`);
    }
  };

  const openSession = async (session: SessionView) => {
    try {
      await AppService.OpenSession(session.id, session.directory);
      showToast("已打开 OpenCode，Session ID 已复制到剪贴板");
    } catch (reason) {
      showToast(`无法打开 Agent：${errorMessage(reason)}`);
    }
  };

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand compact-brand">
          <span className="brand-icon"><CodeXml size={20} /></span>
          <div><strong>Agent Handoff</strong><small>Desktop</small></div>
        </div>
        <nav className="navigation" aria-label="主导航">
          {navigation.map(({ id, label, icon: Icon }) => (
            <button key={id} className={page === id ? "nav-item active" : "nav-item"} onClick={() => setPage(id)}>
              <Icon size={18} /><span>{label}</span>
            </button>
          ))}
        </nav>
        <div className="sidebar-footer"><PanelLeftClose size={18} /></div>
      </aside>

      <main className="workspace">
        <header className="topbar">
          <div className="brand wide-brand">
            <span className="brand-icon"><CodeXml size={20} /></span>
            <strong>Agent Handoff</strong>
          </div>
          <div className="service-strip">
            <span className={`health-dot ${dashboard.service.engineRunning ? "online" : "offline"}`} />
            <strong>{dashboard.service.engineRunning ? "服务运行中" : serviceStateLabel(dashboard.service.state)}</strong>
            <span className="divider" />
            <span>{dashboard.service.openCodeUrl}</span>
            <span className={dashboard.service.openCodeOnline ? "connected-text" : "muted"}>
              · {dashboard.service.openCodeOnline ? "已连接" : "未连接"}
            </span>
          </div>
          <div className="window-actions">
            <button className="icon-button" aria-label="通知"><Bell size={18} /></button>
            <span className="divider vertical" />
            <button className="icon-button" title="最小化到托盘" onClick={() => void AppService.HideWindow()}><Minus size={19} /></button>
            <button className="icon-button" title="设置" onClick={() => setPage("settings")}><Settings size={19} /></button>
          </div>
        </header>

        <div className="content">
          {error && <div className="banner error-banner"><CircleAlert size={18} /><span>{error}</span><button onClick={() => void loadDashboard()}>重试</button></div>}
          {dashboard.service.state === "conflict" && (
            <div className="banner warning-banner"><ShieldAlert size={18} /><span>{dashboard.service.message}</span><button onClick={() => void retryService()}>关闭 CLI 后重试</button></div>
          )}
          {dashboard.service.state === "config_error" && (
            <div className="banner warning-banner"><SlidersHorizontal size={18} /><span>{dashboard.service.message}</span><button onClick={() => setPage("settings")}>打开设置</button></div>
          )}

          {page === "overview" && <Overview dashboard={dashboard} loading={loading} onNavigate={setPage} onRoute={changeRoute} onRefresh={refreshProjects} onOpenSession={openSession} />}
          {page === "projects" && <ProjectsPage projects={dashboard.projects ?? []} loading={loading} onRoute={changeRoute} onRefresh={refreshProjects} />}
          {page === "sessions" && <SessionsPage sessions={dashboard.sessions ?? []} executionRuns={dashboard.executionRuns ?? []} executionSessions={dashboard.executionSessions ?? []} retentionDays={dashboard.executionRetentionDays || 30} onOpenSession={openSession} onRefresh={() => void loadDashboard()} showToast={showToast} />}
          {page === "agents" && <IntegrationsPage title="Agents" description="本地 Agent 实例及连接状态" items={dashboard.agents ?? []} />}
          {page === "channels" && <IntegrationsPage title="渠道" description="项目事件可以路由到一个或多个通信渠道" items={dashboard.channels ?? []} />}
          {page === "events" && <EventsPage showToast={showToast} />}
          {page === "settings" && <SettingsPage showToast={showToast} onSaved={() => void loadDashboard()} />}
        </div>
      </main>
      {toast && <div className="toast"><Check size={17} />{toast}</div>}
    </div>
  );
}

function Overview({ dashboard, loading, onNavigate, onRoute, onRefresh, onOpenSession }: {
  dashboard: Dashboard;
  loading: boolean;
  onNavigate: (page: Page) => void;
  onRoute: (id: string, enabled: boolean) => Promise<void>;
  onRefresh: () => Promise<void>;
  onOpenSession: (session: SessionView) => Promise<void>;
}) {
  const summaryCards = [
    { label: "已接入项目", value: dashboard.summary.connectedProjects, icon: Folder, tone: "blue" },
    { label: "运行中 Sessions", value: dashboard.summary.runningSessions, icon: Activity, tone: "green" },
    { label: "等待操作", value: dashboard.summary.pendingActions, icon: Clock3, tone: "orange" },
    { label: "已连接渠道", value: dashboard.summary.connectedChannels, icon: Link2, tone: "purple" },
  ];
  const activeSessions = (dashboard.sessions ?? []).filter((session) => !["idle", "unmonitored"].includes(session.status)).slice(0, 5);
  return (
    <section className="page overview-page">
      <div className="summary-grid">
        {summaryCards.map(({ label, value, icon: Icon, tone }) => (
          <div className="summary-card" key={label}>
            <div><span>{label}</span><strong>{value}</strong></div>
            <span className={`metric-icon ${tone}`}><Icon size={25} /></span>
          </div>
        ))}
      </div>

      <div className="overview-grid">
        <section className="panel projects-panel">
          <PanelHeader title="项目接入" action={<button className="secondary-button" onClick={() => void onRefresh()} disabled={loading}><RefreshCw size={16} className={loading ? "spin" : ""} />刷新 OpenCode 项目</button>} />
          <ProjectToolbar projects={dashboard.projects ?? []} compact />
          <ProjectTable projects={(dashboard.projects ?? []).slice(0, 8)} onRoute={onRoute} />
          {(dashboard.projects ?? []).length > 8 && <button className="panel-link" onClick={() => onNavigate("projects")}>查看全部项目 <ChevronRight size={15} /></button>}
        </section>

        <section className="panel sessions-panel">
          <PanelHeader title="Session 实时状态" action={<button className="text-button" onClick={() => onNavigate("sessions")}>查看全部 <ChevronRight size={15} /></button>} />
          <div className="session-list">
            {activeSessions.length ? activeSessions.map((session) => <SessionCard key={session.id} session={session} compact onOpen={() => void onOpenSession(session)} />) : <EmptyState icon={Activity} title="当前没有运行中的 Session" text="已接入项目出现活动后会显示在这里。" />}
          </div>
        </section>
      </div>

      <section className="panel integrations-panel">
        <PanelHeader title="Agents 与渠道" />
        <div className="integration-columns">
          <div><h3>Agents</h3><div className="integration-row">{(dashboard.agents ?? []).map((item) => <IntegrationCard key={item.id} item={item} compact />)}</div></div>
          <div><h3>Channels</h3><div className="integration-row">{(dashboard.channels ?? []).map((item) => <IntegrationCard key={item.id} item={item} compact />)}</div></div>
        </div>
      </section>
    </section>
  );
}

function ProjectsPage({ projects, loading, onRoute, onRefresh }: { projects: ProjectView[]; loading: boolean; onRoute: (id: string, enabled: boolean) => Promise<void>; onRefresh: () => Promise<void> }) {
  const [query, setQuery] = useState("");
  const [connectedOnly, setConnectedOnly] = useState(false);
  const filtered = projects.filter((project) => {
    const matches = `${project.name} ${project.directory}`.toLowerCase().includes(query.toLowerCase());
    return matches && (!connectedOnly || project.routeEnabled);
  });
  return (
    <section className="page">
      <PageHeader title="项目接入" description="选择哪些 OpenCode 项目可以通过飞书接收和处理 Handoff 事件。" />
      <section className="panel full-panel">
        <div className="toolbar">
          <SearchBox value={query} onChange={setQuery} placeholder="搜索项目名称或路径" />
          <label className="check-control"><input type="checkbox" checked={connectedOnly} onChange={(event) => setConnectedOnly(event.target.checked)} />仅显示已接入</label>
          <span className="toolbar-spacer" />
          <button className="primary-button" onClick={() => void onRefresh()} disabled={loading}><RefreshCw size={16} className={loading ? "spin" : ""} />刷新 OpenCode 项目</button>
        </div>
        <ProjectTable projects={filtered} onRoute={onRoute} roomy />
      </section>
    </section>
  );
}

type ExecutionMetric = "round" | "session";
type ExecutionRankingItem = { key: string; title: string; project: string; duration: number; statusLabel: string; active: boolean; detail: string };

function SessionsPage({ sessions, executionRuns, executionSessions, retentionDays, onOpenSession, onRefresh, showToast }: {
  sessions: SessionView[];
  executionRuns: ExecutionRunView[];
  executionSessions: ExecutionSessionView[];
  retentionDays: number;
  onOpenSession: (session: SessionView) => Promise<void>;
  onRefresh: () => void;
  showToast: (message: string) => void;
}) {
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("all");
  const [agent, setAgent] = useState("all");
  const [project, setProject] = useState("all");
  const [channel, setChannel] = useState("all");
  const [timeRange, setTimeRange] = useState("7");
  const [metric, setMetric] = useState<ExecutionMetric>("round");
  const [pageNumber, setPageNumber] = useState(1);
  const pageSize = 8;
  const agents = uniqueValues(sessions.map((item) => item.agentName));
  const projects = uniqueValues(sessions.map((item) => item.projectName));
  const channels = uniqueValues(sessions.map((item) => item.channelName));
  const filtered = sessions.filter((session) => {
    const matches = `${session.title} ${session.projectName} ${session.id}`.toLowerCase().includes(query.toLowerCase());
    const updatedAt = new Date(session.updatedAt).getTime();
    const withinRange = timeRange === "all" || (!Number.isNaN(updatedAt) && Date.now() - updatedAt <= Number(timeRange) * 86_400_000);
    return matches
      && (status === "all" || session.status === status)
      && (agent === "all" || session.agentName === agent)
      && (project === "all" || session.projectName === project)
      && (channel === "all" || session.channelName === channel)
      && withinRange;
  });
  const ranked: ExecutionRankingItem[] = (metric === "round"
    ? executionRuns.map((run) => ({ key: run.active ? `active-${run.sessionId}-${run.directory}` : `${run.id}-${run.sessionId}-${run.startedAt}`, title: run.sessionTitle, project: run.projectName, duration: run.durationSeconds, statusLabel: run.statusLabel, active: run.active, detail: run.active ? "当前轮次" : formatRecentTime(run.endedAt) }))
    : executionSessions.map((session) => ({ key: `${session.sessionId}-${session.directory}`, title: session.sessionTitle, project: session.projectName, duration: session.totalExecutionSeconds, statusLabel: session.statusLabel, active: session.active, detail: `${session.executionCount} 轮` })))
    .filter((item) => item.duration > 0)
    .sort((a, b) => b.duration - a.duration)
    .slice(0, 7);
  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const currentPage = Math.min(pageNumber, pageCount);
  const visibleSessions = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize);
  const updateFilter = (setter: (value: string) => void, value: string) => { setter(value); setPageNumber(1); };
  return (
    <section className="page sessions-page-v2">
      <div className="sessions-heading">
        <PageHeader title="Sessions" description="管理 Session 并分析无人参与时的自动执行效果，持续优化 Agent 的自主执行能力。" />
        <button className="new-session-button" onClick={() => showToast("请在 OpenCode 中新建 Session，创建后将自动同步到这里")}><Plus size={16} />新建 Session</button>
      </div>

      <section className="session-leaderboard">
        <div className="leaderboard-title">
          <Trophy size={19} />
          <div><strong>{metric === "round" ? "单轮自主执行时长排行榜" : "Session 累计自主执行时长排行榜"}</strong><p>{metric === "round" ? "每次用户输入触发一轮计时，到完成或需要人工介入时结束。" : `汇总每个 Session 最近 ${retentionDays} 天内的全部自主执行轮次。`}</p></div>
          <Info size={15} className="leaderboard-info" />
          <span className="retention-note">保留 {retentionDays} 天</span>
          <div className="metric-switch" role="group" aria-label="统计维度"><button className={metric === "round" ? "active" : ""} onClick={() => setMetric("round")}>单轮执行</button><button className={metric === "session" ? "active" : ""} onClick={() => setMetric("session")}>Session 累计</button></div>
        </div>
        {ranked.length ? (
          <div className="leaderboard-content">
            <div className="podium-grid">
              {[ranked[1], ranked[0], ranked[2]].map((item, index) => item ? (
                <article className={`podium-card rank-${[2, 1, 3][index]}`} key={item.key}>
                  <span className="rank-medal">{[2, 1, 3][index]}</span>
                  <strong title={item.title}>{item.title}</strong>
                  <b><LiveDuration seconds={item.duration} running={item.active} /></b>
                  <div><span>{item.project} · {item.detail}</span><span className={`status-pill ${rankingTone(item)}`}>{item.statusLabel}</span></div>
                </article>
              ) : <span className="podium-placeholder" aria-hidden="true" key={`empty-rank-${index}`} />)}
            </div>
            <div className="leaderboard-list">
              <div className="leaderboard-row leaderboard-head"><span>排名</span><span>Session 名称</span><span>自动执行时长</span></div>
              {ranked.slice(3).map((item, index) => <div className="leaderboard-row" key={item.key}><span>{index + 4}</span><strong title={item.title}>{item.title}</strong><time><LiveDuration seconds={item.duration} running={item.active} /></time></div>)}
              {ranked.length <= 3 && <div className="leaderboard-empty">更多有运行记录的 Session 将显示在这里</div>}
              <button className="leaderboard-link" onClick={() => document.querySelector(".sessions-data-panel")?.scrollIntoView({ behavior: "smooth" })}>查看完整列表 <ChevronRight size={14} /></button>
            </div>
          </div>
        ) : <div className="leaderboard-empty large">产生自主执行记录后，这里会展示{metric === "round" ? "单轮" : "Session 累计"}时长排行。</div>}
      </section>

      <section className="sessions-filterbar">
        <SearchBox value={query} onChange={(value) => updateFilter(setQuery, value)} placeholder="搜索 Session 名称、项目或 ID" />
        <FilterSelect label="状态" value={status} onChange={(value) => updateFilter(setStatus, value)} options={[...statusOptions]} />
        <FilterSelect label="Agent" value={agent} onChange={(value) => updateFilter(setAgent, value)} options={agents.map((value) => ({ value, label: value }))} />
        <FilterSelect label="项目" value={project} onChange={(value) => updateFilter(setProject, value)} options={projects.map((value) => ({ value, label: value }))} />
        <FilterSelect label="渠道" value={channel} onChange={(value) => updateFilter(setChannel, value)} options={channels.map((value) => ({ value, label: value }))} />
        <span className="filter-spacer" />
        <FilterSelect label="时间范围" value={timeRange} onChange={(value) => updateFilter(setTimeRange, value)} options={[{ value: "7", label: "近 7 天" }, { value: "30", label: "近 30 天" }]} />
        <button className="filter-refresh" onClick={onRefresh} aria-label="刷新 Session" title="刷新 Session"><RefreshCw size={15} /></button>
      </section>

      <section className="sessions-data-panel">
        <div className="sessions-table" role="table" aria-label="Session 列表">
          <div className="sessions-table-row sessions-table-head" role="row"><span>Session</span><span>状态</span><span>{metric === "round" ? "本轮 / 最近一次" : "累计自主执行"}</span><span>最近输入</span><span>当前模型</span><span>最后活跃</span><span>操作</span></div>
          {visibleSessions.map((session) => <SessionTableRow key={`${session.directory}-${session.id}`} session={session} metric={metric} onOpen={() => void onOpenSession(session)} />)}
          {!visibleSessions.length && <EmptyState icon={MessageSquareText} title="没有匹配的 Session" text="更改筛选条件，或先在 OpenCode 中创建 Session。" />}
        </div>
        <div className="sessions-pagination"><span>共 {filtered.length} 条记录</span><nav aria-label="分页"><button disabled={currentPage === 1} onClick={() => setPageNumber((value) => Math.max(1, value - 1))}><ChevronLeft size={15} /></button>{paginationItems(currentPage, pageCount).map((item, index) => item === "…" ? <span className="pagination-ellipsis" key={`ellipsis-${index}`}>…</span> : <button className={currentPage === item ? "active" : ""} key={item} onClick={() => setPageNumber(Number(item))}>{item}</button>)}<button disabled={currentPage === pageCount} onClick={() => setPageNumber((value) => Math.min(pageCount, value + 1))}><ChevronRight size={15} /></button></nav></div>
      </section>
    </section>
  );
}

const statusOptions = [
  { value: "running", label: "运行中" },
  { value: "waiting_permission", label: "等待权限" },
  { value: "waiting_answer", label: "等待回答" },
  { value: "retrying", label: "重试中" },
  { value: "idle", label: "空闲" },
  { value: "unmonitored", label: "未监控" },
];

function FilterSelect({ label, value, onChange, options }: { label: string; value: string; onChange: (value: string) => void; options: Array<{ value: string; label: string }> }) {
  return <label className="session-filter"><span>{label}：</span><select value={value} onChange={(event) => onChange(event.target.value)}><option value="all">全部</option>{options.map((option) => <option value={option.value} key={option.value}>{option.label}</option>)}</select></label>;
}

function SessionTableRow({ session, metric, onOpen }: { session: SessionView; metric: ExecutionMetric; onOpen: () => void }) {
  const tone = statusTone(session.status);
  const modelLabel = `${session.currentModel || "默认模型"}${session.currentVariant ? ` · ${session.currentVariant}` : ""}`;
  const serverDuration = metric === "round" ? session.latestExecutionSeconds : session.totalExecutionSeconds;
  const duration = useLiveSeconds(serverDuration, isSessionBusy(session));
  return <article className="sessions-table-row" role="row">
    <div className="table-session-cell"><span className={`session-run-icon ${tone}`}>{session.status === "running" ? <Play size={15} /> : <Activity size={15} />}</span><div><strong title={session.title}>{session.title}</strong><span>项目：{session.projectName}<i />Agent：{session.agentName}<i />渠道：{session.channelName}</span><code>{shortID(session.id)}</code></div></div>
    <span><span className={`table-status ${tone}`}>{tone === "running" && <i className="health-dot online" />}{session.statusLabel}</span>{session.statusDetail && <small title={session.statusDetail}>{session.statusDetail}</small>}</span>
    <span className="duration-cell">{duration > 0 ? formatDuration(duration) : "—"}{session.busyForSeconds > 0 && metric === "round" ? <small>↑ LIVE</small> : session.executionCount > 0 && <small>{session.executionCount} 轮</small>}</span>
    <span className="last-input-cell" title={session.hasLastInput ? session.lastInput : "暂无用户输入"}>{session.hasLastInput ? session.lastInput : "—"}</span>
    <span className="model-cell" title={modelLabel}>{modelLabel}</span>
    <time>{formatRecentTime(session.updatedAt)}</time>
    <button className="table-action" onClick={onOpen}>查看详情</button>
  </article>;
}

function rankingTone(item: ExecutionRankingItem): string {
  if (item.active) return "running";
  if (item.statusLabel === "需要介入") return "question";
  return "success";
}

function IntegrationsPage({ title, description, items }: { title: string; description: string; items: IntegrationView[] }) {
  return (
    <section className="page">
      <PageHeader title={title} description={description} />
      <div className="integration-page-grid">{items.map((item) => <IntegrationCard item={item} key={item.id} />)}</div>
      <section className="panel architecture-note">
        <Waypoints size={22} />
        <div><strong>可扩展路由模型</strong><p>底层按 Agent 实例、项目和渠道实例建模。当前仅启用 OpenCode 与飞书，未来接入新类型时无需改变项目路由数据。</p></div>
      </section>
    </section>
  );
}

function EventsPage({ showToast }: { showToast: (message: string) => void }) {
  const [query, setQuery] = useState("");
  const [events, setEvents] = useState<EventView[]>([]);
  const [loading, setLoading] = useState(true);
  const load = useCallback(async () => {
    setLoading(true);
    try {
      const page = await AppService.GetEvents(query, 300);
      setEvents(page.items ?? []);
    } catch (reason) {
      showToast(`读取事件失败：${errorMessage(reason)}`);
    } finally {
      setLoading(false);
    }
  }, [query, showToast]);
  useEffect(() => { void load(); }, [load]);
  const exportEvents = async () => {
    try {
      const path = await AppService.ExportEvents(query);
      if (path) showToast(`已导出到 ${path}`);
    } catch (reason) { showToast(`导出失败：${errorMessage(reason)}`); }
  };
  const clearEvents = async () => {
    if (!window.confirm("确定清空事件记录吗？此操作无法撤销。")) return;
    try { await AppService.ClearEvents(); await load(); showToast("事件记录已清空"); } catch (reason) { showToast(`清空失败：${errorMessage(reason)}`); }
  };
  return (
    <section className="page">
      <PageHeader title="事件记录" description="保留最近30天、最多10,000条运行事件；密钥、Token 和 Prompt 不会写入这里。" />
      <section className="panel full-panel">
        <div className="toolbar">
          <SearchBox value={query} onChange={setQuery} placeholder="搜索消息、来源或事件类型" />
          <button className="secondary-button" onClick={() => void load()}><RefreshCw size={16} className={loading ? "spin" : ""} />刷新</button>
          <span className="toolbar-spacer" />
          <button className="secondary-button" onClick={() => void exportEvents()}><Download size={16} />导出</button>
          <button className="danger-button" onClick={() => void clearEvents()}><Trash2 size={16} />清空</button>
        </div>
        <div className="event-table table-scroll">
          <div className="event-row event-head"><span>时间</span><span>级别</span><span>来源</span><span>事件</span><span>消息</span></div>
          {events.map((event) => <div className="event-row" key={event.id}><time>{formatDateTime(event.createdAt)}</time><span><span className={`level-badge ${event.level}`}>{event.level}</span></span><span>{event.source}</span><code>{event.type}</code><span className="event-message">{event.message}</span></div>)}
          {!events.length && !loading && <EmptyState icon={FileClock} title="暂无事件记录" text="服务状态和路由变更会显示在这里。" />}
        </div>
      </section>
    </section>
  );
}

function SettingsPage({ showToast, onSaved }: { showToast: (message: string) => void; onSaved: () => void }) {
  const [settings, setSettings] = useState<SettingsView | null>(null);
  const [form, setForm] = useState<SettingsInput | null>(null);
  const [autostart, setAutostart] = useState(false);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    void Promise.all([AppService.GetSettings(), AppService.GetAutostart()]).then(([value, auto]) => {
      setSettings(value); setForm(settingsToInput(value)); setAutostart(auto);
    }).catch((reason) => showToast(`读取设置失败：${errorMessage(reason)}`));
  }, [showToast]);
  const locked = (field: string) => settings?.environmentOverrides?.[field];
  const update = <K extends keyof SettingsInput>(key: K, value: SettingsInput[K]) => setForm((current) => current ? { ...current, [key]: value } : current);
  const save = async () => {
    if (!form) return;
    setSaving(true);
    try { const value = await AppService.SaveSettings(form); setSettings(value); setForm(settingsToInput(value)); showToast("配置已保存，服务已重新加载"); onSaved(); } catch (reason) { showToast(`保存失败：${errorMessage(reason)}`); } finally { setSaving(false); }
  };
  const toggleAutostart = async (enabled: boolean) => {
    try { await AppService.SetAutostart(enabled); setAutostart(enabled); showToast(enabled ? "已启用开机自启" : "已关闭开机自启"); } catch (reason) { showToast(`设置失败：${errorMessage(reason)}`); }
  };
  if (!settings || !form) return <div className="center-loading"><LoaderCircle className="spin" />正在读取设置</div>;
  return (
    <section className="page settings-page">
      <PageHeader title="设置" description="管理 OpenCode、飞书、通知和桌面应用行为。" />
      {settings.configError && <div className="banner warning-banner"><CircleAlert size={18} /><span>{settings.configError}</span></div>}
      <div className="settings-layout">
        <section className="panel settings-section">
          <h2>OpenCode</h2><p className="section-description">桌面应用只检测并连接用户启动的 OpenCode Server。</p>
          <FormField label="服务地址" lockedBy={locked("opencode.base_url")}><input value={form.openCodeBaseUrl} disabled={!!locked("opencode.base_url")} onChange={(event) => update("openCodeBaseUrl", event.target.value)} /></FormField>
          <FormField label="限定目录" hint="留空时发现 OpenCode 中的全部项目" lockedBy={locked("opencode.directory")}><input value={form.openCodeDirectory} disabled={!!locked("opencode.directory")} onChange={(event) => update("openCodeDirectory", event.target.value)} placeholder="留空" /></FormField>
          <div className="form-grid two"><FormField label="用户名" lockedBy={locked("opencode.username")}><input value={form.openCodeUsername} disabled={!!locked("opencode.username")} onChange={(event) => update("openCodeUsername", event.target.value)} /></FormField><FormField label="密码" hint={settings.openCodePasswordSet ? "已配置；留空表示保持不变" : "当前未配置"} lockedBy={locked("opencode.password")}><input type="password" value={form.openCodePassword} disabled={!!locked("opencode.password")} onChange={(event) => update("openCodePassword", event.target.value)} placeholder="••••••••" /></FormField></div>
          <ToggleRow label="允许连接远程地址" description="关闭时仅允许 localhost 和回环地址" checked={form.allowRemote} onChange={(value) => update("allowRemote", value)} />
        </section>

        <section className="panel settings-section">
          <h2>飞书渠道</h2><p className="section-description">App Secret 按你的选择明文存放在 config.yaml，界面不会回显真实内容。</p>
          <div className="plaintext-warning"><ShieldAlert size={18} /><span>查看、复制或导出配置文件时，请确认不会泄露 App Secret。</span></div>
          <FormField label="App ID" lockedBy={locked("feishu.app_id")}><input value={form.feishuAppId} disabled={!!locked("feishu.app_id")} onChange={(event) => update("feishuAppId", event.target.value)} /></FormField>
          <FormField label="App Secret" hint={settings.feishuAppSecretSet ? "已配置；留空表示保持不变" : "尚未配置"} lockedBy={locked("feishu.app_secret")}><input type="password" value={form.feishuAppSecret} disabled={!!locked("feishu.app_secret")} onChange={(event) => update("feishuAppSecret", event.target.value)} placeholder="••••••••••••" /></FormField>
          <FormField label="Chat ID" hint="可留空并通过 /bind 配对" lockedBy={locked("feishu.chat_id")}><input value={form.feishuChatId} disabled={!!locked("feishu.chat_id")} onChange={(event) => update("feishuChatId", event.target.value)} /></FormField>
          <FormField label="允许的用户" hint="多个用户 ID 用逗号分隔" lockedBy={locked("security.allowed_users")}><input value={(form.allowedUsers ?? []).join(", ")} disabled={!!locked("security.allowed_users")} onChange={(event) => update("allowedUsers", event.target.value.split(",").map((item) => item.trim()).filter(Boolean))} /></FormField>
        </section>

        <section className="panel settings-section">
          <h2>Handoff 与通知</h2>
          <div className="form-grid two"><FormField label="轮询间隔"><input value={form.pollingInterval} onChange={(event) => update("pollingInterval", event.target.value)} /></FormField><FormField label="最大输出字符" lockedBy={locked("handoff.max_output_chars")}><input type="number" value={form.maxOutputChars} disabled={!!locked("handoff.max_output_chars")} onChange={(event) => update("maxOutputChars", Number(event.target.value))} /></FormField></div>
          <FormField label="自主执行记录保留天数" hint="默认 30 天；保存后会立即清理超过保留期的统计记录" lockedBy={locked("analytics.retention_days")}><input type="number" min="1" max="3650" value={form.executionRetentionDays} disabled={!!locked("analytics.retention_days")} onChange={(event) => update("executionRetentionDays", Number(event.target.value))} /></FormField>
          <ToggleRow label="通知 Session 空闲" checked={form.notifyIdle} disabled={!!locked("handoff.notify_idle")} onChange={(value) => update("notifyIdle", value)} />
          <ToggleRow label="通知运行错误" checked={form.notifyError} disabled={!!locked("handoff.notify_error")} onChange={(value) => update("notifyError", value)} />
          <ToggleRow label="转发 Question" checked={form.notifyQuestion} disabled={!!locked("handoff.notify_question")} onChange={(value) => update("notifyQuestion", value)} />
          <ToggleRow label="转发 Permission" checked={form.notifyPermission} disabled={!!locked("handoff.notify_permission")} onChange={(value) => update("notifyPermission", value)} />
        </section>

        <section className="panel settings-section">
          <h2>桌面应用</h2>
          <ToggleRow label="开机自动启动" description="默认关闭；启用后以托盘模式启动" checked={autostart} onChange={(value) => void toggleAutostart(value)} />
          <FormField label="日志级别"><select value={form.loggingLevel} onChange={(event) => update("loggingLevel", event.target.value)}><option value="debug">Debug</option><option value="info">Info</option><option value="warn">Warn</option><option value="error">Error</option></select></FormField>
          <div className="path-list"><PathRow label="配置文件" value={settings.paths.configPath} /><PathRow label="数据库" value={settings.paths.storePath} /><PathRow label="日志" value={settings.paths.logPath} /></div>
          <div className="desktop-actions"><button className="secondary-button" onClick={() => void AppService.OpenDataDirectory()}><Folder size={16} />打开数据目录</button><button className="danger-button" onClick={() => { if (window.confirm("退出后将停止 Handoff 服务，确定继续吗？")) AppService.Quit(); }}><Power size={16} />退出应用</button></div>
        </section>
      </div>
      <div className="settings-savebar"><span>保存后会安全重启 Handoff 引擎，不会启动或停止 OpenCode。</span><button className="primary-button" disabled={saving} onClick={() => void save()}>{saving ? <LoaderCircle size={16} className="spin" /> : <Check size={16} />}保存设置</button></div>
    </section>
  );
}

function ProjectTable({ projects, onRoute, roomy = false }: { projects: ProjectView[]; onRoute: (id: string, enabled: boolean) => Promise<void>; roomy?: boolean }) {
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const toggle = async (project: ProjectView) => {
    setBusy((current) => new Set(current).add(project.id));
    try { await onRoute(project.id, !project.routeEnabled); } finally { setBusy((current) => { const next = new Set(current); next.delete(project.id); return next; }); }
  };
  return (
    <div className={roomy ? "project-table roomy" : "project-table"}>
      <div className="project-row project-head"><span>启用</span><span>项目名称</span><span>本地目录路径</span><span>最近对话</span><span>Agent</span><span>渠道</span><span>状态</span></div>
      {projects.map((project) => <div className="project-row" key={project.id}><span><Switch checked={project.routeEnabled} disabled={busy.has(project.id)} onChange={() => void toggle(project)} /></span><strong title={project.name}>{project.name}</strong><code title={project.directory}>{project.directory}</code><span className="project-recent" title={project.lastConversationAt ? formatDateTime(project.lastConversationAt) : "暂无对话"}>{formatRecentTime(project.lastConversationAt)}</span><span className="type-chip">{project.agentName}</span><span className={project.routeEnabled ? "type-chip blue-chip" : "type-chip muted-chip"}>{project.routeEnabled ? project.channelName : "—"}</span><span className={project.routeEnabled ? "status-pill success" : "status-pill neutral"}>{project.status}</span></div>)}
      {!projects.length && <EmptyState icon={Folder} title="没有发现项目" text="启动 OpenCode 并刷新项目后即可在这里选择接入范围。" />}
    </div>
  );
}

function SessionCard({ session, compact = false, onOpen }: { session: SessionView; compact?: boolean; onOpen: () => void }) {
  const tone = statusTone(session.status);
  const modelLabel = `${session.currentModel || "OpenCode 默认/尚未识别"}${session.currentVariant ? ` · ${session.currentVariant}` : ""}`;
  const busy = isSessionBusy(session);
  const busyForSeconds = useLiveSeconds(session.busyForSeconds, busy);
  const sinceLastInputSeconds = useLiveSeconds(session.sinceLastInputSeconds, session.hasLastInput);
  return (
    <article className={`session-card ${tone} ${compact ? "compact" : ""}`}>
      <div className="session-card-head"><span className={`session-icon ${tone}`}>{session.status === "running" ? <Play size={17} /> : session.status.startsWith("waiting") ? <Clock3 size={17} /> : session.status === "retrying" ? <RotateCcw size={17} /> : <Activity size={17} />}</span><div className="session-title"><strong>{session.title}</strong><code>{shortID(session.id)}</code></div><span className={`status-pill ${tone}`}>{session.statusLabel}</span></div>
      <div className="session-meta"><span>项目：{session.projectName}</span><i /> <span>Agent：{session.agentName}</span><i /> <span>渠道：{session.channelName}</span></div>
      {session.statusDetail && <p className="status-detail">{session.statusDetail}</p>}
      <div className="session-model-row" title={`模型：${modelLabel}`}><Bot size={14} /><span>模型：{modelLabel}</span></div>
      <div className="session-time-row"><span>{busy ? `当前忙碌 ${formatDuration(busyForSeconds)}` : "当前未忙碌"}</span><span>{session.hasLastInput ? `距最后用户输入 ${formatDuration(sinceLastInputSeconds)}` : "暂无用户输入"}</span></div>
      {!compact && session.hasLastInput && <details className="last-input"><summary>完整最后输入</summary><pre>{session.lastInput}</pre></details>}
      <div className="session-actions"><button className="secondary-button" onClick={onOpen}><ExternalLink size={15} />在 OpenCode 中打开</button></div>
    </article>
  );
}

function IntegrationCard({ item, compact = false }: { item: IntegrationView; compact?: boolean }) {
  const Icon = item.type === "feishu" ? Link2 : item.type === "future" ? Waypoints : CodeXml;
  return <article className={`integration-card ${compact ? "compact" : ""} ${item.comingSoon ? "disabled" : ""}`}><span className={`integration-icon ${item.status}`}><Icon size={23} /></span><div><strong>{item.name}</strong><span className={item.status === "connected" ? "connected-text" : "muted"}><i className={`health-dot ${item.status === "connected" ? "online" : "offline"}`} />{item.statusLabel}</span>{!compact && item.endpoint && <code>{item.endpoint}</code>}</div>{item.comingSoon && <span className="coming-badge">即将支持</span>}</article>;
}

function PanelHeader({ title, action }: { title: string; action?: React.ReactNode }) { return <div className="panel-header"><h2>{title}</h2>{action}</div>; }
function PageHeader({ title, description }: { title: string; description: string }) { return <div className="page-header"><div><h1>{title}</h1><p>{description}</p></div></div>; }
function ProjectToolbar({ projects }: { projects: ProjectView[]; compact?: boolean }) { return <div className="mini-toolbar"><span>{projects.length} 个已发现项目</span><span>{projects.filter((item) => item.routeEnabled).length} 个已接入</span></div>; }
function SearchBox({ value, onChange, placeholder }: { value: string; onChange: (value: string) => void; placeholder: string }) { return <label className="search-box"><Search size={17} /><input value={value} onChange={(event) => onChange(event.target.value)} placeholder={placeholder} /></label>; }
function Switch({ checked, disabled, onChange }: { checked: boolean; disabled?: boolean; onChange: () => void }) { return <button type="button" role="switch" aria-checked={checked} disabled={disabled} className={`switch ${checked ? "checked" : ""}`} onClick={onChange}><span /></button>; }
function ToggleRow({ label, description, checked, disabled, onChange }: { label: string; description?: string; checked: boolean; disabled?: boolean; onChange: (value: boolean) => void }) { return <div className="toggle-row"><div><strong>{label}</strong>{description && <p>{description}</p>}</div><Switch checked={checked} disabled={disabled} onChange={() => onChange(!checked)} /></div>; }
function FormField({ label, hint, lockedBy, children }: { label: string; hint?: string; lockedBy?: string; children: React.ReactNode }) { return <label className="form-field"><span>{label}{lockedBy && <em>由 {lockedBy} 控制</em>}</span>{children}{hint && <small>{hint}</small>}</label>; }
function PathRow({ label, value }: { label: string; value: string }) { return <div className="path-row"><span>{label}</span><code title={value}>{value}</code></div>; }
function EmptyState({ icon: Icon, title, text }: { icon: typeof Activity; title: string; text: string }) { return <div className="empty-state"><Icon size={27} /><strong>{title}</strong><p>{text}</p></div>; }

function settingsToInput(settings: SettingsView): SettingsInput {
  return {
    openCodeBaseUrl: settings.openCodeBaseUrl,
    openCodeDirectory: settings.openCodeDirectory,
    openCodeUsername: settings.openCodeUsername,
    openCodePassword: "",
    clearOpenCodePassword: false,
    allowRemote: settings.allowRemote,
    feishuAppId: settings.feishuAppId,
    feishuAppSecret: "",
    feishuChatId: settings.feishuChatId,
    allowedUsers: settings.allowedUsers ?? [],
    pollingInterval: settings.pollingInterval,
    maxOutputChars: settings.maxOutputChars,
    notifyIdle: settings.notifyIdle,
    notifyError: settings.notifyError,
    notifyQuestion: settings.notifyQuestion,
    notifyPermission: settings.notifyPermission,
    loggingLevel: settings.loggingLevel,
    executionRetentionDays: settings.executionRetentionDays,
  };
}

function normaliseDashboard(value: Dashboard): Dashboard { return { ...value, projects: value.projects ?? [], sessions: value.sessions ?? [], executionRuns: value.executionRuns ?? [], executionSessions: value.executionSessions ?? [], agents: value.agents ?? [], channels: value.channels ?? [] }; }
function errorMessage(reason: unknown): string { return reason instanceof Error ? reason.message : String(reason); }
function serviceStateLabel(state: string): string { return ({ conflict: "等待关闭 CLI", config_error: "配置待完善", error: "服务异常", stopped: "服务已停止", loading: "正在启动" } as Record<string, string>)[state] ?? "未运行"; }
function statusTone(status: string): string { return ({ running: "running", waiting_permission: "permission", waiting_answer: "question", retrying: "retry", idle: "idle", unmonitored: "neutral" } as Record<string, string>)[status] ?? "neutral"; }
function isSessionBusy(session: SessionView): boolean { return session.status === "running" || session.status === "retrying"; }
function LiveDuration({ seconds, running }: { seconds: number; running: boolean }) { return <>{formatDuration(useLiveSeconds(seconds, running))}</>; }
function useLiveSeconds(serverSeconds: number, running: boolean): number {
  const normalise = (value: number) => Math.max(0, Math.floor(value));
  const [seconds, setSeconds] = useState(() => normalise(serverSeconds));
  const previousServerSeconds = useRef(normalise(serverSeconds));

  useEffect(() => {
    const next = normalise(serverSeconds);
    const previous = previousServerSeconds.current;
    previousServerSeconds.current = next;
    setSeconds((current) => running && next >= previous ? Math.max(current, next) : next);
  }, [serverSeconds, running]);

  useEffect(() => {
    if (!running) return;
    const timer = window.setInterval(() => setSeconds((current) => current + 1), 1000);
    return () => window.clearInterval(timer);
  }, [running]);

  return seconds;
}
function shortID(id: string): string { return id.length > 12 ? `${id.slice(0, 7)}…${id.slice(-3)}` : id; }
function formatDuration(value: number): string { const seconds = Math.max(0, Math.floor(value)); if (seconds < 60) return `${seconds} 秒`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes} 分 ${seconds % 60} 秒`; const hours = Math.floor(minutes / 60); return `${hours} 小时 ${minutes % 60} 分 ${seconds % 60} 秒`; }
function formatDateTime(value: string): string { const date = new Date(value); return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN", { hour12: false }); }
function formatRecentTime(value: string): string { const date = new Date(value); const elapsed = Date.now() - date.getTime(); if (!value || Number.isNaN(date.getTime()) || date.getUTCFullYear() <= 1) return "暂无"; if (elapsed < 60_000) return "刚刚"; const minutes = Math.floor(elapsed / 60_000); if (minutes < 60) return `${minutes} 分钟前`; const hours = Math.floor(minutes / 60); if (hours < 24) return `${hours} 小时前`; const days = Math.floor(hours / 24); if (days < 30) return `${days} 天前`; return date.toLocaleDateString("zh-CN"); }
function uniqueValues(values: string[]): string[] { return [...new Set(values.filter(Boolean))].sort((a, b) => a.localeCompare(b, "zh-CN")); }
function paginationItems(current: number, total: number): Array<number | "…"> {
  if (total <= 5) return Array.from({ length: total }, (_, index) => index + 1);
  if (current <= 3) return [1, 2, 3, "…", total];
  if (current >= total - 2) return [1, "…", total - 2, total - 1, total];
  return [1, "…", current, "…", total];
}

export default App;
