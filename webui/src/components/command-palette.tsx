import { useEffect, useMemo } from "react";
import { Command } from "cmdk";
import { useNavigate } from "react-router-dom";
import { create } from "zustand";
import {
  FileText,
  LayoutGrid,
  Plug,
  Plus,
  Radio,
  Settings,
  ShieldAlert,
  Target,
} from "lucide-react";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { useInstances, useScansList } from "@/api/queries";
import { useI18n } from "@/i18n";

type PaletteState = {
  open: boolean;
  setOpen: (open: boolean) => void;
  toggle: () => void;
};

export const useCommandPalette = create<PaletteState>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
  toggle: () => set((s) => ({ open: !s.open })),
}));

const NAV_ITEMS = [
  { labelKey: "nav.overview", to: "/", icon: LayoutGrid },
  { labelKey: "tasks.create", to: "/tasks?create=1", icon: Plus },
  { labelKey: "nav.scans", to: "/scans", icon: Target },
  { labelKey: "nav.findings", to: "/findings", icon: ShieldAlert },
  { labelKey: "nav.live", to: "/live", icon: Radio },
  { labelKey: "nav.reports", to: "/reports", icon: FileText },
  { labelKey: "nav.integrations", to: "/integrations", icon: Plug },
  { labelKey: "nav.settings", to: "/settings", icon: Settings },
];

export function CommandPalette() {
  const open = useCommandPalette((s) => s.open);
  const setOpen = useCommandPalette((s) => s.setOpen);
  const { t } = useI18n();
  const navigate = useNavigate();
  const { data: instances } = useInstances();
  const { data: scans } = useScansList();

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.key === "k" || e.key === "K") && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen(!open);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, setOpen]);

  const go = (path: string) => {
    setOpen(false);
    navigate(path);
  };

  const scanItems = useMemo(() => {
    const map = new Map<string, { id: string; label: string }>();
    instances?.instances?.forEach((i) =>
      map.set(i.id, {
        id: i.id,
        label: i.name || i.targets || i.id,
      }),
    );
    scans?.forEach((s) => {
      if (!map.has(s.id)) {
        map.set(s.id, { id: s.id, label: s.target || s.id });
      }
    });
    return Array.from(map.values()).slice(0, 12);
  }, [instances, scans]);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="p-0 max-w-xl overflow-hidden">
        <Command label={t("palette.label")} className="flex flex-col">
          <Command.Input
            placeholder={t("palette.placeholder")}
            className="h-11 w-full border-b border-border bg-transparent px-4 text-sm outline-none placeholder:text-muted-foreground"
          />
          <Command.List className="max-h-96 overflow-y-auto p-2">
            <Command.Empty className="px-3 py-6 text-center text-xs text-muted-foreground">
              {t("palette.noResults")}
            </Command.Empty>
            <Command.Group heading={t("palette.navigate")} className="text-[10px] uppercase tracking-wide text-muted-foreground [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1">
              {NAV_ITEMS.map((n) => {
                const Icon = n.icon;
                return (
                  <Command.Item
                    key={n.to}
                    onSelect={() => go(n.to)}
                    className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm text-foreground aria-selected:bg-accent aria-selected:text-accent-foreground cursor-pointer"
                  >
                    <Icon className="h-3.5 w-3.5 text-muted-foreground" /> {t(n.labelKey)}
                  </Command.Item>
                );
              })}
            </Command.Group>
            {scanItems.length > 0 && (
              <Command.Group heading={t("scans.title")} className="text-[10px] uppercase tracking-wide text-muted-foreground [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1">
                {scanItems.map((s) => (
                  <Command.Item
                    key={s.id}
                    value={`scan ${s.label} ${s.id}`}
                    onSelect={() => go(`/scans/${s.id}`)}
                    className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm text-foreground aria-selected:bg-accent aria-selected:text-accent-foreground cursor-pointer"
                  >
                    <Target className="h-3.5 w-3.5 text-muted-foreground" />
                    <span className="truncate">{s.label}</span>
                    <span className="ml-auto text-[10px] text-muted-foreground mono">
                      {s.id.slice(0, 7)}
                    </span>
                  </Command.Item>
                ))}
              </Command.Group>
            )}
          </Command.List>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
