import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { Loader2, Rocket, ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useQuickCreateTask, useValidateProjectScope } from "@/api/queries";
import { useI18n } from "@/i18n";
import type { Project, ScopeValidateResult } from "@/types/api";

/**
 * 快速创建任务（项目页「快速创建」入口）——一次交互 = 目标 + 目标范围，
 * 保存即建任务并立即发起运行。
 *
 * 触发条件：项目卡片上的「快速创建」，或新建/改完范围后的引导条。
 * 输入：goal（必填）、name（可空，由 goal 派生）、targets（默认取项目 scope，
 *       可收窄不可越界）、intensity。
 * 输出：任务创建成功 + 运行 dispatch（两条契约端点，见 useQuickCreateTask）。
 *
 * 异常处理（顺序固定，任一步失败都不静默）：
 *   1. 目标先过 POST …/scope/validate 试算：范围本身有问题、或任一目标越界 →
 *      不创建任务，逐条列出原因；
 *   2. 任务创建失败 → toast 报错，弹窗保留输入；
 *   3. 任务已建但启动失败 → 明确提示「任务已保存，启动失败」，并跳到任务页
 *      让用户重试启动（不把已落库的任务当成失败丢掉）。
 *
 * 围栏衔接：项目围栏启用时，本弹窗只做「起跑前」的 dispatch 校验；运行期间
 * 命令 / 写请求仍由项目围栏在审批层逐次拦截，二者用同一个 Scope 对象。
 */
export function QuickTaskDialog({
  open,
  onOpenChange,
  project,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: Project | null;
}) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const validate = useValidateProjectScope();
  const quickCreate = useQuickCreateTask();

  const [goal, setGoal] = useState("");
  const [name, setName] = useState("");
  const [targets, setTargets] = useState("");
  const [intensity, setIntensity] = useState<"active" | "passive">("active");
  const [phase, setPhase] = useState<"idle" | "validating" | "creating">("idle");
  const [check, setCheck] = useState<ScopeValidateResult | null>(null);
  const [error, setError] = useState("");
  const projectId = project?.id;

  // 只在「打开」或「换项目」时重置：项目列表 15s 轮询会换掉 project 的对象
  // 身份，若把它放进依赖里，用户正在输入的目标会被轮询清空。
  useEffect(() => {
    if (!open) return;
    setGoal("");
    setName("");
    // 默认就是项目边界本身；用户可以收窄，越界会被试算挡下。
    setTargets((project?.scope.targets ?? []).join("\n"));
    setIntensity("active");
    setPhase("idle");
    setCheck(null);
    setError("");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, projectId]);

  const busy = phase !== "idle";
  const fenceOn = !!project?.scope.fence?.enforce;
  const parsedTargets = targets
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);

  async function submit() {
    if (!project || busy || !goal.trim()) return;
    if (parsedTargets.length === 0) {
      setError(t("scope.quickTargetsRequired"));
      return;
    }
    setError("");
    setPhase("validating");
    try {
      const result = await validate.mutateAsync({
        id: project.id,
        req: { targets: parsedTargets },
      });
      setCheck(result);
      const outside = result.entries.filter((e) => !e.in_scope);
      if (!result.ok || outside.length > 0) {
        // 边界不成立就不落库：否则会建出一个只能失败的任务。
        setPhase("idle");
        return;
      }
      setPhase("creating");
      const title = name.trim() || deriveName(goal);
      const { task, run, launchError } = await quickCreate.mutateAsync({
        projectId: project.id,
        req: {
          name: title,
          goal: goal.trim(),
          phase_policy: { scan_mode: "single", intensity },
        },
        launch: { targets: parsedTargets, scan_intensity: intensity },
      });
      if (launchError) {
        toast.error(t("scope.quickLaunchFailed"), {
          description: `${task.name} · ${launchError}`,
        });
        onOpenChange(false);
        navigate(`/tasks/${task.id}`);
        return;
      }
      toast.success(t("scope.quickCreated"), {
        description: run ? `${task.name} · ${run.status}` : task.name,
      });
      onOpenChange(false);
      navigate(`/tasks/${task.id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      toast.error(`${t("scope.quickFailed")}: ${err instanceof Error ? err.message : ""}`);
    } finally {
      setPhase("idle");
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent className="max-h-[88vh] max-w-xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("scope.quickTitle")}</DialogTitle>
          <DialogDescription>
            {t("scope.quickDescription")}
            {project && <span className="mono"> · {project.name}</span>}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="quick-goal">{t("tasks.fieldGoal")}</Label>
            <Textarea
              id="quick-goal"
              rows={3}
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder={t("tasks.goalPlaceholder")}
              disabled={busy}
            />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="quick-name">{t("scope.quickName")}</Label>
              <Input
                id="quick-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={deriveName(goal) || t("scope.quickNamePlaceholder")}
                disabled={busy}
              />
            </div>
            <div className="space-y-1.5">
              <Label>{t("scope.quickIntensity")}</Label>
              <Select
                value={intensity}
                onValueChange={(v) => setIntensity(v as "active" | "passive")}
                disabled={busy}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="active">{t("newScan.intensityActive")}</SelectItem>
                  <SelectItem value="passive">{t("newScan.intensityPassive")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="quick-targets">{t("scope.quickTargets")}</Label>
            <Textarea
              id="quick-targets"
              rows={2}
              value={targets}
              onChange={(e) => {
                setTargets(e.target.value);
                setCheck(null);
              }}
              placeholder={"acme.com"}
              disabled={busy}
            />
            <p className="text-[11px] text-muted-foreground">{t("scope.quickTargetsHint")}</p>
          </div>

          {fenceOn && (
            <p className="flex items-start gap-1.5 rounded-sm border border-border bg-muted/30 px-2 py-1.5 text-[11px] text-muted-foreground">
              <ShieldAlert className="mt-0.5 h-3 w-3 shrink-0" aria-hidden />
              {t("scope.quickFenceNotice")}
            </p>
          )}

          {error && (
            <p className="rounded-sm border border-red-500/30 bg-red-500/5 px-2 py-1.5 text-[11px] text-red-700 dark:text-red-400">
              {error}
            </p>
          )}

          {check && (
            <div className="space-y-1 rounded-md border border-border px-3 py-2">
              <p className="text-xs font-medium">{t("scope.checkTitle")}</p>
              {check.problems.map((p) => (
                <p key={p} className="text-[11px] text-red-700 dark:text-red-400">
                  · {p}
                </p>
              ))}
              {check.entries.map((entry) => (
                <p key={entry.target} className="flex flex-wrap items-baseline gap-x-2 text-[11px]">
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
                </p>
              ))}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" onClick={submit} disabled={busy || !goal.trim()}>
            {busy ? (
              <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Rocket className="mr-1 h-3.5 w-3.5" />
            )}
            {phase === "validating" ? t("scope.quickValidating") : t("scope.quickSubmit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** 任务名默认值：goal 的前 48 个字符（与后端「name 必填」对齐，不做隐式空名）。 */
function deriveName(goal: string): string {
  const trimmed = goal.trim().replace(/\s+/g, " ");
  if (!trimmed) return "";
  return trimmed.length > 48 ? `${trimmed.slice(0, 48)}…` : trimmed;
}
