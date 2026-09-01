import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  Bot,
  CalendarClock,
  Check,
  CircleAlert,
  CirclePause,
  CircleStop,
  ExternalLink,
  GitBranch,
  History,
  Infinity as InfinityIcon,
  LoaderCircle,
  MessageSquareText,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Save,
  ShieldCheck,
  Sparkles,
  Target,
  Trash2,
  X,
  Zap,
} from "lucide-react";
import * as AppService from "../bindings/github.com/Hans2573/OpenCode-Handoff/appservice";
import type {
  GoalLoopEventView,
  GoalLoopInput,
  GoalLoopPage,
  GoalLoopView,
  GoalModelView,
  LoopApprovalView,
  ProjectView,
	SessionView,
} from "../bindings/github.com/Hans2573/OpenCode-Handoff/internal/desktop/models";

type Props = {
  projects: ProjectView[];
	sessions: SessionView[];
	initialSession: SessionView | null;
	onInitialSessionConsumed: () => void;
  showToast: (message: string) => void;
};

const emptyPage: GoalLoopPage = { generatedAt: new Date().toISOString(), loops: [], approvals: [] };

export default function LoopsPage({ projects, sessions, initialSession, onInitialSessionConsumed, showToast }: Props) {
  const [data, setData] = useState<GoalLoopPage>(emptyPage);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [events, setEvents] = useState<GoalLoopEventView[]>([]);
  const [editor, setEditor] = useState<GoalLoopView | null | undefined>(undefined);
	const [attachSession, setAttachSession] = useState<SessionView | null>(null);
  const [models, setModels] = useState<GoalModelView[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState("");
  const [approvalScope, setApprovalScope] = useState<{ loopID?: string; title: string } | null>(null);
  const polling = useRef(false);

  const loops = data.loops ?? [];
  const approvals = data.approvals ?? [];
  const selected = loops.find((item) => item.id === selectedID) ?? loops[0];
  const connectedProjects = projects.filter((item) => item.routeEnabled);

  const load = useCallback(async (quiet = false) => {
    if (polling.current) return;
    polling.current = true;
    if (!quiet) setLoading(true);
    try {
      const page = await AppService.GetGoalLoops();
      setData({ ...page, loops: page.loops ?? [], approvals: page.approvals ?? [] });
    } catch (reason) {
      showToast(`读取 Loop 失败：${errorMessage(reason)}`);
    } finally {
      polling.current = false;
      if (!quiet) setLoading(false);
    }
  }, [showToast]);

  const loadModels = useCallback(async () => {
    setModelsLoading(true);
    setModelsError("");
    try {
      const nextModels = (await AppService.GetGoalModels()) ?? [];
      setModels(nextModels);
      if (!nextModels.length) setModelsError("OpenCode Server 未返回可用模型");
    } catch (reason) {
      const message = errorMessage(reason).trim() || "模型接口暂不可用，请重启桌面应用后重试";
      setModelsError(message);
      showToast(`读取模型失败：${message}`);
    } finally {
      setModelsLoading(false);
    }
  }, [showToast]);

  const openEditor = (loop: GoalLoopView | null, session: SessionView | null = null) => {
    setEditor(loop);
	setAttachSession(session);
    if (!models.length && !modelsLoading) void loadModels();
  };

	useEffect(() => {
		if (!initialSession) return;
		openEditor(null, initialSession);
		onInitialSessionConsumed();
	}, [initialSession, onInitialSessionConsumed]);

  useEffect(() => {
    void load();
    void loadModels();
    const timer = window.setInterval(() => void load(true), 3000);
    return () => window.clearInterval(timer);
  }, [load, loadModels]);

  useEffect(() => {
    if (!selected?.id) { setEvents([]); return; }
    void AppService.GetGoalLoopEvents(selected.id).then((items) => setEvents(items ?? [])).catch(() => setEvents([]));
  }, [selected?.id, selected?.updatedAt]);

  const apply = async (label: string, action: () => Promise<GoalLoopPage>) => {
    setBusy(label);
    try {
      const page = await action();
      setData({ ...page, loops: page.loops ?? [], approvals: page.approvals ?? [] });
      showToast("操作已完成");
      return true;
    } catch (reason) {
      showToast(`操作失败：${errorMessage(reason)}`);
      return false;
    } finally {
      setBusy("");
    }
  };

  const deleteLoop = (loop: GoalLoopView) => {
    if (!window.confirm(`删除 Goal「${loop.name}」？原 OpenCode Session 会保留。`)) return;
    void apply("delete", () => AppService.DeleteGoalLoop(loop.id));
  };

  const terminateLoop = (loop: GoalLoopView) => {
    if (!window.confirm("终止后 Goal 将不会自动继续，但原 Session 会保留。确定终止？")) return;
    void apply("terminate", () => AppService.TerminateGoalLoop(loop.id));
  };

  const openSession = async (loop: GoalLoopView) => {
    if (!loop.sessionId) return;
    try {
      await AppService.OpenSession(loop.sessionId, loop.directory);
      showToast("已打开 OpenCode，Session ID 已复制");
    } catch (reason) {
      showToast(`打开 Session 失败：${errorMessage(reason)}`);
    }
  };

  const summary = useMemo(() => ({
    running: loops.filter((item) => ["running", "retrying", "waiting_approval", "waiting_takeover", "deciding"].includes(item.status)).length,
    approvals: approvals.length,
    projects: new Set(loops.map((item) => item.projectId)).size,
    completed: loops.filter((item) => item.status === "completed").length,
  }), [loops, approvals]);
  const scopedApprovals = approvalScope?.loopID ? approvals.filter((item) => item.loopId === approvalScope.loopID) : approvals;

  const openLoopApprovals = (loop: GoalLoopView) => {
    setSelectedID(loop.id);
    setApprovalScope({ loopID: loop.id, title: loop.name });
  };

  const applyApproval = async (label: string, action: () => Promise<GoalLoopPage>) => {
    const succeeded = await apply(label, action);
    if (succeeded && scopedApprovals.length <= 1) setApprovalScope(null);
    return succeeded;
  };

  return (
    <section className="page loops-page">
      <div className="loops-heading">
        <div><h1>Loop 工程</h1><p>让 Agent 围绕明确目标持续工作，直到按约定报告完成。</p></div>
        <div className="loops-heading-actions">
          <button className="secondary-button" onClick={() => void AppService.OpenLoopGuide()}><ExternalLink size={15} />什么是 Loop 工程？</button>
          <button className="primary-button" onClick={() => openEditor(null)}><Plus size={16} />创建 Goal</button>
        </div>
      </div>

      <div className="loop-summary-grid">
        <LoopMetric label="运行中 Loops" value={summary.running} icon={Activity} tone="green" />
        <LoopMetric label="待处理请求" value={summary.approvals} icon={ShieldCheck} tone="orange" onClick={() => setApprovalScope({ title: "全部待处理请求" })} />
        <LoopMetric label="关联项目" value={summary.projects} icon={GitBranch} tone="blue" />
        <LoopMetric label="已完成" value={summary.completed} icon={Check} tone="purple" />
      </div>

      <div className="loop-kind-grid">
        <article className="loop-kind active"><span><Target size={24} /></span><div><strong>Goal-based Loop</strong><p>新建或接入现有 Session，自主迭代直至完成。</p><small>/goal · AI 自动审批与选择 · 无硬性轮次上限</small></div><em>已开放</em></article>
        <article className="loop-kind disabled"><span><CalendarClock size={24} /></span><div><strong>Time-based Loop</strong><p>按计划或时间间隔运行 Agent 任务。</p><small>计划 · 时区 · 重复规则</small></div><em>即将支持</em></article>
        <article className="loop-kind disabled"><span><Zap size={24} /></span><div><strong>Trigger-based Loop</strong><p>由外部事件触发自动化任务。</p><small>Webhook · Issue · PR · CI</small></div><em>即将支持</em></article>
      </div>

      <div className="loop-guide-strip"><Sparkles size={17} /><span>Loop 是 Agent 重复执行、验证和继续工作的循环，直到满足停止条件。</span><button onClick={() => void AppService.OpenLoopGuide()}>阅读 Claude 官方介绍 <ExternalLink size={13} /></button></div>

      <div className="loops-workspace">
        <section className="panel loop-list-panel">
          <div className="loop-panel-title"><div><h2>Goal 实例</h2><span>{loops.length} 个</span></div><button className="icon-action" onClick={() => void load()} title="刷新"><RefreshCw size={15} className={loading ? "spin" : ""} /></button></div>
          {loading && !loops.length ? <div className="loop-loading"><LoaderCircle className="spin" />正在读取 Goal</div> : (
            <div className="loop-table">
              <div className="loop-row loop-head"><span>名称</span><span>项目 / Agent</span><span>状态</span><span>轮次</span><span>最后活动</span></div>
              {loops.map((loop) => <div key={loop.id} role="button" tabIndex={0} className={`loop-row interactive ${selected?.id === loop.id ? "selected" : ""}`} onClick={() => setSelectedID(loop.id)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); setSelectedID(loop.id); } }}><span className="loop-name"><i className={`loop-dot ${loopTone(loop.status)}`} /><strong>{loop.name}</strong><small>{loop.goal}</small></span><span><strong>{loop.projectName}</strong><small>{loop.agentName}</small></span><span>{["waiting_approval", "deciding"].includes(loop.status) ? <button type="button" className="loop-status approval actionable" onClick={(event) => { event.stopPropagation(); openLoopApprovals(loop); }}>{loop.statusLabel}</button> : <em className={`loop-status ${loopTone(loop.status)}`}>{loop.statusLabel}</em>}{loop.consecutiveFailures > 0 && <small>失败 {loop.consecutiveFailures}/{loop.failureLimit}</small>}</span><span>{loop.cycleCount}</span><time>{relativeTime(loop.updatedAt)}</time></div>)}
              {!loops.length && <div className="loop-empty"><InfinityIcon size={28} /><strong>还没有 Goal Loop</strong><p>创建一个目标，让 Agent 持续工作直到完成。</p><button className="primary-button" onClick={() => openEditor(null)}><Plus size={15} />创建 Goal</button></div>}
            </div>
          )}
        </section>

        <section className="panel loop-detail-panel">
          {selected ? <>
            <div className="loop-detail-head"><div><span>Goal 配置</span><h2>{selected.name}</h2></div>{["waiting_approval", "deciding"].includes(selected.status) ? <button type="button" className="loop-status approval actionable" onClick={() => openLoopApprovals(selected)}>{selected.statusLabel}</button> : <em className={`loop-status ${loopTone(selected.status)}`}>{selected.statusLabel}</em>}</div>
            <div className="loop-detail-body">
              <Detail label="目标"><p className="goal-copy">{selected.goal}</p></Detail>
              <div className="loop-detail-grid"><Detail label="项目"><strong>{selected.projectName}</strong><code>{selected.directory}</code></Detail><Detail label="Session 来源"><strong>{selected.attachedSession ? "接入现有 Session" : "自动新建 Session"}</strong><small>接管后消息添加 /goal 前缀</small></Detail></div>
              <Detail label="模型"><strong>{selected.modelName || selected.modelId || "未配置"}{selected.modelVariant ? ` · ${selected.modelVariant}` : ""}</strong><code>{selected.modelProviderId && selected.modelId ? `${selected.modelProviderId}/${selected.modelId}` : "—"}</code></Detail>
              <div className="loop-detail-grid"><Detail label="自主策略"><strong>{selected.automationMode === "manual" ? "人工监督" : "完全自主"}</strong><small>{selected.automationMode === "manual" ? "应用或飞书处理请求" : "自动处理权限与选择框"}</small></Detail><Detail label="权限审批"><strong>{selected.automationMode === "manual" ? "人工审批" : selected.permissionApprovalMode === "allow_all" ? "全部同意" : "AI 智能审批"}</strong><small>{selected.automationMode === "manual" ? "在应用或飞书中处理" : selected.permissionApprovalMode === "allow_all" ? "每个权限请求直接允许一次" : "由监督模型判断风险和范围"}</small></Detail></div>
              {selected.automationMode === "autonomous" && <Detail label="监督模型"><strong>{selected.supervisorModelName || selected.supervisorModelId || selected.modelName}</strong><small>{selected.supervisorModelId === "__agent_default__" ? "Agent 默认模型" : `${selected.supervisorModelProviderId}/${selected.supervisorModelId}`}</small></Detail>}
              <div className="loop-detail-grid"><Detail label="连续故障恢复阈值"><strong>{selected.failureLimit} 次</strong><small>当前连续失败 {selected.consecutiveFailures} 次</small></Detail><Detail label="完成策略"><strong>{selected.requireCompletionConfirmation ? "需要人工确认" : "自动完成"}</strong><small>无最大轮数与时长限制</small></Detail></div>
              {selected.automationMode === "autonomous" && selected.permissionApprovalMode !== "allow_all" && <Detail label="额外允许路径"><strong>{(selected.allowedDirectories ?? []).length ? (selected.allowedDirectories ?? []).join("、") : "未配置"}</strong><small>项目目录始终允许；OpenCode 按目录申请权限时需添加对应目录</small></Detail>}
              {selected.lastError && <div className="loop-error"><CircleAlert size={16} /><span>{selected.lastError}</span></div>}
              {selected.sessionId && <Detail label="Session"><button className="session-link" onClick={() => void openSession(selected)}>{selected.sessionId}<ExternalLink size={13} /></button></Detail>}
              <div className="loop-event-block"><h3><History size={15} />执行与 AI 决策记录</h3><div className="loop-events">{events.slice(0, 12).map((event) => <div key={event.id}><i /><span><strong>{event.message}</strong>{event.metadata?.reason && <small title={String(event.metadata.reason)}>理由：{String(event.metadata.reason)}</small>}<small>{relativeTime(event.createdAt)}{event.metadata?.model ? ` · ${String(event.metadata.model)}` : ""}</small></span></div>)}{!events.length && <p>暂无执行记录</p>}</div></div>
            </div>
            <div className="loop-detail-actions">
              {selected.status === "draft" && <><button className="secondary-button" onClick={() => openEditor(selected)}><Pencil size={14} />编辑</button><button className="primary-button" disabled={!!busy} onClick={() => { if (window.confirm("请确认 OpenCode Agent 已支持 /goal。确认后立即启动？")) void apply("start", () => AppService.StartGoalLoop(selected.id, true)); }}><Play size={14} />启动</button></>}
              {["blocked", "terminated"].includes(selected.status) && <><button className="secondary-button" onClick={() => openEditor(selected)}><Pencil size={14} />编辑配置</button><button className="primary-button" disabled={!!busy} onClick={() => { if (window.confirm("将复用当前配置和原 Session，重新发送 /goal；若 Session 正忙会先等待。确定重新启动？")) void apply("restart", () => AppService.RestartGoalLoop(selected.id, true)); }}><Play size={14} />重新启动</button></>}
              {["running", "retrying", "waiting_approval", "waiting_takeover", "deciding"].includes(selected.status) && <button className="secondary-button" disabled={!!busy} onClick={() => void apply("pause", () => AppService.PauseGoalLoop(selected.id))}><CirclePause size={14} />暂停 Goal</button>}
              {selected.status === "paused" && <button className="primary-button" disabled={!!busy} onClick={() => void apply("resume", () => AppService.ResumeGoalLoop(selected.id))}><Play size={14} />继续</button>}
              {selected.status === "awaiting_confirmation" && <><button className="secondary-button" disabled={!!busy} onClick={() => void apply("resume", () => AppService.ResumeGoalLoop(selected.id))}><Play size={14} />继续 Goal</button><button className="primary-button" disabled={!!busy} onClick={() => void apply("confirm", () => AppService.ConfirmGoalLoopComplete(selected.id))}><Check size={14} />确认完成</button></>}
              {["running", "retrying", "waiting_approval", "waiting_takeover", "deciding", "paused", "awaiting_confirmation"].includes(selected.status) && <button className="danger-button" disabled={!!busy} onClick={() => terminateLoop(selected)}><CircleStop size={14} />终止 Goal</button>}
              {["running", "retrying", "waiting_approval", "waiting_takeover", "deciding", "paused", "awaiting_confirmation"].includes(selected.status) && <button className="danger-button" disabled={!!busy} onClick={() => { if (window.confirm("这会同时中断 OpenCode Session 当前执行，确定继续？")) void apply("terminate-session", () => AppService.TerminateGoalLoopAndSession(selected.id)); }}><CircleStop size={14} />并中断 Session</button>}
              {["draft", "paused", "completed", "blocked", "terminated", "awaiting_confirmation"].includes(selected.status) && <button className="danger-button icon-only" disabled={!!busy} onClick={() => deleteLoop(selected)} title="删除 Goal"><Trash2 size={14} /></button>}
            </div>
          </> : <div className="loop-empty detail"><Target size={29} /><strong>选择一个 Goal</strong><p>这里会显示配置、状态、Session 和执行记录。</p></div>}
        </section>
      </div>

      {editor !== undefined && <GoalEditor current={editor} projects={connectedProjects} sessions={sessions} initialSession={attachSession} models={models} modelsLoading={modelsLoading} modelsError={modelsError} reloadModels={loadModels} loops={loops} onClose={() => { setEditor(undefined); setAttachSession(null); }} onSaved={(page) => { setData({ ...page, loops: page.loops ?? [], approvals: page.approvals ?? [] }); setEditor(undefined); setAttachSession(null); showToast(editor ? "Goal 配置已更新" : "Goal 已创建"); }} showToast={showToast} />}
      {approvalScope && <ApprovalDialog title={approvalScope.title} approvals={scopedApprovals} busy={busy} apply={applyApproval} onClose={() => setApprovalScope(null)} onRefresh={() => void load(true)} />}
    </section>
  );
}

