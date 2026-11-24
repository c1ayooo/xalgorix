import { useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { Pencil, Plus, Search, Sparkles, Trash2 } from "lucide-react";
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
import { Markdown } from "@/components/markdown";
import { menuContentClass, menuItemClass } from "@/lib/utils";
import { useI18n } from "@/i18n";
import {
  useSkills,
  useBuiltinSkills,
  useBuiltinSkill,
  useCreateSkill,
  useUpdateSkill,
  useDeleteSkill,
  useSkillInvocations,
} from "@/api/queries";
import type { BuiltinSkillEntry, InvocationListParams, UserSkill } from "@/types/api";

/**
 * 技能库（契约 §integration-skills）。内置语料（编译进二进制、只读）与
 * 操作者编写的用户技能合并展示；同名用户技能遮蔽内置条目。用户技能与
 * 内置语料在引擎侧合并进 read_skill/list_skills/search_skills。
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
          <SkillsLibraryTab />
        </TabsContent>
        <TabsContent value="records">
          <SkillsRecordsTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

/** 查看目标：内置按 name+category 懒加载正文，用户技能正文已在列表里。 */
type SkillViewTarget =
  | { kind: "builtin"; name: string; category: string }
  | { kind: "user"; skill: UserSkill };

const PAGE_SIZE = 60;

function SkillsLibraryTab() {
  const { t } = useI18n();
  const users = useSkills();
  const builtin = useBuiltinSkills();
  const update = useUpdateSkill();
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState("");
  const [limit, setLimit] = useState(PAGE_SIZE);
  const [viewing, setViewing] = useState<SkillViewTarget | null>(null);
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<UserSkill | null>(null);
  const [deleting, setDeleting] = useState<UserSkill | null>(null);

  const userSkills = users.data?.skills ?? [];
  const builtinSkills = builtin.data?.skills ?? [];
  const loading = users.isLoading || builtin.isLoading;
  const isError = users.isError || builtin.isError;
  const error = users.error ?? builtin.error;

  const categories = useMemo(() => {
    const set = new Set(builtinSkills.map((s) => s.category));
    if (userSkills.length > 0) set.add("user");
    return Array.from(set).sort();
  }, [builtinSkills, userSkills.length]);

  // 同名用户技能遮蔽内置条目（与引擎侧的合并语义一致）。
  const userNames = useMemo(() => new Set(userSkills.map((s) => s.name)), [userSkills]);

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    const matches = (haystack: string) => !q || haystack.includes(q);
    const userRows = userSkills
      .filter((s) => category === "" || category === "user")
      .filter((s) =>
        matches(
          [s.name, s.description, s.category, s.intent ?? "", ...(s.tags ?? [])]
            .join(" ")
            .toLowerCase(),
        ),
      )
      .map((s) => ({ kind: "user" as const, skill: s }));
    const builtinRows = builtinSkills
      .filter((s) => !userNames.has(s.name))
      .filter((s) => category === "" || category === s.category)
      .filter((s) =>
        matches(
          [s.name, s.description, s.category, s.intent ?? "", ...(s.tags ?? [])]
            .join(" ")
            .toLowerCase(),
        ),
      )
      .map((s) => ({ kind: "builtin" as const, skill: s }));
    return [...userRows, ...builtinRows];
  }, [builtinSkills, userSkills, userNames, query, category]);

  const visible = rows.slice(0, limit);
  const resetLimit = () => setLimit(PAGE_SIZE);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[12rem] flex-1 sm:max-w-xs">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              resetLimit();
            }}
            placeholder={t("skills.searchPlaceholder")}
            className="pl-8"
            aria-label={t("skills.searchPlaceholder")}
          />
        </div>
        <select
          value={category}
          onChange={(e) => {
            setCategory(e.target.value);
            resetLimit();
          }}
          aria-label={t("skills.categoryAll")}
          className="h-9 rounded-md border border-border bg-card px-3 text-sm"
        >
          <option value="">{t("skills.categoryAll")}</option>
          {categories.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
        <span className="text-xs text-muted-foreground tabular-nums">
          {t("skills.builtin")} {builtinSkills.length} · {t("skills.userLabel")} {userSkills.length}
        </span>
        <Button
          className="ml-auto"
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
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                void users.refetch();
                void builtin.refetch();
              }}
            >
              {t("common.retry")}
            </Button>
          }
        />
      ) : loading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-14 w-full" />
          ))}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<Sparkles className="h-6 w-6" />}
          title={t("skills.noMatchTitle")}
          description={t("skills.noMatchDescription")}
        />
      ) : (
        <div className="space-y-3">
          {visible.map((row) =>
            row.kind === "user" ? (
              <UserSkillRow
                key={`user:${row.skill.id}`}
                skill={row.skill}
                onView={() => setViewing({ kind: "user", skill: row.skill })}
                onEdit={() => {
                  setEditing(row.skill);
                  setFormOpen(true);
                }}
                onDelete={() => setDeleting(row.skill)}
                onToggle={(enabled) =>
                  update.mutate(
                    { id: row.skill.id, patch: { enabled } },
                    {
                      onSuccess: () => toast.success(t("toast.skillUpdated")),
                      onError: (err: Error) =>
                        toast.error(`${t("toast.skillFailed")}: ${err.message}`),
                    },
                  )
                }
              />
            ) : (
              <BuiltinSkillRow
                key={`builtin:${row.skill.category}:${row.skill.name}`}
                skill={row.skill}
                onView={() =>
                  setViewing({
                    kind: "builtin",
                    name: row.skill.name,
                    category: row.skill.category,
                  })
                }
              />
            ),
          )}
          {rows.length > limit ? (
            <div className="flex justify-center pt-1">
              <Button variant="outline" size="sm" onClick={() => setLimit((n) => n + PAGE_SIZE)}>
                {t("skills.showMore")} ({rows.length - limit})
              </Button>
            </div>
          ) : null}
        </div>
      )}

      <SkillDetailDialog target={viewing} onClose={() => setViewing(null)} />
      <SkillFormDialog open={formOpen} onOpenChange={setFormOpen} editing={editing} />
      <SkillDeleteDialog target={deleting} onClear={() => setDeleting(null)} />
    </div>
  );
}

