import type { Approval, ApprovalRuleDraft } from "@/types/api";

// 从审批单 payload 提取「批准并记住」的默认规则草稿（契约 §3.4）。
// 纯函数：与后端 internal/approval 的匹配语义对齐，但不复制其安全判断——
// deny 形状（命令替换/重定向等）由后端在匹配期拒绝，这里只管预填。

// 命令草稿：首段程序 basename；contains 取命令里第一个 http(s) URL 的
// hostname（目标主机子串——操作者裁决的默认收窄粒度，可编辑/清空）。
export function draftCommandRule(command: string): ApprovalRuleDraft {
  return {
    kind: "command",
    match: {
      program: firstProgramBasename(command),
      contains: firstUrlHostname(command) ?? "",
    },
  };
}

// HTTP 草稿：method + hostname + 首段路径前缀（同 endpoint 反复 fuzzing 时
// 路径含变化 ID，首段前缀是可复用的最小稳定范围；UI 可编辑）。
export function draftHttpRule(
  method: string,
  url: string,
): ApprovalRuleDraft {
  return {
    kind: "http",
    match: {
      method: method.toUpperCase(),
      host: urlHostname(url),
      path_prefix: firstPathSegment(url),
    },
  };
}

// approvalRuleDraft 由审批单 kind 分派；无匹配材料的单返回 null。
export function approvalRuleDraft(approval: Approval): ApprovalRuleDraft | null {
  if (approval.kind === "command" && approval.payload.command) {
    return draftCommandRule(approval.payload.command);
  }
  if (
    approval.kind === "http" &&
    approval.payload.url &&
    approval.payload.method
  ) {
    return draftHttpRule(approval.payload.method, approval.payload.url);
  }
  return null;
}

// firstProgramBasename 提取首段的程序 basename：跳过前导 env 赋值
// （PATH=/x curl → curl），剥路径（/usr/bin/curl → curl）。
function firstProgramBasename(command: string): string {
  const firstSegment = command
    .split(/(?:&&|\|\||;|\||\n|`)/)
    .map((s) => s.trim())
    .find((s) => s.length > 0);
  if (!firstSegment) return "";
  const fields = firstSegment.split(/\s+/).filter(Boolean);
  let program = "";
  for (const f of fields) {
    if (f.includes("=") && !f.startsWith("-")) continue; // env assignment
    program = f;
    break;
  }
  const idx = program.lastIndexOf("/");
  return idx >= 0 ? program.slice(idx + 1) : program;
}

// firstUrlHostname 返回命令文本里第一个 http(s) URL 的 hostname。
function firstUrlHostname(command: string): string | undefined {
  const m = command.match(/https?:\/\/[^\s'"\\]+/i);
  return m ? urlHostname(m[0]) : undefined;
}

function urlHostname(url: string): string {
  const m = url.match(/^(?:https?:)?\/\/([^/?#\s@]+@)?([^/?#\s@]+)/i);
  if (!m) {
    // scheme-less 形态（api.example.com/x）：取首个 / 之前的 authority。
    const slash = url.indexOf("/");
    const authority = slash >= 0 ? url.slice(0, slash) : url;
    return authority.split("@").pop() ?? "";
  }
  return m[2];
}

function firstPathSegment(url: string): string {
  const noScheme = url.replace(/^[a-z][a-z0-9+.-]*:\/\//i, "");
  const slash = noScheme.indexOf("/");
  if (slash < 0) return "";
  const path = noScheme.slice(slash + 1).split(/[?#]/)[0];
  const seg = path.split("/").filter(Boolean)[0];
  return seg ?? "";
}
