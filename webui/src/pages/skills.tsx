import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { Pencil, Plus, Sparkles, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { EmptyState, ErrorState } from "@/components/states";
import { InvocationRecords } from "@/components/invocation-records";
import { menuContentClass, menuItemClass } from "@/lib/utils";
import { useI18n } from "@/i18n";
import {
  useSkills,
  useCreateSkill,
  useUpdateSkill,
  useDeleteSkill,
  useSkillInvocations,
} from "@/api/queries";
import type { InvocationListParams, UserSkill } from "@/types/api";

/**
 * 用户技能管理（契约 §integration-skills）。用户技能与内置语料在引擎侧
 * 合并（read_skill/list_skills/search_skills），本页只管理 user 语料。
 */
export default function SkillsPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = searchParams.get("tab") === "records" ? "records" : "config";
  const setTab = (v: string) =>
    setSearchParams(v === "config" ? {} : { tab: v }, { replace: true });

  return (
    <div className="space-y-6">
      <header>
        <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("skills.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("skills.subtitle")}</p>
      </header>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="config">{t("skills.tabConfig")}</TabsTrigger>
          <TabsTrigger value="records">{t("skills.tabRecords")}</TabsTrigger>
        </TabsList>
        <TabsContent value="config">
          <SkillsConfigTab />
        </TabsContent>
        <TabsContent value="records">
          <SkillsRecordsTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function SkillsConfigTab() {
  const { t } = useI18n();
  const { data, isLoading, isError, error, refetch } = useSkills();
  const update = useUpdateSkill();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<UserSkill | null>(null);
  const [deleting, setDeleting] = useState<UserSkill | null>(null);

  const skills = data?.skills ?? [];

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <Button
          onClick={() => {
            setEditing(null);
            setFormOpen(true);
          }}
        >
          <Plus className="mr-1 h-4 w-4" />
          {t("skills.create")}
        </Button>
      </div>

      {isError ? (
        <ErrorState
          title={t("skills.loadFailed")}
          description={error instanceof Error ? error.message : ""}
          action={
            <Button size="sm" variant="outline" onClick={() => refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-14 w-full" />
          ))}
        </div>
      ) : skills.length === 0 ? (
        <EmptyState
          icon={<Sparkles className="h-6 w-6" />}
          title={t("skills.emptyTitle")}
          description={t("skills.emptyDescription")}
          action={
            <Button
              onClick={() => {
                setEditing(null);
                setFormOpen(true);
              }}
            >
              <Plus className="mr-1 h-4 w-4" />
              {t("skills.create")}
            </Button>
          }
        />
      ) : (
        <div className="space-y-3">
          {skills.map((k) => (
            <div
              key={k.id}
              className="flex flex-col gap-3 rounded-md border border-border bg-card p-4 lg:flex-row lg:items-center"
            >
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-sm font-medium">{k.name}</span>
                  <Badge variant="muted">{k.category}</Badge>
                  {(k.tags ?? []).map((tag) => (
                    <Badge key={tag} variant="outline">
                      {tag}
                    </Badge>
                  ))}
                </div>
                <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">{k.description}</p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Switch
                    checked={k.enabled}
                    onCheckedChange={(v) =>
                      update.mutate(
                        { id: k.id, patch: { enabled: v } },
                        {
                          onSuccess: () => toast.success(t("toast.skillUpdated")),
                          onError: (err: Error) =>
                            toast.error(`${t("toast.skillFailed")}: ${err.message}`),
                        },
                      )
                    }
                  />
                  {t("mcp.fieldEnabled")}
                </label>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button size="sm" variant="outline">
                      {t("common.actions")}
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" className={menuContentClass}>
                    <DropdownMenuItem
                      className={menuItemClass}
                      onClick={() => {
                        setEditing(k);
                        setFormOpen(true);
                      }}
                    >
                      <Pencil className="mr-2 h-3.5 w-3.5" />
                      {t("common.edit")}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      className={`${menuItemClass} text-red-600 focus:text-red-600 dark:text-red-400`}
                      onClick={() => setDeleting(k)}
                    >
                      <Trash2 className="mr-2 h-3.5 w-3.5" />
                      {t("common.delete")}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            </div>
          ))}
        </div>
      )}

      <SkillFormDialog open={formOpen} onOpenChange={setFormOpen} editing={editing} />
      <SkillDeleteDialog target={deleting} onClear={() => setDeleting(null)} />
    </div>
  );
}

function SkillsRecordsTab() {
  const { t } = useI18n();
  const [params, setParams] = useState<InvocationListParams>({ page: 1, size: 25 });
  const query = useSkillInvocations(params);
  return (
    <InvocationRecords
      query={query}
      params={params}
      onParamsChange={setParams}
      namePlaceholder={t("skills.filterName")}
    />
  );
}

const NAME_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;

function SkillFormDialog({
  open,
  onOpenChange,
  editing,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  editing: UserSkill | null;
}) {
  const { t } = useI18n();
  const create = useCreateSkill();
  const update = useUpdateSkill();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [tags, setTags] = useState("");
  const [intent, setIntent] = useState("");
  const [body, setBody] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [formError, setFormError] = useState("");

  const [wasOpen, setWasOpen] = useState(false);
  if (open && !wasOpen) {
    setName(editing?.name ?? "");
    setDescription(editing?.description ?? "");
    setTags((editing?.tags ?? []).join(", "));
    setIntent(editing?.intent ?? "");
    setBody(editing?.body ?? "");
    setEnabled(editing?.enabled ?? true);
    setFormError("");
    setWasOpen(true);
  } else if (!open && wasOpen) {
    setWasOpen(false);
  }

  function submit() {
    setFormError("");
    const trimmedName = name.trim().toLowerCase();
    if (!NAME_PATTERN.test(trimmedName)) {
      setFormError(t("skills.nameFormatError"));
      return;
    }
    if (!description.trim()) {
      setFormError(t("skills.descriptionRequired"));
      return;
    }
    if (!body.trim()) {
      setFormError(t("skills.bodyRequired"));
      return;
    }
    const tagList = tags
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter(Boolean);
    const onSaved = (isEdit: boolean) => {
      toast.success(isEdit ? t("toast.skillUpdated") : t("toast.skillCreated"));
      onOpenChange(false);
    };
    if (editing) {
      update.mutate(
        {
          id: editing.id,
          patch: {
            name: trimmedName,
            description: description.trim(),
            tags: tagList,
            intent: intent.trim() || undefined,
            body,
            enabled,
          },
        },
        {
          onSuccess: () => onSaved(true),
          onError: (err: Error) => toast.error(`${t("toast.skillFailed")}: ${err.message}`),
        },
      );
    } else {
      create.mutate(
        {
          name: trimmedName,
          description: description.trim(),
          tags: tagList,
          intent: intent.trim() || undefined,
          body,
          enabled,
        },
        {
          onSuccess: () => onSaved(false),
          onError: (err: Error) => toast.error(`${t("toast.skillFailed")}: ${err.message}`),
        },
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? t("skills.editTitle") : t("skills.createTitle")}</DialogTitle>
          <DialogDescription>{t("skills.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="skill-name">{t("skills.fieldName")}</Label>
              <Input id="skill-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="testing-price-tampering" className="font-mono" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="skill-tags">{t("skills.fieldTags")}</Label>
              <Input id="skill-tags" value={tags} onChange={(e) => setTags(e.target.value)} placeholder="payment, business-logic" />
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="skill-description">{t("skills.fieldDescription")}</Label>
            <Input id="skill-description" value={description} onChange={(e) => setDescription(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="skill-intent">{t("skills.fieldIntent")}</Label>
            <select
              id="skill-intent"
              value={intent}
              onChange={(e) => setIntent(e.target.value)}
              className="h-9 w-full rounded-md border border-border bg-card px-3 text-sm"
            >
              <option value="">{t("skills.intentOffensive")}</option>
              <option value="defensive">{t("skills.intentDefensive")}</option>
            </select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="skill-body">{t("skills.fieldBody")}</Label>
            <Textarea id="skill-body" rows={12} value={body} onChange={(e) => setBody(e.target.value)} className="font-mono" placeholder={"# Testing Payment Price Tampering\n\n1. Replay the checkout request with a modified price…"} />
          </div>
          <label className="flex items-center gap-2 text-sm">
            <Switch checked={enabled} onCheckedChange={setEnabled} />
            {t("mcp.fieldEnabled")}
          </label>
          {formError ? <p className="text-xs text-red-600 dark:text-red-400">{formError}</p> : null}
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" onClick={submit} disabled={create.isPending || update.isPending}>
            {editing ? t("common.save") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function SkillDeleteDialog({ target, onClear }: { target: UserSkill | null; onClear: () => void }) {
  const { t } = useI18n();
  const del = useDeleteSkill();
  return (
    <AlertDialog open={!!target} onOpenChange={(v) => !v && onClear()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("skills.deleteTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("skills.deleteDescriptionPrefix")} “{target?.name}”.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={() => {
              if (!target) return;
              del.mutate(target.id, {
                onSuccess: () => toast.success(t("toast.skillDeleted")),
                onError: (err: Error) => toast.error(`${t("toast.skillFailed")}: ${err.message}`),
              });
              onClear();
            }}
          >
            {t("common.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