function LoopMetric({ label, value, icon: Icon, tone, onClick }: { label: string; value: number; icon: typeof Activity; tone: string; onClick?: () => void }) {
  if (onClick) return <button type="button" className="loop-metric actionable" onClick={onClick}><div><span>{label}</span><strong>{value}</strong></div><i className={tone}><Icon size={22} /></i></button>;
  return <article className="loop-metric"><div><span>{label}</span><strong>{value}</strong></div><i className={tone}><Icon size={22} /></i></article>;
}

function Detail({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="loop-detail-item"><span>{label}</span>{children}</div>;
}

function ApprovalDialog({ title, approvals, busy, apply, onClose, onRefresh }: { title: string; approvals: LoopApprovalView[]; busy: string; apply: (label: string, action: () => Promise<GoalLoopPage>) => Promise<boolean>; onClose: () => void; onRefresh: () => void }) {
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}><section className="approval-modal" role="dialog" aria-modal="true" aria-label="处理审批">
    <header><div><span>应用内审批</span><h2>{title}</h2><small>与飞书共享同一 OpenCode 请求，先处理的一端生效</small></div><button type="button" onClick={onClose} aria-label="关闭"><X size={18} /></button></header>
    <div className="approval-modal-body">{approvals.map((approval) => <ApprovalCard key={approval.id} approval={approval} busy={busy} apply={apply} />)}{!approvals.length && <div className="approval-modal-empty"><ShieldCheck size={28} /><strong>没有待处理的审批</strong><p>请求可能已在飞书或其他窗口中处理。</p><button type="button" className="secondary-button" onClick={onRefresh}><RefreshCw size={14} />重新检查</button></div>}</div>
  </section></div>;
}

