import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  Bell,
  Bot,
  Boxes,
  Check,
  CircleCheck,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  CircleAlert,
  Clock3,
  CodeXml,
  Copy,
  Download,
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
  Repeat2,
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
import LoopsPage from "./LoopsPage";
import { saveInterfaceDensity, type InterfaceDensity } from "./uiPreferences";
import * as AppService from "../bindings/github.com/Hans2573/OpenCode-Handoff/appservice";
import type {
  Dashboard,
  ExecutionRunView,
  ExecutionSessionView,
  EventView,
  IntegrationView,
  ProjectView,
  SessionView,
  SessionDetailView,
  SessionOperationView,
  SettingsInput,
  SettingsView,
} from "../bindings/github.com/Hans2573/OpenCode-Handoff/internal/desktop/models";

type Page = "overview" | "projects" | "sessions" | "agents" | "loops" | "channels" | "events" | "settings";

const navigation: Array<{ id: Page; label: string; icon: typeof Activity }> = [
  { id: "overview", label: "总览", icon: Boxes },
  { id: "projects", label: "项目接入", icon: FolderCheck },
  { id: "sessions", label: "Sessions", icon: Activity },
  { id: "loops", label: "Loop 工程", icon: Repeat2 },
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
    feishuPairingRequired: false,
    feishuPairingCode: "",
    configValid: true,
    openCodeUrl: "http://127.0.0.1:4096",
  },
  summary: { connectedProjects: 0, completedSessions: 0, pendingActions: 0, suspectedStalls: 0, stalledSessions: 0, connectedChannels: 0 },
  projects: [],
  sessions: [],
  executionRuns: [],
  executionSessions: [],
  executionRetentionDays: 30,
  agents: [],
  channels: [],
};

function App({ initialInterfaceDensity }: { initialInterfaceDensity: InterfaceDensity }) {
  const [page, setPage] = useState<Page>("overview");
  const [interfaceDensity, setInterfaceDensity] = useState<InterfaceDensity>(initialInterfaceDensity);
  const [dashboard, setDashboard] = useState<Dashboard>(emptyDashboard);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [toast, setToast] = useState("");
	const [goalSession, setGoalSession] = useState<SessionView | null>(null);
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

  const copyPairingCommand = async () => {
    const command = `/bind ${dashboard.service.feishuPairingCode}`;
    try {
      await navigator.clipboard.writeText(command);
      showToast("飞书绑定命令已复制");
    } catch (reason) {
      showToast(`复制失败：${errorMessage(reason)}`);
    }
  };

  const changeInterfaceDensity = (density: InterfaceDensity) => {
    setInterfaceDensity(density);
    saveInterfaceDensity(density);
    showToast(`已切换为${density === "standard" ? "标准" : "紧凑"}界面`);
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
            <button className="icon-button notification-button" aria-label="通知" title={dashboard.summary.stalledSessions ? `${dashboard.summary.stalledSessions} 个 Session 长时间停滞` : dashboard.summary.suspectedStalls ? `${dashboard.summary.suspectedStalls} 个 Session 疑似停滞` : "暂无停滞提醒"} onClick={() => setPage("sessions")}><Bell size={18} />{dashboard.summary.stalledSessions + dashboard.summary.suspectedStalls > 0 && <span>{dashboard.summary.stalledSessions + dashboard.summary.suspectedStalls}</span>}</button>
            <span className="divider vertical" />
            <button className="icon-button" title="最小化到托盘" onClick={() => void AppService.HideWindow()}><Minus size={19} /></button>
            <button className="icon-button" title="设置" onClick={() => setPage("settings")}><Settings size={19} /></button>
          </div>
        </header>

        <div className={`content${page === "sessions" ? " sessions-content" : ""}`}>
          {error && <div className="banner error-banner"><CircleAlert size={18} /><span>{error}</span><button onClick={() => void loadDashboard()}>重试</button></div>}
          {dashboard.service.state === "conflict" && (
            <div className="banner warning-banner"><ShieldAlert size={18} /><span>{dashboard.service.message}</span><button onClick={() => void retryService()}>关闭 CLI 后重试</button></div>
          )}
          {dashboard.service.state === "config_error" && (
            <div className="banner warning-banner"><SlidersHorizontal size={18} /><span>{dashboard.service.message}</span><button onClick={() => setPage("settings")}>打开设置</button></div>
          )}
          {dashboard.service.feishuPairingRequired && (
            <div className="banner warning-banner pairing-banner">
              <Unplug size={18} />
              <div className="pairing-message">
                <strong>飞书尚未配对，通知暂时无法发送</strong>
                <span>请在飞书机器人会话发送 <code>/bind {dashboard.service.feishuPairingCode}</code></span>
              </div>
              <button type="button" title="复制飞书绑定命令" onClick={() => void copyPairingCommand()}><Copy size={16} />复制命令</button>
            </div>
          )}

          {page === "overview" && <Overview dashboard={dashboard} loading={loading} onNavigate={setPage} onRoute={changeRoute} onRefresh={refreshProjects} />}
          {page === "projects" && <ProjectsPage projects={dashboard.projects ?? []} loading={loading} onRoute={changeRoute} onRefresh={refreshProjects} />}
          {page === "sessions" && <SessionsPage sessions={dashboard.sessions ?? []} executionRuns={dashboard.executionRuns ?? []} executionSessions={dashboard.executionSessions ?? []} retentionDays={dashboard.executionRetentionDays || 30} onAddToGoal={(session) => { setGoalSession(session); setPage("loops"); }} onRefresh={() => void loadDashboard()} showToast={showToast} />}
          {page === "agents" && <IntegrationsPage title="Agents" description="本地 Agent 实例及连接状态" items={dashboard.agents ?? []} />}
          {page === "loops" && <LoopsPage projects={dashboard.projects ?? []} sessions={dashboard.sessions ?? []} initialSession={goalSession} onInitialSessionConsumed={() => setGoalSession(null)} showToast={showToast} />}
          {page === "channels" && <IntegrationsPage title="渠道" description="项目事件可以路由到一个或多个通信渠道" items={dashboard.channels ?? []} />}
          {page === "events" && <EventsPage showToast={showToast} />}
          {page === "settings" && <SettingsPage interfaceDensity={interfaceDensity} onInterfaceDensityChange={changeInterfaceDensity} showToast={showToast} onSaved={() => void loadDashboard()} />}
        </div>
      </main>
      {toast && <div className="toast"><Check size={17} />{toast}</div>}
    </div>
  );
}

