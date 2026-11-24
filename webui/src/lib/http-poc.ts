// Raw-HTTP PoC extraction.
//
// HTTP findings are proven by the request/response that was actually sent.
// The MITM archive is the authoritative copy, but it is not always there
// (proxy off, traffic from a path the archive never saw), and the model's
// exploitation proof routinely contains the same exchange pasted verbatim
// ("POST /x HTTP/1.1" + headers + body, then "HTTP/1.1 200 OK" + headers +
// body). This module pulls those blocks out so the detail view can show a
// raw HTTP PoC instead of a curl one-liner, WITHOUT inventing anything: every
// emitted block is a line-for-line slice of the input text.
//
// It is deliberately conservative — a block must start at a syntactically
// valid request line or status line, and prose (Chinese labels, 【…】 or ---
// separators) always ends it. When nothing parses, callers fall back to the
// finding's script/proof sections.

export interface RawHttpBlock {
  kind: "request" | "response";
  /** Verbatim lines of the exchange, with only the model's arrow prefixes
   *  (→ / >) removed — those are emphasis markers, not part of the packet. */
  raw: string;
}

const HTTP_METHODS = new Set([
  "GET",
  "POST",
  "PUT",
  "DELETE",
  "PATCH",
  "HEAD",
  "OPTIONS",
  "TRACE",
  "CONNECT",
]);

const EMPHASIS_PREFIX = /^\s*(?:→|➜|>)\s*/;

/** METHOD SP target [SP HTTP/x.y] [2+ spaces compact query/body] */
const REQUEST_LINE = /^([A-Z]{3,10})\s+(\S+)(?:\s+(HTTP\/\d(?:\.\d)?))?(?:\s{2,}\S.*)?$/;
/** HTTP/x.y STATUS [reason phrase] — leading emphasis arrows allowed. */
const STATUS_LINE = /^HTTP\/\d(?:\.\d)?\s+\d{3}(?:\s+[A-Za-z][A-Za-z0-9 _-]*)?$/;
const HEADER_LINE = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+:\s*\S.*$/;
/** Lines after a header/status that are body rather than prose. */
const BODY_START = /^[{<[]|^(?:[A-Za-z0-9_.-]+=[^&\s]*)(?:&[A-Za-z0-9_.-]+=[^&\s]*)*$/;
/** Prose separators / labels always terminate a block. */
const PROSE_LINE = /^[【\[]|^-{3,}$|^=+$|^[\u4e00-\u9fff]/;

function stripEmphasis(line: string): string {
  return line.replace(EMPHASIS_PREFIX, "");
}

/** True when the finding is an HTTP request/response class finding. */
export function isHttpFinding(finding: {
  method?: string;
  endpoint?: string;
  target?: string;
  traffic_refs?: unknown[];
}): boolean {
  const method = (finding.method ?? "").trim().toUpperCase();
  if (HTTP_METHODS.has(method)) return true;
  if ((finding.traffic_refs?.length ?? 0) > 0) return true;
  const haystack = `${finding.endpoint ?? ""} ${finding.target ?? ""}`;
  return /\bhttps?:\/\//i.test(haystack);
}

/**
 * Extract raw HTTP request/response blocks, in document order. Returns an
 * empty slice when the text carries no parseable exchange.
 */
export function extractRawHttpBlocks(text: string, maxBlocks = 8): RawHttpBlock[] {
  if (!text || maxBlocks <= 0) return [];
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const blocks: RawHttpBlock[] = [];

  let i = 0;
  while (i < lines.length && blocks.length < maxBlocks) {
    const trimmed = stripEmphasis(lines[i]).trim();
    const request = REQUEST_LINE.exec(trimmed);
    const isRequest = !!request && HTTP_METHODS.has(request[1]);
    const isResponse = STATUS_LINE.test(trimmed);
    if (!isRequest && !isResponse) {
      i++;
      continue;
    }

    const blockLines = [stripEmphasis(lines[i]).trimEnd()];
    i++;

    // Header section (requests and responses alike).
    while (i < lines.length) {
      const candidate = stripEmphasis(lines[i]).trim();
      if (!HEADER_LINE.test(candidate)) break;
      blockLines.push(stripEmphasis(lines[i]).trimEnd());
      i++;
    }

    // Body: either after a blank line, or a JSON/form line directly after the
    // start line (the proofs in the wild often omit the separating blank).
    let bodyStart = -1;
    if (i < lines.length && stripEmphasis(lines[i]).trim() === "") {
      const afterBlank = i + 1;
      if (afterBlank < lines.length && BODY_START.test(stripEmphasis(lines[afterBlank]).trim())) {
        blockLines.push("");
        bodyStart = afterBlank;
      }
    } else if (i < lines.length && BODY_START.test(stripEmphasis(lines[i]).trim())) {
      bodyStart = i;
    }
    if (bodyStart >= 0) {
      let j = bodyStart;
      while (j < lines.length) {
        const raw = stripEmphasis(lines[j]);
        const candidate = raw.trim();
        if (candidate === "") break;
        if (REQUEST_LINE.test(candidate) || STATUS_LINE.test(candidate)) break;
        if (PROSE_LINE.test(candidate)) break;
        blockLines.push(raw.trimEnd());
        j++;
      }
      i = j;
    }

    blocks.push({ kind: isRequest ? "request" : "response", raw: blockLines.join("\n") });
  }

  return blocks;
}