function SkillTagBadges({ category, tags }: { category: string; tags?: string[] }) {
  const list = tags ?? [];
  return (
    <>
      {/* 用户技能的 category 恒为 "user"，与来源章重复，故不渲染。 */}
      {category && category !== "user" ? <Badge variant="outline">{category}</Badge> : null}
      {list.slice(0, 3).map((tag) => (
        <Badge key={tag} variant="outline">
          {tag}
        </Badge>
      ))}
      {list.length > 3 ? (
        <span className="text-[11px] text-muted-foreground tabular-nums">+{list.length - 3}</span>
      ) : null}
    </>
  );
}

function BuiltinSkillRow({ skill, onView }: { skill: BuiltinSkillEntry; onView: () => void }) {
  const { t } = useI18n();
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-card p-4 lg:flex-row lg:items-center">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm font-medium">{skill.name}</span>
          <Badge variant="muted">{t("skills.builtin")}</Badge>
          <SkillTagBadges category={skill.category} tags={skill.tags} />
        </div>
        <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">{skill.description}</p>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Button size="sm" variant="outline" onClick={onView}>
          {t("common.view")}
        </Button>
      </div>
    </div>
  );
}

function UserSkillRow({
  skill,
  onView,
  onEdit,
  onDelete,
  onToggle,
}: {
  skill: UserSkill;
  onView: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onToggle: (enabled: boolean) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-card p-4 lg:flex-row lg:items-center">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm font-medium">{skill.name}</span>
          <Badge variant="muted">{t("skills.userLabel")}</Badge>
          <SkillTagBadges category={skill.category} tags={skill.tags} />
        </div>
        <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">{skill.description}</p>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Switch checked={skill.enabled} onCheckedChange={onToggle} />
          {t("mcp.fieldEnabled")}
        </label>
        <Button size="sm" variant="outline" onClick={onView}>
          {t("common.view")}
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm" variant="outline">
              {t("common.actions")}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className={menuContentClass}>
            <DropdownMenuItem className={menuItemClass} onClick={onEdit}>
              <Pencil className="mr-2 h-3.5 w-3.5" />
              {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem
              className={`${menuItemClass} text-red-600 focus:text-red-600 dark:text-red-400`}
              onClick={onDelete}
            >
              <Trash2 className="mr-2 h-3.5 w-3.5" />
              {t("common.delete")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}

function SkillDetailDialog({
  target,
  onClose,
}: {
  target: SkillViewTarget | null;
  onClose: () => void;
}) {
  const { t } = useI18n();
  // Hook 无条件调用：只有内置目标才发请求，用户技能正文已在列表数据里。
  const builtinName = target?.kind === "builtin" ? target.name : null;
  const builtinCategory = target?.kind === "builtin" ? target.category : undefined;
  const detail = useBuiltinSkill(builtinName, builtinCategory);

  const userSkill = target?.kind === "user" ? target.skill : null;
  const name = target?.kind === "builtin" ? target.name : userSkill?.name ?? "";
  const category = target?.kind === "builtin" ? target.category : userSkill?.category ?? "";
  const description = target?.kind === "builtin" ? detail.data?.description : userSkill?.description;
  const tags = target?.kind === "builtin" ? detail.data?.tags : userSkill?.tags;
  const intent = target?.kind === "builtin" ? detail.data?.intent : userSkill?.intent;
  const body = userSkill ? userSkill.body : detail.data?.body;
  const loadingBody = target?.kind === "builtin" && detail.isLoading;
  const bodyError = target?.kind === "builtin" && detail.isError ? detail.error : null;

  return (
    <Dialog open={!!target} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-base">{name}</DialogTitle>
          <DialogDescription className="flex flex-wrap items-center gap-1.5">
            <Badge variant="muted">
              {target?.kind === "builtin" ? t("skills.builtin") : t("skills.userLabel")}
            </Badge>
            {category && category !== "user" ? (
              <Badge variant="outline">{category}</Badge>
            ) : null}
            {intent ? <Badge variant="outline">{intent}</Badge> : null}
            {(tags ?? []).map((tag) => (
              <Badge key={tag} variant="outline">
                {tag}
              </Badge>
            ))}
          </DialogDescription>
        </DialogHeader>
        {description ? <p className="text-sm text-muted-foreground">{description}</p> : null}
        <div className="max-h-[60vh] overflow-y-auto rounded-md border border-border bg-background/40 p-4">
          {loadingBody ? (
            <div className="space-y-2">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-4 w-full" />
              ))}
            </div>
          ) : bodyError ? (
            <ErrorState
              title={t("skills.detailFailed")}
              description={bodyError instanceof Error ? bodyError.message : ""}
              action={
                <Button size="sm" variant="outline" onClick={() => void detail.refetch()}>
                  {t("common.retry")}
                </Button>
              }
            />
          ) : (
            <Markdown source={body} />
          )}
        </div>
      </DialogContent>
    </Dialog>
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