function Overview({ dashboard, loading, onNavigate, onRoute, onRefresh }: {
  dashboard: Dashboard;
  loading: boolean;
  onNavigate: (page: Page) => void;
  onRoute: (id: string, enabled: boolean) => Promise<void>;
  onRefresh: () => Promise<void>;
}) {
  const summaryCards = [
    { label: "已接入项目", value: dashboard.summary.connectedProjects, icon: Folder, tone: "blue" },
    { label: "已完成 Sessions", value: dashboard.summary.completedSessions, icon: CircleCheck, tone: "green" },
    { label: "等待操作", value: dashboard.summary.pendingActions, icon: Clock3, tone: "orange" },
    { label: "停滞 Sessions", value: dashboard.summary.stalledSessions, detail: dashboard.summary.suspectedStalls ? `${dashboard.summary.suspectedStalls} 个疑似停滞` : "暂无疑似停滞", icon: CircleAlert, tone: "red" },
    { label: "已连接渠道", value: dashboard.summary.connectedChannels, icon: Link2, tone: "purple" },
  ];
  const activeSessions = (dashboard.sessions ?? []).filter((session) => !["idle", "unmonitored"].includes(session.status)).sort((a, b) => activityRank(b.activityLevel) - activityRank(a.activityLevel)).slice(0, 5);
  return (
    <section className="page overview-page">
      <div className="summary-grid">
        {summaryCards.map(({ label, value, detail, icon: Icon, tone }) => (
          <div className="summary-card" key={label}>
            <div><span>{label}</span><strong>{value}</strong>{detail && <small>{detail}</small>}</div>
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
            {activeSessions.length ? activeSessions.map((session) => <SessionCard key={session.id} session={session} compact />) : <EmptyState icon={Activity} title="当前没有运行中的 Session" text="已接入项目出现活动后会显示在这里。" />}
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
type ExecutionRankingItem = { key: string; title: string; context: string; duration: number; statusLabel: string; active: boolean; detail: string };
const sessionPageSizes = [5, 8, 10, 15, 20] as const;
const sessionLeaderboardPreferenceKey = "agent-handoff:sessions:leaderboard-expanded";

function SessionsPage({ sessions, executionRuns, executionSessions, retentionDays, onAddToGoal, onRefresh, showToast }: {
  sessions: SessionView[];
  executionRuns: ExecutionRunView[];
  executionSessions: ExecutionSessionView[];
  retentionDays: number;
  onAddToGoal: (session: SessionView) => void;
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
  const [selectedSessionKey, setSelectedSessionKey] = useState("");
  const [pageNumber, setPageNumber] = useState(1);
  const [autoPageSize, setAutoPageSize] = useState<number>(8);
  const [manualPageSize, setManualPageSize] = useState<number | null>(null);
  const [compactLayout, setCompactLayout] = useState(false);
  const [leaderboardPreferredOpen, setLeaderboardPreferredOpen] = useState(() => {
    try {
      return window.localStorage.getItem(sessionLeaderboardPreferenceKey) !== "false";
    } catch {
      return true;
    }
  });
  const pageRef = useRef<HTMLElement | null>(null);
  const tableViewportRef = useRef<HTMLDivElement | null>(null);
  const autoPageSizeRef = useRef(autoPageSize);
  const pageSize = manualPageSize ?? autoPageSize;
  const leaderboardOpen = leaderboardPreferredOpen && !compactLayout;
  const agents = uniqueValues(sessions.map((item) => item.agentName));
  const projects = uniqueValues(sessions.map((item) => item.projectName));
  const channels = uniqueValues(sessions.map((item) => item.channelName));
  const sessionDetails = new Map(sessions.map((session) => [`${session.id}\u0000${session.directory}`, session]));
  const selectedSession = selectedSessionKey ? sessionDetails.get(selectedSessionKey) ?? null : null;
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
    ? executionRuns.map((run) => { const session = sessionDetails.get(`${run.sessionId}\u0000${run.directory}`); return { key: run.active ? `active-${run.sessionId}-${run.directory}` : `${run.id}-${run.sessionId}-${run.startedAt}`, title: run.sessionTitle, context: session?.hasLastInput ? session.lastInput : "—", duration: run.durationSeconds, statusLabel: run.statusLabel, active: run.active, detail: run.active ? "当前轮次" : formatRecentTime(run.endedAt) }; })
    : executionSessions.map((session) => ({ key: `${session.sessionId}-${session.directory}`, title: session.sessionTitle, context: session.projectName || "—", duration: session.totalExecutionSeconds, statusLabel: session.statusLabel, active: session.active, detail: `${session.executionCount} 轮` })))
    .filter((item) => item.duration > 0)
    .sort((a, b) => b.duration - a.duration)
    .slice(0, 6);
  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const currentPage = Math.min(pageNumber, pageCount);
  const visibleSessions = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize);
  const updateFilter = (setter: (value: string) => void, value: string) => { setter(value); setPageNumber(1); };

  useEffect(() => {
    try {
      window.localStorage.setItem(sessionLeaderboardPreferenceKey, String(leaderboardPreferredOpen));
    } catch {
      // UI preferences are best-effort only.
    }
  }, [leaderboardPreferredOpen]);

  useEffect(() => {
    const pageElement = pageRef.current;
    if (!pageElement) return;
    const updateCompactLayout = (width: number, height: number) => setCompactLayout(height < 700 || width < 900);
    updateCompactLayout(pageElement.clientWidth, pageElement.clientHeight);
    const observer = new ResizeObserver(([entry]) => updateCompactLayout(entry.contentRect.width, entry.contentRect.height));
    observer.observe(pageElement);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const viewport = tableViewportRef.current;
    if (!viewport) return;
    const updatePageSize = (height: number) => {
      const rootStyles = window.getComputedStyle(document.documentElement);
      const rowHeight = Number.parseFloat(rootStyles.getPropertyValue("--table-row-height")) || 60;
      const headHeight = Number.parseFloat(rootStyles.getPropertyValue("--table-head-height")) || 40;
      const rowCapacity = Math.max(1, Math.floor((height - headHeight) / rowHeight));
      const nextSize = [...sessionPageSizes].reverse().find((size) => size <= rowCapacity) ?? sessionPageSizes[0];
      const previousSize = autoPageSizeRef.current;
      if (nextSize === previousSize) return;
      autoPageSizeRef.current = nextSize;
      setAutoPageSize(nextSize);
      if (manualPageSize === null) {
        setPageNumber((value) => Math.floor(((value - 1) * previousSize) / nextSize) + 1);
      }
    };
    updatePageSize(viewport.clientHeight);
    const observer = new ResizeObserver(([entry]) => updatePageSize(entry.contentRect.height));
    observer.observe(viewport);
    return () => observer.disconnect();
  }, [manualPageSize]);

  useEffect(() => {
    setPageNumber((value) => Math.min(value, pageCount));
  }, [pageCount]);

  const changePageSize = (value: string) => {
    const nextManualSize = value === "auto" ? null : Number(value);
    const nextPageSize = nextManualSize ?? autoPageSize;
    const firstVisibleIndex = (currentPage - 1) * pageSize;
    setManualPageSize(nextManualSize);
    setPageNumber(Math.floor(firstVisibleIndex / nextPageSize) + 1);
  };

  const collapseLeaderboard = () => setLeaderboardPreferredOpen(false);

  return (
    <section ref={pageRef} className={`page sessions-page-v2${compactLayout ? " compact-layout" : ""}`}>
      <div className="sessions-heading">
        <PageHeader title="Sessions" description="管理 Session 并分析无人参与时的自动执行效果，持续优化 Agent 的自主执行能力。" />
        <button className="new-session-button" onClick={() => showToast("请在 OpenCode 中新建 Session，创建后将自动同步到这里")}><Plus size={16} />新建 Session</button>
      </div>

      <section className={`session-leaderboard${leaderboardOpen ? "" : " collapsed"}`}>
        <div className="leaderboard-title">
          <Trophy size={19} />
          <div><strong>{metric === "round" ? "单轮自主执行时长排行榜" : "Session 累计自主执行时长排行榜"}</strong><p>{metric === "round" ? "每次用户输入触发一轮计时，到完成或需要人工介入时结束。" : `汇总每个 Session 最近 ${retentionDays} 天内的全部自主执行轮次。`}</p></div>
          <Info size={15} className="leaderboard-info" />
          <span className="retention-note">保留 {retentionDays} 天</span>
          <div className="metric-switch" role="group" aria-label="统计维度"><button className={metric === "round" ? "active" : ""} onClick={() => setMetric("round")}>单轮执行</button><button className={metric === "session" ? "active" : ""} onClick={() => setMetric("session")}>Session 累计</button></div>
          <button className="leaderboard-toggle" disabled={compactLayout} aria-expanded={leaderboardOpen} title={compactLayout ? "当前窗口高度下已自动收起排行榜" : leaderboardOpen ? "收起排行榜" : "展开排行榜"} onClick={() => setLeaderboardPreferredOpen((value) => !value)}>{leaderboardOpen ? <ChevronUp size={15} /> : <ChevronDown size={15} />}</button>
        </div>
        {leaderboardOpen && (ranked.length ? (
          <div className="leaderboard-content">
            <div className="podium-grid">
              {[ranked[1], ranked[0], ranked[2]].map((item, index) => item ? (
                <article className={`podium-card rank-${[2, 1, 3][index]}`} key={item.key}>
                  <span className="rank-medal">{[2, 1, 3][index]}</span>
                  <strong title={item.title}>{item.title}</strong>
                  <b><LiveDuration seconds={item.duration} running={item.active} /></b>
                  <div><span title={`${metric === "round" ? "上次输入" : "项目"}：${item.context} · ${item.detail}`}>{metric === "round" ? "上次输入" : "项目"}：{item.context} · {item.detail}</span><span className={`status-pill ${rankingTone(item)}`}>{item.statusLabel}</span></div>
                </article>
              ) : <span className="podium-placeholder" aria-hidden="true" key={`empty-rank-${index}`} />)}
            </div>
            <div className="leaderboard-list">
              <div className="leaderboard-row leaderboard-head"><span>排名</span><span>Session 名称</span><span>{metric === "round" ? "上次输入" : "项目名称"}</span><span>自动执行时长</span></div>
              {ranked.slice(3).map((item, index) => <div className="leaderboard-row" key={item.key}><span>{index + 4}</span><strong title={item.title}>{item.title}</strong><span className="leaderboard-context" title={item.context}>{item.context}</span><time><LiveDuration seconds={item.duration} running={item.active} /></time></div>)}
              {ranked.length <= 3 && <div className="leaderboard-empty">更多有运行记录的 Session 将显示在这里</div>}
              <button className="leaderboard-link" onClick={collapseLeaderboard}>收起排行并查看全部 <ChevronRight size={14} /></button>
            </div>
          </div>
        ) : <div className="leaderboard-empty large">产生自主执行记录后，这里会展示{metric === "round" ? "单轮" : "Session 累计"}时长排行。</div>)}
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
        <div ref={tableViewportRef} className="sessions-table-scroll">
          <div className="sessions-table" role="table" aria-label="Session 列表">
            <div className="sessions-table-row sessions-table-head" role="row"><span>Session</span><span>状态</span><span>{metric === "round" ? "本轮 / 最近一次" : "累计自主执行"}</span><span>最近输入</span><span>当前模型</span><span>最后操作</span><span>操作</span></div>
            {visibleSessions.map((session) => <SessionTableRow key={`${session.directory}-${session.id}`} session={session} metric={metric} onDetails={() => setSelectedSessionKey(`${session.id}\u0000${session.directory}`)} onAddToGoal={() => onAddToGoal(session)} />)}
            {!visibleSessions.length && <EmptyState icon={MessageSquareText} title="没有匹配的 Session" text="更改筛选条件，或先在 OpenCode 中创建 Session。" />}
          </div>
        </div>
        <div className="sessions-pagination"><div className="pagination-summary"><span>共 {filtered.length} 条记录</span><label>每页 <select value={manualPageSize === null ? "auto" : String(manualPageSize)} onChange={(event) => changePageSize(event.target.value)}><option value="auto">自动 ({autoPageSize})</option>{sessionPageSizes.map((size) => <option value={size} key={size}>{size}</option>)}</select></label></div><nav aria-label="分页"><button disabled={currentPage === 1} onClick={() => setPageNumber((value) => Math.max(1, value - 1))}><ChevronLeft size={15} /></button>{paginationItems(currentPage, pageCount).map((item, index) => item === "…" ? <span className="pagination-ellipsis" key={`ellipsis-${index}`}>…</span> : <button className={currentPage === item ? "active" : ""} key={item} onClick={() => setPageNumber(Number(item))}>{item}</button>)}<button disabled={currentPage === pageCount} onClick={() => setPageNumber((value) => Math.min(pageCount, value + 1))}><ChevronRight size={15} /></button></nav></div>
      </section>
      {selectedSession && <SessionDetailDialog session={selectedSession} onClose={() => setSelectedSessionKey("")} onRefresh={onRefresh} showToast={showToast} />}
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

function SessionTableRow({ session, metric, onDetails, onAddToGoal }: { session: SessionView; metric: ExecutionMetric; onDetails: () => void; onAddToGoal: () => void }) {
  const tone = activityTone(session);
  const modelLabel = `${session.currentModel || "默认模型"}${session.currentVariant ? ` · ${session.currentVariant}` : ""}`;
  const serverDuration = metric === "round" ? session.latestExecutionSeconds : session.totalExecutionSeconds;
  const duration = useLiveSeconds(serverDuration, isSessionBusy(session));
  const noActivity = useLiveSeconds(session.noActivitySeconds, isSessionBusy(session) && session.activityLevel !== "waiting");
  return <article className="sessions-table-row" role="row">
    <div className="table-session-cell"><span className={`session-run-icon ${tone}`}>{session.status === "running" ? <Play size={15} /> : <Activity size={15} />}</span><div><strong title={session.title}>{session.title}</strong><span>项目：{session.projectName}<i />Agent：{session.agentName}<i />渠道：{session.channelName}</span><code>{shortID(session.id)}</code></div></div>
    <span><span className={`table-status ${tone}`}>{tone === "running" && <i className="health-dot online" />}{session.statusLabel}</span>{session.statusDetail && <small title={session.statusDetail}>{session.statusDetail}</small>}</span>
    <span className="duration-cell">{duration > 0 ? formatDuration(duration) : "—"}{session.busyForSeconds > 0 && metric === "round" ? <small>↑ LIVE</small> : session.executionCount > 0 && <small>{session.executionCount} 轮</small>}</span>
    <span className="last-input-cell" title={session.hasLastInput ? session.lastInput : "暂无用户输入"}>{session.hasLastInput ? session.lastInput : "—"}</span>
    <span className="model-cell" title={modelLabel}>{modelLabel}</span>
    <span className={`activity-cell ${session.activityLevel}`} title={session.operationSummary || "暂无操作信息"}><strong>{operationLabel(session)}</strong><small>{isSessionBusy(session) ? `无动作 ${formatDuration(noActivity)}` : `最后活动 ${formatRecentTime(session.lastActivityAt || session.updatedAt)}`}</small></span>
    <div className="table-actions"><button className="table-action" onClick={onDetails}>详情</button><button className="table-action goal-action" disabled={session.goalLoopActive || !session.routeEnabled} title={session.goalLoopActive ? "已加入活动 Goal Loop" : !session.routeEnabled ? "请先在项目接入中启用该项目" : "将当前 Session 接入 Goal Loop"} onClick={onAddToGoal}>{session.goalLoopActive ? "已接入" : "加入 Goal"}</button></div>
  </article>;
}

function SessionDetailDialog({ session, onClose, onRefresh, showToast }: { session: SessionView; onClose: () => void; onRefresh: () => void; showToast: (message: string) => void }) {
  const [busy, setBusy] = useState("");
  const [waitMinutes, setWaitMinutes] = useState(30);
  const [detail, setDetail] = useState<SessionDetailView | null>(null);
  const [detailError, setDetailError] = useState("");
  const [detailLoading, setDetailLoading] = useState(true);
  const [detailTab, setDetailTab] = useState<"slow" | "recent" | "subagents">("slow");
  const noActivity = useLiveSeconds(session.noActivitySeconds, isSessionBusy(session) && session.activityLevel !== "waiting");
  const loadDetail = useCallback(async () => {
    setDetailLoading(true);
    setDetailError("");
    try {
      const value = await AppService.GetSessionDetail(session.id, session.directory);
      setDetail({ ...value, operations: value.operations ?? [], toolStats: value.toolStats ?? [], subagents: value.subagents ?? [] });
    } catch (reason) {
      setDetailError(errorMessage(reason));
    } finally {
      setDetailLoading(false);
    }
  }, [session.id, session.directory]);
  useEffect(() => { void loadDetail(); }, [loadDetail]);
  const slowOperations = useMemo(() => (detail?.operations ?? []).filter((operation) => operation.persisted || operation.running).sort((left, right) => right.durationSeconds - left.durationSeconds), [detail]);
  const recentOperations = useMemo(() => [...(detail?.operations ?? [])].sort((left, right) => new Date(right.startedAt).getTime() - new Date(left.startedAt).getTime()), [detail]);
  const runningOperations = useMemo(() => (detail?.operations ?? []).filter((operation) => operation.running).sort((left, right) => new Date(right.startedAt).getTime() - new Date(left.startedAt).getTime()), [detail]);
  const currentOperation = runningOperations[0];
  const toolStats = detail?.toolStats ?? [];
  const subagents = detail?.subagents ?? [];
  const maxToolSeconds = Math.max(1, ...toolStats.map((item) => item.totalSeconds));
  const act = async (label: string, successMessage: string, action: () => Promise<void>) => {
    setBusy(label);
    try { await action(); showToast(successMessage); onRefresh(); void loadDetail(); } catch (reason) { showToast(`操作失败：${errorMessage(reason)}`); } finally { setBusy(""); }
  };
  const abort = () => {
    const source = session.activityFromSubagent ? `检测到 Subagent「${session.activitySourceTitle}」停滞；该操作会中断主 Session。` : "该操作会中断当前主 Session。";
    if (window.confirm(`${source}确定继续？`)) void act("abort", "已请求中断主 Session", () => AppService.AbortSessionExecution(session.id, session.directory));
  };
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}><section className="session-detail-modal" role="dialog" aria-modal="true" aria-label="Session 详情">
    <header><div><span>Session 分析</span><h2>{session.title}</h2><code>{session.id}</code></div><div className="session-detail-head-actions"><button onClick={() => void loadDetail()} aria-label="刷新详情" title="刷新详情" disabled={detailLoading}>{detailLoading ? <LoaderCircle size={17} className="spin" /> : <RefreshCw size={17} />}</button><button onClick={onClose} aria-label="关闭"><X size={17} /></button></div></header>
    <div className="session-detail-body">
      <div className={`stall-banner ${session.activityLevel}`}><CircleAlert size={19} /><div><strong>{activityStatusLabel(session)}</strong><span>{isSessionBusy(session) ? `已经 ${formatDuration(noActivity)} 没有检测到新动作` : `最后活动于 ${formatDateTime(session.lastActivityAt || session.updatedAt)}`}</span>{isSessionBusy(session) && <div className="current-operation"><span><code>{currentOperation?.tool || session.operationType || "Agent"}</code><b title={currentOperation?.summary || session.operationSummary}>{currentOperation?.summary || session.operationSummary || "正在处理当前任务"}</b></span><small>{currentOperation ? `${currentOperation.fromSubagent ? `Subagent · ${currentOperation.sessionTitle}` : "主 Agent"} · 已运行 ${formatDuration(currentOperation.durationSeconds)}` : `${session.activityFromSubagent ? `Subagent · ${session.activitySourceTitle}` : "主 Agent"}${session.operationStatus ? ` · ${session.operationStatus}` : ""}`}{runningOperations.length > 1 ? ` · 另有 ${runningOperations.length - 1} 个操作并行执行` : ""}</small>{currentOperation?.inputPreview && <code className="current-operation-input" title={currentOperation.inputPreview}>{currentOperation.inputPreview}</code>}</div>}</div></div>
      {detailLoading && !detail ? <div className="session-analysis-loading"><LoaderCircle className="spin" /><span>正在读取主 Session 与 Subagent 操作数据</span></div> : detailError ? <div className="session-analysis-error"><CircleAlert size={18} /><span>{detailError}</span><button className="secondary-button" onClick={() => void loadDetail()}>重试</button></div> : detail && <>
        <div className="session-analysis-summary"><AnalysisMetric label="Session 跨度" value={formatDuration(detail.elapsedSeconds)} hint={`${formatDateTime(detail.createdAt)} 开始`} /><AnalysisMetric label="工具调用" value={`${detail.toolCallCount} 次`} hint={`${detail.runningOperationCount} 个正在运行 · ${detail.failedOperationCount} 个失败`} /><AnalysisMetric label="工具累计耗时" value={formatDuration(detail.totalToolSeconds)} hint="并行工具耗时会分别累计" /><AnalysisMetric label="Subagents" value={`${detail.subagentCount} 个`} hint={`${detail.messageCount} 条消息已分析`} /></div>
        <section className="session-tool-breakdown"><header><div><strong>工具耗时分布</strong><span>按累计耗时排序</span></div>{detail.truncated && <em>消息较多，仅分析最近窗口与已保存慢操作</em>}</header>{toolStats.length ? <div className="tool-stat-list">{toolStats.slice(0, 8).map((item) => <div className="tool-stat-row" key={item.tool}><code>{item.tool || "tool"}</code><div><i style={{ width: `${Math.max(3, item.totalSeconds / maxToolSeconds * 100)}%` }} /></div><strong>{formatDuration(item.totalSeconds)}</strong><span>{item.callCount} 次 · 最长 {formatDuration(item.longestSeconds)}</span></div>)}</div> : <p className="analysis-empty">该 Session 暂无工具调用。</p>}</section>
        <section className="session-operation-analysis"><header><div className="analysis-tabs" role="tablist"><button className={detailTab === "slow" ? "active" : ""} onClick={() => setDetailTab("slow")}>耗时操作</button><button className={detailTab === "recent" ? "active" : ""} onClick={() => setDetailTab("recent")}>最近操作</button><button className={detailTab === "subagents" ? "active" : ""} onClick={() => setDetailTab("subagents")}>Subagents</button></div><span>{detailTab === "slow" ? "耗时从高到低，可展开查看输入" : detailTab === "recent" ? "按开始时间倒序" : "按工具累计耗时排序"}</span></header>
          {detailTab === "slow" && <SessionOperationList operations={slowOperations} emptyText="没有可展示的耗时操作。" />}
          {detailTab === "recent" && <SessionOperationList operations={recentOperations} emptyText="没有可展示的最近操作。" />}
          {detailTab === "subagents" && <SubagentAnalysisList items={subagents} />}
        </section>
      </>}
      {session.activityFromSubagent && <div className="subagent-impact"><Bot size={17} /><span><strong>当前停滞发生在 Subagent</strong><small>{session.activitySourceTitle} · {shortID(session.activitySourceSessionId)}。执行中断会作用于主 Session。</small></span></div>}
    </div>
    <footer>{["suspected", "stalled"].includes(session.activityLevel) && <div className="stall-wait-control"><select aria-label="继续等待时长" value={waitMinutes} disabled={!!busy} onChange={(event) => setWaitMinutes(Number(event.target.value))}><option value={15}>15 分钟</option><option value={30}>30 分钟</option><option value={60}>60 分钟</option></select><button className="secondary-button" disabled={!!busy} onClick={() => void act("snooze", `已继续等待 ${waitMinutes} 分钟`, () => AppService.SnoozeSessionStall(session.id, session.directory, waitMinutes))}><Clock3 size={15} />继续等待</button></div>}<span className="toolbar-spacer" /><button className="secondary-button" onClick={onClose}>关闭</button>{isSessionBusy(session) && <button className="danger-button" disabled={!!busy} onClick={abort}>{busy === "abort" ? <LoaderCircle size={15} className="spin" /> : <X size={15} />}中断主 Session</button>}</footer>
  </section></div>;
}

function AnalysisMetric({ label, value, hint }: { label: string; value: string; hint: string }) { return <div><span>{label}</span><strong>{value}</strong><small title={hint}>{hint}</small></div>; }

function SessionOperationList({ operations, emptyText }: { operations: SessionOperationView[]; emptyText: string }) {
  if (!operations.length) return <p className="analysis-empty">{emptyText}</p>;
  return <div className="analysis-operation-list">{operations.map((operation, index) => <article className={`analysis-operation-row${operation.running ? " running" : operation.failed ? " failed" : ""}`} key={operation.id}>
    <span className="operation-rank">{index + 1}</span><div className="operation-main"><div><code>{operation.tool || "tool"}</code><strong title={operation.summary}>{operation.summary || "未命名操作"}</strong>{operation.persisted && <em>已保存</em>}{operation.running && <em className="running">运行中</em>}</div><details><summary>{operation.inputPreview || "无可用输入参数"}</summary>{operation.inputPreview && <pre>{operation.inputPreview}</pre>}</details></div><div className="operation-source"><strong>{operation.fromSubagent ? "Subagent" : "主 Agent"}</strong><span title={operation.sessionTitle}>{operation.sessionTitle}</span><small>{operation.agent || "默认 Agent"}</small></div><div className="operation-time"><strong>{formatDuration(operation.durationSeconds)}</strong><span>{formatDateTime(operation.startedAt)}</span><small>{operation.status || "未知状态"}</small></div>
  </article>)}</div>;
}

function SubagentAnalysisList({ items }: { items: NonNullable<SessionDetailView["subagents"]> }) {
  if (!items.length) return <p className="analysis-empty">该 Session 没有 Subagent。</p>;
  return <div className="subagent-analysis-list">{items.map((item) => <article key={item.id}><Bot size={17} /><div><strong title={item.title}>{item.title || shortID(item.id)}</strong><code>{shortID(item.id)}</code></div><span>{item.agent || "默认 Agent"}</span><span>{item.toolCallCount} 次工具调用</span><span>累计 {formatDuration(item.totalToolSeconds)}</span><span>最长 {formatDuration(item.longestSeconds)}</span>{item.runningCount > 0 && <em>{item.runningCount} 个运行中</em>}{item.failedCount > 0 && <em className="failed">{item.failedCount} 个失败</em>}</article>)}</div>;
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

function SettingsPage({ interfaceDensity, onInterfaceDensityChange, showToast, onSaved }: { interfaceDensity: InterfaceDensity; onInterfaceDensityChange: (density: InterfaceDensity) => void; showToast: (message: string) => void; onSaved: () => void }) {
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
  return (
    <section className="page settings-page">
      <PageHeader title="设置" description="管理 OpenCode、飞书、通知和桌面应用行为。" />
      {settings?.configError && <div className="banner warning-banner"><CircleAlert size={18} /><span>{settings.configError}</span></div>}
      <div className="settings-layout">
        <AppearanceSettings density={interfaceDensity} onChange={onInterfaceDensityChange} />
        {!settings || !form ? <section className="panel settings-section settings-loading-panel"><LoaderCircle className="spin" /><span>正在读取服务设置</span></section> : <>
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
          <div className="form-grid two"><FormField label="疑似停滞时间" hint="例如 10m；必须小于长时间停滞阈值"><input value={form.activitySuspectedAfter} onChange={(event) => update("activitySuspectedAfter", event.target.value)} /></FormField><FormField label="长时间停滞时间" hint="例如 30m；Goal 可在达到后自动恢复"><input value={form.activityStalledAfter} onChange={(event) => update("activityStalledAfter", event.target.value)} /></FormField><FormField label="耗时操作阈值" hint="例如 30s；达到后保存输入摘要和耗时"><input value={form.slowOperationAfter} onChange={(event) => update("slowOperationAfter", event.target.value)} /></FormField></div>
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
          <div className="path-list"><PathRow label="配置文件" value={settings.paths.configPath} size={settings.fileSizes.config} /><PathRow label="数据库" value={settings.paths.storePath} size={settings.fileSizes.store} /><PathRow label="日志" value={settings.paths.logPath} size={settings.fileSizes.log} /></div>
          <div className="desktop-actions"><button className="secondary-button" onClick={() => void AppService.OpenDataDirectory()}><Folder size={16} />打开数据目录</button><button className="danger-button" onClick={() => { if (window.confirm("退出后将停止 Handoff 服务，确定继续吗？")) AppService.Quit(); }}><Power size={16} />退出应用</button></div>
        </section>
        </>}
      </div>
      {settings && form && <div className="settings-savebar"><span>保存后会安全重启 Handoff 引擎，不会启动或停止 OpenCode。</span><button className="primary-button" disabled={saving} onClick={() => void save()}>{saving ? <LoaderCircle size={16} className="spin" /> : <Check size={16} />}保存设置</button></div>}
    </section>
  );
}

function AppearanceSettings({ density, onChange }: { density: InterfaceDensity; onChange: (density: InterfaceDensity) => void }) {
  return (
    <section className="panel settings-section appearance-settings">
      <h2>外观</h2><p className="section-description">统一调整字体、控件和列表密度，切换后立即生效。</p>
      <div className="density-options" role="radiogroup" aria-label="界面密度">
        <button className={density === "compact" ? "density-option active" : "density-option"} role="radio" aria-checked={density === "compact"} onClick={() => onChange("compact")}>
          <span className="density-preview compact-preview" aria-hidden="true"><i /><i /><i /></span>
          <span><strong>紧凑</strong><small>适合希望同屏显示更多信息的场景</small></span>
        </button>
        <button className={density === "standard" ? "density-option active" : "density-option"} role="radio" aria-checked={density === "standard"} onClick={() => onChange("standard")}>
          <span className="density-preview standard-preview" aria-hidden="true"><i /><i /><i /></span>
          <span><strong>标准</strong><small>推荐，字体和间距更舒适</small></span>
        </button>
      </div>
      <p className="density-note">正文不会低于 12px；状态标签和短 ID 不会低于 10px。</p>
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

function SessionCard({ session, compact = false }: { session: SessionView; compact?: boolean }) {
  const tone = activityTone(session);
  const modelLabel = `${session.currentModel || "OpenCode 默认/尚未识别"}${session.currentVariant ? ` · ${session.currentVariant}` : ""}`;
  const busy = isSessionBusy(session);
  const busyForSeconds = useLiveSeconds(session.busyForSeconds, busy);
  const sinceLastInputSeconds = useLiveSeconds(session.sinceLastInputSeconds, session.hasLastInput);
  const noActivitySeconds = useLiveSeconds(session.noActivitySeconds, busy && session.activityLevel !== "waiting");
  return (
    <article className={`session-card ${tone} ${compact ? "compact" : ""}`}>
      <div className="session-card-head"><span className={`session-icon ${tone}`}>{session.status === "running" ? <Play size={17} /> : session.status.startsWith("waiting") ? <Clock3 size={17} /> : session.status === "retrying" ? <RotateCcw size={17} /> : <Activity size={17} />}</span><div className="session-title"><strong>{session.title}</strong><code>{shortID(session.id)}</code></div><span className={`status-pill ${tone}`}>{session.statusLabel}</span></div>
      <div className="session-meta"><span>项目：{session.projectName}</span><i /> <span>Agent：{session.agentName}</span><i /> <span>渠道：{session.channelName}</span></div>
      {session.statusDetail && <p className="status-detail">{session.statusDetail}</p>}
      <div className="session-model-row" title={`模型：${modelLabel}`}><Bot size={14} /><span>模型：{modelLabel}</span></div>
      <div className={`session-activity-row ${session.activityLevel}`}><strong>{operationLabel(session)}</strong><span>{busy ? `无动作 ${formatDuration(noActivitySeconds)}` : `最后活动 ${formatRecentTime(session.lastActivityAt || session.updatedAt)}`}</span>{session.activityFromSubagent && <small>Subagent · {session.activitySourceTitle}</small>}</div>
      <div className="session-time-row"><span>{busy ? `当前忙碌 ${formatDuration(busyForSeconds)}` : "当前未忙碌"}</span><span>{session.hasLastInput ? `距最后用户输入 ${formatDuration(sinceLastInputSeconds)}` : "暂无用户输入"}</span></div>
      {!compact && session.hasLastInput && <details className="last-input"><summary>完整最后输入</summary><pre>{session.lastInput}</pre></details>}
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
function PathRow({ label, value, size }: { label: string; value: string; size: number }) { return <div className="path-row"><span>{label}</span><code title={value}>{value}</code><span className="path-size" title={`${size.toLocaleString("zh-CN")} 字节`}>{formatFileSize(size)}</span></div>; }
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
    activitySuspectedAfter: settings.activitySuspectedAfter,
    activityStalledAfter: settings.activityStalledAfter,
    slowOperationAfter: settings.slowOperationAfter,
  };
}

function normaliseDashboard(value: Dashboard): Dashboard { return { ...value, projects: value.projects ?? [], sessions: value.sessions ?? [], executionRuns: value.executionRuns ?? [], executionSessions: value.executionSessions ?? [], agents: value.agents ?? [], channels: value.channels ?? [] }; }
function errorMessage(reason: unknown): string { return reason instanceof Error ? reason.message : String(reason); }
function serviceStateLabel(state: string): string { return ({ conflict: "等待关闭 CLI", config_error: "配置待完善", error: "服务异常", stopped: "服务已停止", loading: "正在启动" } as Record<string, string>)[state] ?? "未运行"; }
function statusTone(status: string): string { return ({ running: "running", waiting_permission: "permission", waiting_answer: "question", retrying: "retry", idle: "idle", unmonitored: "neutral" } as Record<string, string>)[status] ?? "neutral"; }
function activityTone(session: SessionView): string { return session.activityLevel === "stalled" ? "stalled" : session.activityLevel === "suspected" ? "suspected" : statusTone(session.status); }
function activityRank(level: string): number { return ({ stalled: 3, suspected: 2, waiting: 1 } as Record<string, number>)[level] ?? 0; }
function activityStatusLabel(session: SessionView): string { return ({ stalled: "长时间停滞", suspected: "疑似停滞", waiting: "等待人工处理" } as Record<string, string>)[session.activityLevel] ?? (isSessionBusy(session) ? "正在执行" : "当前空闲"); }
function operationLabel(session: SessionView): string { const type = session.operationType && session.operationType !== "Session" ? session.operationType : "活动"; return `${type}${session.operationSummary ? ` · ${session.operationSummary}` : ""}`; }
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
function formatFileSize(bytes: number): string { const value = Math.max(0, bytes); if (value < 1024) return `${value} B`; const units = ["KB", "MB", "GB", "TB"]; const unitIndex = Math.min(Math.floor(Math.log(value) / Math.log(1024)) - 1, units.length - 1); const amount = value / 1024 ** (unitIndex + 1); return `${amount.toLocaleString("zh-CN", { maximumFractionDigits: 1 })} ${units[unitIndex]}`; }
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