function GoalEditor({ current, projects, sessions, initialSession, models, modelsLoading, modelsError, reloadModels, loops, onClose, onSaved, showToast }: { current: GoalLoopView | null; projects: ProjectView[]; sessions: SessionView[]; initialSession: SessionView | null; models: GoalModelView[]; modelsLoading: boolean; modelsError: string; reloadModels: () => Promise<void>; loops: GoalLoopView[]; onClose: () => void; onSaved: (page: GoalLoopPage) => void; showToast: (message: string) => void }) {
  const initialProject = initialSession ? projects.find((item) => item.directory === initialSession.directory)?.id : "";
  const [goal, setGoal] = useState(current?.goal ?? "");
  const [projectID, setProjectID] = useState((current?.projectId ?? initialProject) || projects[0]?.id || "");
  const [source, setSource] = useState<"new" | "existing">(current?.attachedSession || initialSession ? "existing" : "new");
  const [sessionID, setSessionID] = useState(current?.sessionId ?? initialSession?.id ?? "");
  const [modelKey, setModelKey] = useState(current?.modelProviderId && current.modelId ? `${current.modelProviderId}\u0000${current.modelId}` : "");
  const [modelVariant, setModelVariant] = useState(current?.modelVariant ?? "");
  const [automationMode, setAutomationMode] = useState(current?.automationMode || "autonomous");
  const [permissionApprovalMode, setPermissionApprovalMode] = useState(current?.permissionApprovalMode || "ai");
  const [allowedDirectories, setAllowedDirectories] = useState((current?.allowedDirectories ?? []).join("\n"));
  const [supervisorKey, setSupervisorKey] = useState(current?.supervisorModelProviderId && current.supervisorModelId ? `${current.supervisorModelProviderId}\u0000${current.supervisorModelId}` : "");
  const [supervisorVariant, setSupervisorVariant] = useState(current?.supervisorModelVariant ?? "");
  const [failureLimit, setFailureLimit] = useState(current?.failureLimit ?? 3);
  const [confirmation, setConfirmation] = useState(current?.requireCompletionConfirmation ?? false);
  const [commandConfirmed, setCommandConfirmed] = useState(false);
  const [saving, setSaving] = useState(false);
  const terminalEdit = !!current && ["blocked", "terminated"].includes(current.status);
  const selectedProject = projects.find((project) => project.id === projectID);
  const selectedModel = models.find((model) => `${model.providerId}\u0000${model.id}` === modelKey);
  const supervisorModel = models.find((model) => `${model.providerId}\u0000${model.id}` === supervisorKey);
  const selectedSession = sessions.find((session) => session.id === sessionID && session.directory === selectedProject?.directory);
  const eligibleSessions = sessions.filter((session) => session.directory === selectedProject?.directory && (!session.goalLoopActive || session.id === current?.sessionId));

  useEffect(() => {
    if (!modelKey && models.length) setModelKey(`${models[0].providerId}\u0000${models[0].id}`);
  }, [modelKey, models]);

  useEffect(() => {
    if (source !== "existing" || !sessionID || !selectedProject || !models.length) return;
    let active = true;
    void AppService.GetSessionModel(sessionID, selectedProject.directory).then((original) => {
      if (!active || !original.providerId || !original.modelId) return;
      const match = models.find((model) => model.providerId === original.providerId && model.id === original.modelId);
      if (match) {
        setModelKey(`${match.providerId}\u0000${match.id}`);
        setModelVariant(original.variant || "");
      }
    }).catch(() => {
      if (!active || !selectedSession?.currentModel) return;
      const match = models.find((model) => `${model.providerId}/${model.id}` === selectedSession.currentModel);
      if (match) { setModelKey(`${match.providerId}\u0000${match.id}`); setModelVariant(selectedSession.currentVariant || ""); }
    });
    return () => { active = false; };
  }, [sessionID, source, models.length]);

  const save = async (startNow: boolean) => {
    if (!goal.trim()) { showToast("请输入目标"); return; }
    if (!projectID) { showToast("请选择已接入飞书的项目"); return; }
    if (source === "existing" && !sessionID) { showToast("请选择要接入的现有 Session"); return; }
    if (!selectedModel) { showToast("请选择可用模型"); return; }
    if (startNow && !commandConfirmed) { showToast("请确认 Agent 支持 /goal"); return; }
    if (startNow && loops.some((item) => item.projectId === projectID && ["running", "retrying", "waiting_approval", "waiting_takeover", "deciding"].includes(item.status))) {
      const proceed = window.confirm("该项目已有 Goal Loop 正在运行。多个 Agent 修改同一工作区可能产生冲突，推荐为新 Goal 使用独立 Git worktree。\n\n仍然启动？");
      if (!proceed) return;
    }
    setSaving(true);
    const input: GoalLoopInput = {
      goal: goal.trim(), projectId: projectID, agentId: "opencode-default",
      modelProviderId: selectedModel.providerId, modelId: selectedModel.id, modelVariant,
      sessionId: terminalEdit ? current?.sessionId ?? "" : source === "existing" ? sessionID : "", automationMode,
      permissionApprovalMode,
      allowedDirectories: allowedDirectories.split(/\r?\n/).map((item) => item.trim()).filter(Boolean),
      supervisorModelProviderId: supervisorModel?.providerId ?? "", supervisorModelId: supervisorModel?.id ?? "",
      supervisorModelVariant: supervisorVariant, failureLimit,
      requireCompletionConfirmation: confirmation, goalCommandConfirmed: commandConfirmed, startNow,
    };
    try {
      const page = current ? await AppService.UpdateGoalLoop(current.id, input) : await AppService.CreateGoalLoop(input);
      onSaved(page);
    } catch (reason) {
      showToast(`保存失败：${errorMessage(reason)}`);
    } finally {
      setSaving(false);
    }
  };

  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}><section className="goal-modal" role="dialog" aria-modal="true" aria-label={current ? "编辑 Goal" : "创建 Goal"}>
    <header><div><span>Goal-based Loop</span><h2>{current ? "编辑 Goal 配置" : "创建 Goal"}</h2></div><button onClick={onClose} aria-label="关闭"><X size={18} /></button></header>
    <div className="goal-modal-body">
      <label className="loop-form-field"><span>目标</span><textarea value={goal} onChange={(event) => setGoal(event.target.value)} placeholder="清晰描述目标和可验证的完成条件，例如：完成实现、测试和验收。" autoFocus /></label>
      {terminalEdit ? <div className="infinite-note"><History size={17} /><span>重新启动将复用原项目与 Session；如需更换项目或 Session，请创建新 Goal。</span></div> : <div className="loop-source-tabs"><button type="button" className={source === "new" ? "active" : ""} onClick={() => { setSource("new"); setSessionID(""); }}>新建 Session</button><button type="button" className={source === "existing" ? "active" : ""} onClick={() => setSource("existing")}>接入现有 Session</button></div>}
      <div className="loop-form-grid"><label className="loop-form-field"><span>Agent</span><select value="opencode-default" disabled><option value="opencode-default">OpenCode</option><option disabled>Claude Code（即将支持）</option><option disabled>Codex（即将支持）</option></select><small>Goal 接管消息自动添加 /goal 前缀</small></label><label className="loop-form-field"><span>项目</span><select value={projectID} disabled={terminalEdit} onChange={(event) => { setProjectID(event.target.value); setSessionID(""); }}><option value="">选择已接入飞书的项目</option>{projects.map((project) => <option value={project.id} key={project.id}>{project.name} · {project.directory}</option>)}</select><small>{terminalEdit ? "终态编辑保留原项目" : "多个 Loop 同项目运行时推荐使用独立 worktree"}</small></label></div>
      {terminalEdit && current?.sessionId && <Detail label="复用 Session"><code>{current.sessionId}</code><small>保存配置不会启动；请返回详情页点击“重新启动”</small></Detail>}
      {!terminalEdit && source === "existing" && <label className="loop-form-field"><span>现有 Session</span><select value={sessionID} onChange={(event) => setSessionID(event.target.value)}><option value="">选择该项目的顶层 Session</option>{eligibleSessions.map((session) => <option value={session.id} key={session.id}>{session.title} · {shortID(session.id)} · {session.statusLabel}</option>)}</select><small>{selectedSession?.currentModel ? `默认继承原模型 ${selectedSession.currentModel}${selectedSession.currentVariant ? ` · ${selectedSession.currentVariant}` : ""}；你也可以修改，新模型从 /goal 下一轮生效。` : "当前执行不会被打断；现有权限和选择请求会立即由 Goal 接管。"}</small></label>}
      <div className="loop-model-config"><div className="loop-model-config-title"><span>执行模型</span><button type="button" onClick={() => void reloadModels()} disabled={modelsLoading}><RefreshCw size={13} className={modelsLoading ? "spin" : ""} />刷新模型</button></div><div className="loop-form-grid"><label className="loop-form-field"><span>模型</span>{models.length ? <select value={modelKey} onChange={(event) => { setModelKey(event.target.value); setModelVariant(""); }} disabled={modelsLoading}><option value="">{modelsLoading ? "正在读取模型…" : "请选择模型"}</option>{models.map((model) => <option value={`${model.providerId}\u0000${model.id}`} key={`${model.providerId}/${model.id}`}>{model.providerName || model.providerId} · {model.name || model.id}</option>)}</select> : <button type="button" className="loop-model-load" onClick={() => void reloadModels()} disabled={modelsLoading}><RefreshCw size={14} className={modelsLoading ? "spin" : ""} />{modelsLoading ? "正在读取模型…" : "加载模型"}</button>}<small className={modelsError ? "loop-form-error" : ""}>{selectedModel ? `${selectedModel.providerId}/${selectedModel.id}` : modelsError || "选择 Goal 每一轮使用的模型"}</small></label><label className="loop-form-field"><span>模型档位 / Variant</span><select value={modelVariant} onChange={(event) => setModelVariant(event.target.value)} disabled={!selectedModel || !(selectedModel.variants ?? []).length}><option value="">默认档位</option>{(selectedModel?.variants ?? []).map((variant) => <option value={variant} key={variant}>{variant}</option>)}</select><small>{(selectedModel?.variants ?? []).length ? "可选档位来自 OpenCode 模型配置" : "该模型没有额外档位"}</small></label></div></div>
      <div className="loop-mode-grid"><button type="button" className={automationMode === "autonomous" ? "active" : ""} onClick={() => setAutomationMode("autonomous")}><Sparkles size={17} /><span><strong>完全自主（推荐）</strong><small>自动处理权限、回答选择框并持续完成 Goal</small></span></button><button type="button" className={automationMode === "manual" ? "active" : ""} onClick={() => setAutomationMode("manual")}><ShieldCheck size={17} /><span><strong>人工监督</strong><small>在应用或飞书中处理权限和问题</small></span></button></div>
      {automationMode === "autonomous" && <div className="loop-model-config">
        <div className="loop-model-config-title"><span>权限审批策略</span><small>仅影响 Permission；选择框仍由 AI 处理</small></div>
        <div className="loop-mode-grid"><button type="button" className={permissionApprovalMode === "ai" ? "active" : ""} onClick={() => setPermissionApprovalMode("ai")}><ShieldCheck size={17} /><span><strong>AI 智能审批（推荐）</strong><small>监督模型结合 Goal、请求范围和安全规则判断</small></span></button><button type="button" className={permissionApprovalMode === "allow_all" ? "active" : ""} onClick={() => setPermissionApprovalMode("allow_all")}><Check size={17} /><span><strong>全部同意</strong><small>所有权限请求出现后立即允许一次，不经过 AI</small></span></button></div>
        {permissionApprovalMode === "allow_all" && <div className="loop-error"><CircleAlert size={16} /><span>全部同意会跳过 AI 与硬性安全检查，包括项目外目录、敏感文件和破坏性操作；每次请求只允许一次，不写入永久授权。</span></div>}
        <div className="loop-model-config-title"><span>AI 监督高级设置</span><small>独立 Session，只输出结构化决策</small></div>
        <div className="loop-form-grid"><label className="loop-form-field"><span>监督模型</span><select value={supervisorKey} onChange={(event) => { setSupervisorKey(event.target.value); setSupervisorVariant(""); }}><option value="">跟随 Goal 执行模型</option>{models.map((model) => <option value={`${model.providerId}\u0000${model.id}`} key={`supervisor-${model.providerId}/${model.id}`}>{model.providerName || model.providerId} · {model.name || model.id}</option>)}</select><small>用于选择框；AI 智能审批时也用于权限判断</small></label><label className="loop-form-field"><span>监督模型档位</span><select value={supervisorVariant} onChange={(event) => setSupervisorVariant(event.target.value)} disabled={!supervisorModel || !(supervisorModel.variants ?? []).length}><option value="">默认档位</option>{(supervisorModel?.variants ?? []).map((variant) => <option value={variant} key={variant}>{variant}</option>)}</select><small>仅影响权限和选择决策</small></label></div>
        {permissionApprovalMode === "ai" && <label className="loop-form-field"><span>额外允许路径（每行一个）</span><textarea className="compact" value={allowedDirectories} onChange={(event) => setAllowedDirectories(event.target.value)} placeholder="例如 C:\Users\name\Desktop 或 D:\shared\spec.html" /><small>可填写目录或明确文件；OpenCode 若按目录范围申请权限，请添加对应目录。硬性安全边界仍不可关闭。</small></label>}
      </div>}
      <div className="loop-form-grid"><label className="loop-form-field"><span>连续技术故障恢复阈值</span><input type="number" min="1" max="100" value={failureLimit} onChange={(event) => setFailureLimit(Math.max(1, Math.min(100, Number(event.target.value))))} /><small>达到阈值后重建监督 Session / 切换备用模型，并持续重试</small></label><label className="loop-check-card"><input type="checkbox" checked={confirmation} onChange={(event) => setConfirmation(event.target.checked)} /><span><strong>完成后需要人工确认</strong><small>默认关闭；关闭时收到完成标记即自动完成</small></span></label></div>
      <label className="goal-command-warning"><input type="checkbox" checked={commandConfirmed} onChange={(event) => setCommandConfirmed(event.target.checked)} /><CircleAlert size={18} /><span><strong>我已确认所选 Agent 支持 <code>/goal</code></strong><small>应用只添加命令前缀，不会安装或检测 Agent 的 /goal 能力。</small></span></label>
      <div className="infinite-note"><InfinityIcon size={17} /><span>Goal Loop 不设置最大轮数；临时故障会持续自动恢复，确定不存在安全路径时才标记为受阻。</span></div>
    </div>
    <footer><button className="secondary-button" onClick={onClose}>取消</button>{!current && <button className="secondary-button" disabled={saving || !selectedModel || (source === "existing" && !sessionID)} onClick={() => void save(false)}><Save size={14} />保存草稿</button>}<button className="primary-button" disabled={saving || !projects.length || !selectedModel || (source === "existing" && !sessionID)} onClick={() => void save(current ? false : true)}>{saving ? <LoaderCircle size={14} className="spin" /> : current ? <Save size={14} /> : <Play size={14} />}{current ? "保存修改" : "创建并启动"}</button></footer>
  </section></div>;
}

