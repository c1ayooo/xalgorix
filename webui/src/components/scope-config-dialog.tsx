import { useEffect, useState } from "react";
import { toast } from "sonner";
import { AlertTriangle, CheckCircle2, Loader2, ShieldCheck, ShieldOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import {
  ScopeFields,
  draftFromScope,
  draftProblems,
  draftTargets,
  draftToScope,
  emptyScopeDraft,
  type ScopeDraft,
} from "@/components/scope-fields";
import {
  useProjectScope,
  useUpdateProjectScope,
  useValidateProjectScope,
} from "@/api/queries";
import { useI18n } from "@/i18n";
import type { Project, ProjectScopeView, ScopeValidateResult } from "@/types/api";

/**
 * 范围配置模块（项目页「范围配置」入口）。
 *
 * 触发条件：项目卡片上的「范围配置」，或新建项目后的引导条。
 * 输入：platform.Scope 的两半（targets/allow_ports + fence）。
 * 输出：PUT /api/projects/{id}/scope → 归一化后的 ProjectScopeView。
 *
 * 状态一致性：
 *   - 打开时以服务端视图为准（不是列表里的旧副本），草稿只在打开瞬间初始化，
 *     轮询回来的新数据不会覆盖用户正在编辑的内容；
 *   - 保存成功后失效 projects + projectScope 两个缓存键，卡片上的围栏徽标、
 *     快速创建任务的可用性立即跟着变；
 *   - 校验（试算）不落库，只把后端判定逐条摊开给用户看。
 */
export function ScopeConfigDialog({
  open,
  onOpenChange,
  project,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: Project | null;
  onSaved?: (view: ProjectScopeView) => void;
}) {
  const { t } = useI18n();
  const projectId = project?.id;
  const { data, isLoading } = useProjectScope(open ? projectId : undefined);
  const save = useUpdateProjectScope();
  const validate = useValidateProjectScope();

  const [draft, setDraft] = useState<ScopeDraft>(emptyScopeDraft);
  const [initialized, setInitialized] = useState(false);
  const [check, setCheck] = useState<ScopeValidateResult | null>(null);
  const [serverProblems, setServerProblems] = useState<string[]>([]);

  // 每次打开重置草稿；数据到达后只初始化一次（不覆盖编辑中的内容）。
  useEffect(() => {
    if (!open) {
      setInitialized(false);
      setCheck(null);
      setServerProblems([]);
      setDraft(emptyScopeDraft());
      return;
    }
    if (!initialized && data) {
      setDraft(draftFromScope(data.scope));
      setInitialized(true);
    }
  }, [open, initialized, data]);

  const clientProblems = initialized ? draftProblems(draft) : [];
  const fenceOn = draft.enforce;

  function runCheck() {
    if (!projectId) return;
    validate
      .mutateAsync({
        id: projectId,
        req: { scope: draftToScope(draft), targets: draftTargets(draft) },
      })
      .then((result) => setCheck(result))
      .catch((err: Error) => toast.error(`${t("scope.validateFailed")}: ${err.message}`));
  }

  function submit() {
    if (!projectId) return;
    setServerProblems([]);
    save
      .mutateAsync({ id: projectId, scope: draftToScope(draft) })
      .then((view) => {
        toast.success(t("scope.saved"));
        if (view.warning) toast.message(t("scope.savedWithWarning"), { description: view.warning });
        onSaved?.(view);
        onOpenChange(false);
      })
      .catch((err: Error) => {
        setServerProblems([err.message]);
        toast.error(`${t("scope.saveFailed")}: ${err.message}`);
      });
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !save.isPending && onOpenChange(v)}>
      <DialogContent className="max-h-[88vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("scope.title")}</DialogTitle>
          <DialogDescription>
            {t("scope.description")}
            {project && <span className="mono"> · {project.name}</span>}
          </DialogDescription>
        </DialogHeader>

        {isLoading || !initialized ? (
          <div className="space-y-3">
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-40 w-full" />
          </div>
        ) : (
          <div className="space-y-4">
            <FenceStatusBanner view={data} fenceOn={fenceOn} />
            <ScopeFields
              draft={draft}
              onChange={(next) => {
                setDraft(next);
                setCheck(null);
              }}
              disabled={save.isPending}
              idPrefix="scope-config"
            />

            {clientProblems.length > 0 && (
              <ul className="space-y-1 text-[11px] text-amber-700 dark:text-amber-400">
                {clientProblems.map((key) => (
                  <li key={key}>· {t(key)}</li>
                ))}
              </ul>
            )}

            {serverProblems.length > 0 && (
              <ul className="space-y-1 rounded-sm border border-red-500/30 bg-red-500/5 px-2 py-1.5 text-[11px] text-red-700 dark:text-red-400">
                {serverProblems.map((p) => (
                  <li key={p}>· {p}</li>
                ))}
              </ul>
            )}

            {check && <CheckResult result={check} />}
          </div>
        )}

        <DialogFooter className="flex-col gap-2 sm:flex-row sm:justify-between">
          <Button
            variant="outline"
            size="sm"
            onClick={runCheck}
            disabled={validate.isPending || !initialized || clientProblems.length > 0}
          >
            {validate.isPending && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
            {t("scope.validate")}
          </Button>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => onOpenChange(false)}
              disabled={save.isPending}
            >
              {t("common.cancel")}
            </Button>
            <Button
              size="sm"
              onClick={submit}
              disabled={save.isPending || !initialized || clientProblems.length > 0}
            >
              {save.isPending && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
              {t("common.save")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** 围栏当前是否真的在拦：enforce 只是配置，HITL off 时它不生效。 */
function FenceStatusBanner({
  view,
  fenceOn,
}: {
  view?: ProjectScopeView;
  fenceOn: boolean;
}) {
  const { t } = useI18n();
  if (!view) return null;
  const bound = fenceOn && view.approval_mode === "enforce";
  return (
    <div className="space-y-1.5 rounded-md border border-border bg-muted/30 px-3 py-2">
      <div className="flex items-center gap-2 text-xs">
        {bound ? (
          <ShieldCheck className="h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400" aria-hidden />
        ) : (
          <ShieldOff className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
        )}
        <span className="font-medium">
          {bound ? t("scope.statusBound") : fenceOn ? t("scope.statusConfiguredOnly") : t("scope.statusOff")}
        </span>
        {view.active_runs > 0 && (
          <span className="mono text-[11px] text-amber-700 dark:text-amber-400">
            {t("scope.activeRuns")}: {view.active_runs}
          </span>
        )}
      </div>
      {!bound && fenceOn && (
        <p className="text-[11px] text-muted-foreground">{t("scope.statusHitlOffHint")}</p>
      )}
      {view.problems.length > 0 && (
        <ul className="space-y-0.5 text-[11px] text-amber-700 dark:text-amber-400">
          {view.problems.map((p) => (
            <li key={p}>· {p}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** 试算结果：每个候选目标一行，两个判定（dispatch + fence）合并成一句原因。 */
function CheckResult({ result }: { result: ScopeValidateResult }) {
  const { t } = useI18n();
  return (
    <div className="space-y-2 rounded-md border border-border px-3 py-2">
      <div className="flex items-center gap-2 text-xs font-medium">
        <CheckCircle2 className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
        {t("scope.checkTitle")}
        <span className="mono text-[11px] text-muted-foreground">
          {result.fence_bound ? t("scope.statusBound") : t("scope.statusConfiguredOnly")}
        </span>
      </div>
      {result.problems.length > 0 && (
        <ul className="space-y-0.5 text-[11px] text-red-700 dark:text-red-400">
          {result.problems.map((p) => (
            <li key={p}>
              <AlertTriangle className="mr-1 inline h-3 w-3" aria-hidden />
              {p}
            </li>
          ))}
        </ul>
      )}
      {result.entries.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">{t("scope.checkNoTargets")}</p>
      ) : (
        <ul className="space-y-1">
          {result.entries.map((entry) => (
            <li key={entry.target} className="flex flex-wrap items-baseline gap-x-2 text-[11px]">
              <span className="mono">{entry.target}</span>
              <span
                className={
                  entry.in_scope
                    ? "text-emerald-700 dark:text-emerald-300"
                    : "text-red-700 dark:text-red-400"
                }
              >
                {entry.in_scope ? t("scope.checkInside") : t("scope.checkOutside")}
              </span>
              {entry.reason && <span className="text-muted-foreground">{entry.reason}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
