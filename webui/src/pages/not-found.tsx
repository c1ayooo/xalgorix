import { Link } from "react-router-dom"
import { Button } from "@/components/ui/button"
import { useI18n } from "@/i18n"

export default function NotFoundPage() {
  const { t } = useI18n()
  return (
    <div className="flex min-h-[60vh] flex-col items-center justify-center gap-4 text-center">
      <div className="font-mono text-xs uppercase tracking-wider text-muted-foreground">404</div>
      <h1 className="font-sans text-3xl font-semibold tracking-tight">{t("notfound.title")}</h1>
      <p className="max-w-sm text-sm text-muted-foreground">
        {t("notfound.description")}
      </p>
      <Button asChild variant="outline">
        <Link to="/">{t("notfound.back")}</Link>
      </Button>
    </div>
  )
}