function ApprovalCard({ approval, busy, apply }: { approval: LoopApprovalView; busy: string; apply: (label: string, action: () => Promise<GoalLoopPage>) => Promise<boolean> }) {
  const [answers, setAnswers] = useState<string[]>((approval.questions ?? []).map(() => ""));
  if (approval.type === "permission") {
    return <article className="approval-card"><header><span className="permission"><ShieldCheck size={17} /></span><div><strong>Permission · {approval.permissionName}</strong><small>{approval.projectName} · {shortID(approval.sessionId)}{approval.autonomous ? " · 完全自主仅允许本次授权" : ""}</small></div>{approval.loopId && <em>Goal Loop</em>}</header>{(approval.patterns ?? []).length > 0 && <pre>{(approval.patterns ?? []).join("\n")}</pre>}<footer><button className="danger-button" disabled={!!busy} onClick={() => void apply("permission", () => AppService.ReplyLoopPermission(approval.id, approval.directory, "reject"))}>拒绝</button><button className="secondary-button" disabled={!!busy} onClick={() => void apply("permission", () => AppService.ReplyLoopPermission(approval.id, approval.directory, "once"))}>允许一次</button>{!approval.autonomous && <button className="primary-button" disabled={!!busy} onClick={() => void apply("permission", () => AppService.ReplyLoopPermission(approval.id, approval.directory, "always"))}>始终允许</button>}</footer></article>;
  }
  const questions = approval.questions ?? [];
  const setAnswer = (index: number, value: string) => setAnswers((current) => current.map((item, itemIndex) => itemIndex === index ? value : item));
  const toggleAnswer = (index: number, value: string) => {
    const selected = answers[index].split("\u0000").filter(Boolean);
    setAnswer(index, selected.includes(value) ? selected.filter((item) => item !== value).join("\u0000") : [...selected, value].join("\u0000"));
  };
  const submit = () => {
    const payload = answers.map((answer) => answer.split("\u0000").map((item) => item.trim()).filter(Boolean));
    void apply("question", () => AppService.ReplyLoopQuestion({ requestId: approval.id, directory: approval.directory, answers: payload, reject: false }));
  };
  return <article className="approval-card"><header><span className="question"><MessageSquareText size={17} /></span><div><strong>Question</strong><small>{approval.projectName} · {shortID(approval.sessionId)}</small></div>{approval.loopId && <em>Goal Loop</em>}</header><div className="question-list">{questions.map((question, index) => <label key={`${approval.id}-${index}`}><span>{question.header || `问题 ${index + 1}`}</span><strong>{question.question}</strong>{(question.options ?? []).length ? question.multiple ? <div className="question-options">{(question.options ?? []).map((option) => <label key={option.label}><input type="checkbox" checked={answers[index].split("\u0000").includes(option.label)} onChange={() => toggleAnswer(index, option.label)} /><span>{option.label}{option.description ? ` · ${option.description}` : ""}</span></label>)}</div> : <select value={answers[index]} onChange={(event) => setAnswer(index, event.target.value)}><option value="">请选择</option>{(question.options ?? []).map((option) => <option value={option.label} key={option.label}>{option.label}{option.description ? ` · ${option.description}` : ""}</option>)}</select> : <input value={answers[index]} onChange={(event) => setAnswer(index, event.target.value)} placeholder="输入回答" />}</label>)}</div><footer><button className="danger-button" disabled={!!busy} onClick={() => void apply("question", () => AppService.ReplyLoopQuestion({ requestId: approval.id, directory: approval.directory, answers: [], reject: true }))}>拒绝</button><button className="primary-button" disabled={!!busy || answers.some((answer) => !answer.trim())} onClick={submit}>提交回答</button></footer></article>;
}

function loopTone(status: string): string {
  return ({ running: "running", retrying: "retry", waiting_approval: "approval", waiting_takeover: "takeover", deciding: "deciding", paused: "paused", awaiting_confirmation: "confirmation", completed: "completed", blocked: "blocked", terminated: "terminated", draft: "draft" } as Record<string, string>)[status] ?? "draft";
}

function shortID(id: string): string { return id.length > 14 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id; }
function errorMessage(reason: unknown): string { return reason instanceof Error ? reason.message : String(reason); }
function relativeTime(value: string): string {
  const date = new Date(value); const elapsed = Date.now() - date.getTime();
  if (!value || Number.isNaN(date.getTime()) || date.getUTCFullYear() <= 1) return "—";
  if (elapsed < 60_000) return "刚刚"; const minutes = Math.floor(elapsed / 60_000);
  if (minutes < 60) return `${minutes} 分钟前`; const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`; const days = Math.floor(hours / 24);
  return days < 30 ? `${days} 天前` : date.toLocaleDateString("zh-CN");
}
