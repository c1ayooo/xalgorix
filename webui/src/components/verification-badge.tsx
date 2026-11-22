import { ShieldCheck, ShieldAlert } from "lucide-react";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";

export const TAG_VERIFIED = "verified";
export const TAG_MANUAL_REVIEW = "needs-manual-verification";

// VerificationBadge shows, at a glance, whether a finding was independently
// reproduced by the Verifier ("Verified") or preserved but still needs a human
// to confirm it ("Manual verification needed"). It reads the machine tags the
// backend attaches, falling back to the boolean `verified` flag.
export function VerificationBadge({
  verified,
  tags,
  className,
}: {
  verified?: boolean;
  tags?: string[];
  className?: string;
}) {
  const { t } = useI18n();
  // When tags are present they are authoritative (the backend sets exactly one
  // verification tag). Only fall back to the boolean `verified` for legacy
  // records that predate tags — otherwise a first-party-proof finding
  // (verified=true but tagged manual-review) would wrongly show as Verified.
  const hasTags = (tags?.length ?? 0) > 0;
  const isVerified = hasTags
    ? tags!.includes(TAG_VERIFIED)
    : verified === true;
  const needsReview = hasTags
    ? tags!.includes(TAG_MANUAL_REVIEW)
    : verified === false;

  if (isVerified) {
    return (
      <span
        className={cn(
          "inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide",
          "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/30",
          className,
        )}
        title={t("badge.verifiedTitle")}
      >
        <ShieldCheck className="h-3 w-3" aria-hidden />
        {t("badge.verified")}
      </span>
    );
  }
  if (needsReview) {
    return (
      <span
        className={cn(
          "inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide",
          "bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/30",
          className,
        )}
        title={t("badge.manualReviewTitle")}
      >
        <ShieldAlert className="h-3 w-3" aria-hidden />
        {t("badge.manualReview")}
      </span>
    );
  }
  return null;
}
