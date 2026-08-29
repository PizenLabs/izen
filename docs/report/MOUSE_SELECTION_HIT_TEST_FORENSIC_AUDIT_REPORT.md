# Final Mouse Selection Forensic Audit: Rendered-Cell Hit Testing

**Status:** Audit Complete — Report Before Large Changes (Read-Only, No Code Changed)  
**Date:** 2026-08-29  
**Auditor:** Muse Spark (OpenCode)  
**Scope:** `logical content → rendered content → physical rows → terminal cells → selection hit-testing`  
**Invariant Under Audit:**
```
Mouse position
       ├──► Highlight
       └──► Copy
visible highlighted character == character returned by clipboard serializer
```

---

## 0. Executive Answer — 9 Decision Questions

| # | Question | Answer |
|---|----------|--------|
| **1** | Exact remaining source of coordinate drift | **Not a constant `Top`/`Left` offset.** Drift is structural: `mousePosToLogical()` (`internal/ui/selection.go:58-147`) assumes uniform word-wrap at `width-4` and a 1:1 `record → wrapRow` model, while the real renderer (`RenderDeterministicPipeline` / `renderCodeBlock` / `renderStreamingContent` in `internal/ui/stream_renderer.go`) uses per-block, per-line wrap budgets, markdown prefix reservation, code `width-6` hard-wrap, extra header/blank rows, and double-gutter composition. Result: `phys[]` approximates line count but **cell X → logical rune is wrong for every 2nd wrapped segment, every code line, every heading/list/bullet row, and every blank separator.** |
| **2** | Line mapping or cell mapping problem? | **Both, but cell mapping is the dominant user-visible bug.** Line mapping (`phys[]`) drifts around headings/code headers/blank separators (extra `\n` rows). Cell mapping (`cellToRuneCol` + `wrapOffset*wrapWidth+cellCol`) is fundamentally incorrect for word-wrapped content and for any row with a markdown prefix/gutter ≠ 2 cells. |
| **3** | Markdown introduces untracked geometry? | **Yes.** See Phase 3 table. |
| **4** | Prefixes/borders counted exactly once? | **No.** `gutterFor()` (`internal/ui/styles.go:485`) always subtracts 2 cells, but real rendered prefix is `0` (`roleUser`), `2` (`roleAI` outer `│ `), `2+2` (`roleAI` code inner `│ `), or `2` + bullet/number prefix (`2-4`). `viewportContentPrefixHeight` and `viewportGeometry` are correct for header geometry; the per-row prefix is not. |
| **5** | `phys[]` represents final rows? | **Approximately but not exactly.** `renderedLineCount:3852` = `count(renderRecordForViewport,\n)+1` does call the authoritative renderer, so it includes header rows and blank `\n` separators. But it is recomputed with a **different width contract** than the composed viewport (`width-4` vs `width-2/availableWidth/innerW/markdownLinePrefixWidth`) and the `strings.TrimSuffix(...,"\n")` / `"\n"` join in `refreshViewportContent:3445-3650` creates a one-row accumulation divergence for multi-block AI answers. |
| **6** | Renderer exposes enough metadata? | **No.** Renderer returns `string` only (`RenderDeterministicPipeline(string,int,bool) string`, `renderStreamingContent(string,int) string`, `renderRecordForViewport(record) string`). No `Rows`, no `HitMap`, no per-cell `→ logical rune` table. Selection rebuilds geometry independently with a second wrapping algorithm. |
| **7** | Shared `RenderLayout`/`HitMap` required? | **Yes.** Minimum fix is a renderer-owned `RenderedContent{Text,Rows,HitMap}` produced atomically with `Text`. Selection must consume it, not re-derive `wrapWidth`. |
| **8** | Lightweight feel — latency or geometry? | **Geometry, not latency.** `MouseActionMotion` (`internal/ui/update.go:2847`) updates `Cursor=mousePosToLogical(); refreshViewportContent()` **synchronously**, no `tea.Cmd`, no tick. The “lightweight” feel is highlight visibly lagging by `1-3` cells because `injectStyleRange` highlights the wrong rune range. |
| **9** | Minimum implementation needed | **(a)** Renderer returns `Layout` (`Rows []Row{PrefixCells,LogicalLine,LogicalColStart,CellWidths}`) atomically with `Text`. **(b)** Cache layout per record / composed viewport and make `mousePosToLogical` a `cell→span→rune` lookup into that cache (use `runewidth`/`lipgloss.Width` once in renderer). **(c)** Fix highlight to be viewport-overlay on rendered rows (not `injectStyleRange` on logical cols). **(d)** Single prefix ownership (renderer). No `+1/-1` tuning. |

---

## Phase 1 — Trace the REAL Render Pipeline to `Viewport.SetContent()`

### 1.1 End-to-End Transformation Chain

```
record.text
  │
  ├─► sanitizeText  (render_helper.go:34)
  │     normalizeLineEndings (\r\n→\n, \r→\n)
  │     sanitizeEscapes (\\n→\n, \\t→\t, \"→")
  │     expandTabs (\t → 4 spaces, tabWidth=4)
  │
  ├─► renderRecordForViewport  (model.go:2923)
  │     width clamp: <40→40, wrapWidth=width-4
  │     role switch:
  │       ├─ roleUser:  dimmedStyle(@name) + userBgStyle(" "+text)   // NO gutter
  │       ├─ roleAI:    renderAIResponseBlocks → renderStreamingContent (stream_renderer.go:377)
  │       │               parseAIContent (view.go:1324) splits ``` fences into block* 
  │       │               per block:
  │       │                 blockPlan/diff/table/evidence/risk/command → renderWidget (view.go:1285) adds "│ " anchors
  │       │                 default → RenderDeterministicPipeline(block.raw, availableWidth=width-2) (stream_renderer.go:40)
  │       │                   innerW=width-4, wrapW=innerW - markdownLinePrefixWidth(line) (stream_renderer.go:86-93)
  │       │                   ansi.Wordwrap(line, wrapW) → per subLine renderDeterministicInlineMarkdown (stream_renderer.go:94-100)
  │       │                     heading → "\n"+mdH*Style  // blank separator physical row
  │       │                     blockquote → "┃ " + applyInlineStyles
  │       │                     bullet/list → "• "/"1."+" " + applyInlineStyles
  │       │                     checkbox → Icon+" "+ applyInlineStyles
  │       │                   inCodeBlock → renderCodeBlock(lang, lines, width) (stream_renderer.go:173)
  │       │                     header: dimmedStyle("│ ")+langLabel  // +1 row not in logical text
  │       │                     codeWidth=width-6, per-token rune hard-wrap, gutter "│ " per emitted line
  │       │               default markdown block post: gutter="│ " + styledLine (stream_renderer.go:395-641)
  │       │               result = Join(renderedBlocks, vspace)  // extra "\n" between widgets
  │       ├─ roleActivity/roleError/roleStatus/default:
  │       │     per srcLine wrapIndentedLine(srcLine, wrapWidth=width-4) (model.go:2968)
  │       │     chunkWord at cell boundaries via ansi.Cut (render_helper.go:54)
  │       │
  │       └─► renderBounded(TrimRight " ") + "\n" per wrapped line, TrimSuffix final "\n"
  │
  ├─► refreshViewportContent (model.go:3453)
  │     prefix = renderStartupBanner + renderContextHeader (view.go:50) + renderWorkspaceHeader (model.go:4009)
  │              counted via viewportContentPrefixHeight (geometry.go:88)
  │     body   = (inViMode ? renderRecordsWithCursor : mouseSel.Active ? renderRecordsWithMouseSelection : PreRenderedHistory)
  │              // renderRecordsWithMouseSelection re-calls renderRecordForViewport per record + injectStyleRange + "\n" join
  │            + Execution Log + shimmerDock + execView + streamed streamBlocks + thinking + trace + ActivityTree
  │     Viewport.SetContent(content.String())  (model.go:3645/3649)
  │     // bubbles/viewport stores string; View() slices by YOffset
  │
  └─► assembleScreen (view.go:111) → Partition(body=Viewport.View(), Header=renderFixedHeader, Footer=renderFixedFooter)
        viewportGeometry.Top = headerLines = countLines(headerView) (geometry.go:75)
        Height = m.height - headerLines - (3+autoH) -1 - footerLines - proposalH (geometry.go:69)
        Left=0, Width=m.Viewport.Width
```

### 1.2 First Divergence — Width Contract

| Layer | Width Used | Source |
|-------|------------|--------|
| `renderRecordForViewport` (non-AI) | `width-4` | `model.go:2929` |
| `RenderDeterministicPipeline` inner | `width-4` | `stream_renderer.go:86` |
| `RenderDeterministicPipeline` wrap | `innerW - markdownLinePrefixWidth(line)` (`innerW-0..3`) | `stream_renderer.go:90` |
| `renderCodeBlock` | `width-6` | `stream_renderer.go:180` |
| `renderStreamingContent` available | `width-2` | `stream_renderer.go:382` |
| `selection.go` assumed wrap | `width-4` uniform | `selection.go:125` |

Every list/bullet/blockquote/heading/code row therefore wraps at a different column than selection assumes. `phys[]` built via `renderedLineCount` (which *does* call `renderRecordForViewport`, so row-count divergence is small for simple paragraphs) is correct for row *count* but **column geometry diverges immediately**.

### 1.3 Is `phys[]` the Same Rows the Terminal Receives?

`renderedLineCount` (`model.go:3852`):
```go
rendered := m.renderRecordForViewport(rec)
return strings.Count(rendered, "\n") + 1
```

* Calls authoritative renderer — includes lang header, blank `\n` separators, word-wrap sublines.
* But re-derives with `width-4` vs composed viewport's `availableWidth/innerW/markdownLinePrefixWidth` → per-record `phys[i+1]-phys[i]` diverges by `1..3` rows for the issue's example (heading `\n` + code `│ go` header + ordered-list prefix reservation).
* `TrimSuffix(...,"\n")` in `renderRecordForViewport:2975/2988` vs `refreshViewportContent:3476` joining records with `"\n"` — accumulation counts `n` lines but composition joins with `"\n"` between records; for 1-record geometry tests this matches, for multi-block AI answers it drifts.

**Answer:** No — `phys[]` describes *approximately* the same rows for narrow plain-text tests (`selection_geometry_test.go` with `roleAI` single-line plain text) but **not** for the issue's example (heading, blank separator, `go` lang header, code block with indentation, ordered list).

---

## Phase 2 — Distinguish Line Mapping From Cell Mapping

### 2.1 What Exists

```
record → physRowCount  (number of rendered rows)
terminal Y → recordRow = YOffset + relY - prefix → linear scan phys[] → record idx
wrapOffset = recordRow - phys[idx]   // which wrap segment inside that record
```

`selection.go:109-147` for X:

```go
gutterWidth = lipgloss.Width(ansi.Strip(gutterFor(idx))) // always 2
cellCol = msg.X - Left - gutterWidth
wrapWidth = width-4
totalCells = wrapOffset*wrapWidth + cellCol   // assumes previous rows full
col = cellToRuneCol(idx, totalCells)         // walks plain stripped text runes with runewidth
```

### 2.2 What Is Missing

```
terminal cell X
      │
      ▼
  Rendered Row (which physical row, with its PrefixCells)
      │
      ▼
  Rendered Span / rune (which Cells slice inside that row)
      │
      ▼
  Logical character (LogicalCol0 + span index)
```

### 2.3 Why Current Cell Mapping Is Insufficient

* **`gutterWidth` constant `2`** — fails for `roleUser` (`0`), double-code gutter (`4`), list prefix after gutter (`4-6`). Every code/list row off by `0..2`.
* **`wrapOffset*wrapWidth` assumes uniform fill** — real word-wrap leaves previous row `~12` cells short (word boundary), so `+wrapWidth` overshoots by `12` on the 2nd segment.
* **`cellToRuneCol` walks plain `records[idx].text`** — not rendered spans. Markdown `**bold**` (4 runes) → rendered `bold` (4 printable but different indices), `• ` prefix not in plain text, code header `go` not in plain text, inline ``code`` backticks stripped. Column index is into a different string than the screen shows.
* **Highlight divergence** — `renderRecordsWithMouseSelection:369` (`selection.go:369-402`) highlights via `injectStyleRange(rendered, sCol, eCol, viSelectionBgStyle)` where `sCol/eCol` are logical plain-text indices, but `injectStyleRange:3798` (`model.go:3798`) counts printable chars across `TokenText` tokens of the *rendered* string. When `renderedPrintable != plainRunes` (bold, bullet, code header), they select different characters.

**Critical invariant violation:** `serializeMouseSelection:183` (`selection.go:183`) slices `records[line].text` runes `[sCol:eCol]`; highlight slices rendered printable chars `[sCol:eCol]`. Exactly the failure mode the issue describes.

---

## Phase 3 — Audit Markdown / Styled Content

| Structure | Logical col change? | Physical rows / prefix change? | Tracked in selection metadata? |
|-----------|---------------------|-------------------------------|-------------------------------|
| Heading `# ` → `"\n"+mdH1Style` (`stream_renderer.go:141-143`) | No | **+1 blank separator row** before heading | Row counted in `phys[]` via `count("\n")` but column still uses plain offset |
| Blank line (`TrimSpace==""` → `"\n"`) (`stream_renderer.go:71-73`) | No | **+1 empty physical row** (gutter-only `│ `) | Counted in `phys[]`, but `mousePosToLogical` treats it as continuation of previous record's wrapping |
| Ordered list `1. ` → `mdBulletStyle("1.")+" "+content` (`stream_renderer.go:151-154`) | Shifts `0`→`3` | `wrapW = innerW-3`, prefix `3` | **Not reserved** — `width-4` overshoots by `1` |
| Unordered `- ` → `• ` (`stream_renderer.go:146-148`) | `2→2` (coincidentally `1+space`) | `wrapW = innerW-2` | **Not reserved** — overshoots `2` |
| Checkbox `- [x]` → `✔ ` (`stream_renderer.go:161-163`) | `5→2` | `wrapW = innerW-2` | **Not reserved** |
| Blockquote `> ` → `┃ ` (`stream_renderer.go:122-124`) | `2→2` | `wrapW=innerW-2` | Not reserved but drift `0` by luck |
| Inline `**bold**`/`*italic*`/``code`` (`incremental_parser.go:238`) | **Strips markers** — `**a**` (4 runes) → `a` (1 printable) | No row change | **Column index into stripped text ≠ rendered printable index** — off by `2` per `**` pair |
| `vspace` between widgets (`renderStreamingContent:650` `Join(..., vspace)`) | No | `+1` empty `"\n"` between blocks | Inside single AI record; `phys[]` includes it via `renderedLineCount`, but `wrapOffset*wrapWidth` assumes uniform rows, not widget gaps |

**Answer:** Yes — renderer changes both logical column positions (marker stripping) and physical geometry (extra rows, per-line prefix widths, per-block wrap budgets) without updating `phys[]`-plus-`cellCol` metadata.

*Does the Markdown renderer produce additional physical rows or prefixes not represented in the selection mapping?* **Yes — heading leading `\n`, code lang header `│ go`, blank separator `│ ` rows, block `vspace`, and per-line `markdownLinePrefixWidth` reservation.**

---

## Phase 4 — Audit Code Blocks

Logical:

```
package main
import "fmt"
func main() {
    fmt.Println("Hello, World!")
}
```

Rendered via `renderCodeBlock:173-284` (AI path):

* Row 0: `│ go`  (lang header `+1` row, `PrefixCells=2`, `LogicalLine=-1`)
* Row 1: `│ package main` (`PrefixCells=2`, `LogicalLine=0`, `LogicalCol0=0`)
* Row 2: `│ ` (blank separator)
* Row 3: `│ import "fmt"`
* Row 4: `│ `
* Row 5: `│ func main() {`
* Row 6: `│     fmt.Println("Hello, World!")` — 4-space indent after `│ ` → screen `│ ····fmt` (gutter `2` + indent `4` = `6` cells before `f`). Selection does `cellCol = X-2`, missing `4` (indent) and missing inner gutter `2` when double composition applies.

Wrapping: `codeWidth=width-6` (`stream_renderer.go:180`), hard wrap per rune with `runewidth.RuneWidth`. Selection assumes `width-4`. For `80`-wide terminal, code wrap `74` vs selection `76` — every long code row drifts `2` cells per segment.

ANSI: Chroma `lexer.Tokenise` → `ansiStart + chunk + ansiReset` per chunk (`stream_renderer.go:274`). `cellToRuneCol` strips ANSI via `ansi.Strip(m.records[idx].text)` (plain text has no ANSI) — zero-width ANSI correctly ignored via `lipgloss.Width`, but the walk is over the wrong string.

Required mapping for `p a c k a g e`:

```
terminal X
 → minus viewport Left (0)
 → minus outer gutter "│ " (2)
 → minus inner code gutter "│ " (2)   // currently missing → +2 drift
 → minus code indent (0 or 4)          // missing when indent present → +4 drift
 → minus Chroma ANSI zero-width
 → runewidth walk into logicalLine runes
```

Indentation invariant: `    fmt.Println(...)` — 4 spaces are logical runes, correctly counted by `cellToRuneCol`'s walk (spaces are `1` cell each), but only after the two gutter subtractions. Currently subtracts `2` not `4`, so hit-test for indented code is `2-4` cells off, exactly the issue's “indented content” drift.

---

## Phase 5 — Audit Prefix / Border Ownership

| Prefix `│` | Owner | Counted in selection? |
|----------|-------|-----------------------|
| Actual viewport content | **Yes** — `renderRecordForViewport`, `renderCodeBlock:194/248`, `renderStreamingContent:395` outer `gutter="│ "` embed `│` into string passed to `Viewport.SetContent`. `Viewport.View()` adds **no** border inside viewport. | Selection subtracts via `gutterFor` (constant `2`) — but ownership of *how many* cells is not shared. |
| Container/border | Outer screen border `bannerBorderStyle` and `rule(width, borderColor)` above input (`view.go:135`) — **outside viewport**, not inside viewport content. No prefix there. | Correctly excluded; `viewportGeometry.Top=headerLines` (`geometry.go:75`) owns header, no double-count. |
| `printRecord:1203` (`view.go:1203`) vs `renderRecordForViewport:2923` | `printRecord` is legacy scrollback (`flushRecord→tea.Println`), adds `gutter + style` per wrapped line. `renderRecordForViewport` is viewport path — **two independent gutter insertions**, different contracts (`printRecord` uses `gutterFor+outputStyle`, `renderRecordForViewport` for AI embeds `gutterAIStyle` inside `stream_renderer` *and* widget borders). | Selection reads `gutterFor` (legacy contract) for the viewport path — mismatch. |
| Final screen assembly | `assembleScreen:111` (`view.go:111`) → `Partition(body=Viewport.View(), Header, Footer)` — adds **no** prefix inside viewport. | Correct; `viewportGeometry.Left=0`, `Width=m.Viewport.Width` — no border compensation needed. |

**Common failure mode** `renderer adds prefix + selection subtracts prefix + viewport adds border`:

* Renderer adds `1` (or `2` for code where both `renderCodeBlock` inner `│ ` and `renderStreamingContent` outer `│ ` compose) → **double-gutter contract violation** (`refreshViewportContent:3478` composes them; example output shows single `│` visually but layout has `│ │ ` logically).
* Selection subtracts `1` constant (`2` cells) → net error `±0..2` varying per row type, not constant `±1`.
* Viewport adds `0` inside viewport — correct.
* **Verdict: prefixes are NOT counted exactly once.** Fix is not `+1/-1` but **renderer-owned `PrefixCells` per row, consumed by selection**.

---

## Phase 6 — Use Actual Cell Width

Current X handling (`selection.go:118-168`) **does** use cell space correctly at the entry:

* `gutterWidth = lipgloss.Width(ansi.Strip(...))` — cells, not bytes, not `len()`.
* `cellCol = X - gutterWidth` — cells.
* `cellToRuneCol` walks `runewidth.RuneWidth(r)` — cells → rune index. CJK (`2`), emoji (`2`), ASCII (`1`), tabs already `expandTabs` (`tabWidth=4`, `render_helper.go:34`), indentation via `leadingWhitespace`/`wrapIndentedLine` — handled for non-AI.

**What is missing is *which string's* cells are walked.** Tabs/indentation inside AI markdown after a prefix are *inside* `applyInlineStyles(content)` content, not accounted as `PrefixCells` in the selection model. So `cellCol` includes indent cells as if they were content, while `LogicalCol` should be `indent + contentOffset`; currently `Col = cell→rune` on flat `ansi.Strip(records[idx].text)` which *does* include indent runes — coincidentally correct for plain indented `fmt.Println` (4 spaces are runes), but wrong when that indent is **after** an extra gutter (`│ `) not subtracted.

Correct contract:

```
terminal X (cells, tea.MouseMsg)
 → rendered cell column = X - viewport.Left - Row.PrefixCells  (per-row, not global gutterWidth)
 → rune span = walk Row.CellWidths (per-rune widths from renderer, same runewidth pass)
 → logical character = Row.LogicalCol0 + span
```

`Row.CellWidths` must come from the same `ansi`/`runewidth` pass the renderer used (`render_helper.go:54` `chunkWord` via `ansi.Cut`, `stream_renderer.go:258` `runewidth.RuneWidth`), not a second `ansi.Strip + runewidth` pass on plain text.

---

## Phase 7 — Add a Render-to-Hit-Test Contract

**Current violation:** renderer returns `string` only — no layout. No `HitMap`, no `RenderLayout`, no per-cell mapping. Selection rebuilds geometry independently with a second wrapping algorithm, second prefix calc — exactly the prohibited duplication:

* `markdownLinePrefixWidth` duplicated between `render_helper.go:144` / `stream_renderer.go:90` and implicit assumption in `selection.go:125`.
* `codeWidth=width-6` vs `width-4` duplicated.
* `ansi.Wordwrap` vs `wrapIndentedLine` duplicated.

**Required contract (shape illustrative, names may differ):**

```go
type RenderedContent struct {
    Text string
    Rows []RenderedRow
}
type RenderedRow struct {
    PrefixCells   int   // "│ " / "┃ " / "• " / "1. " / code "│ " — renderer-authoritative
    LogicalLine   int   // index into record.text logical lines, -1 for header/blank/widget chrome
    LogicalCol0   int   // rune offset of first content rune on this physical row
    Cells         []int // per-rune cell widths for content runes on this row (runewidth)
    Plain         string // plain content slice for this row (for serialize without ANSI)
}
```

Principle: **`RenderedContent.Rows` is the `HitMap`.** The component that creates geometry (`stream_renderer.go`, `render_helper.go:wrapIndentedLine/chunkWord`) is authoritative for `HitMap`. `selection.go` becomes a pure lookup: `Y → Row` (`Rows[YOffset+relY]`), `X → Cells` walk. No second `wrapWidth`, no second `gutterFor`. Never scrape terminal, never parse `Viewport.View()` output — build from structured render representation.

---

## Phase 8 — Investigate “Lightweight” Feel

Measured (code inspection, `internal/ui/update.go:2815-2843`):

* `MouseMotion` at `tea.EnableMouseCellMotion` — high frequency (per cell). Handler `update.go:2847`:

```go
case tea.MouseActionMotion:
    if m.mouseSel.Dragging {
        m.mouseSel.lastY = msg.Y; m.mouseSel.lastX = msg.X
        m.mouseSel.Cursor = m.mousePosToLogical(msg)
        m.refreshViewportContent()   // synchronous
        // edge auto-scroll single loop, 80ms bounded
        if inEdge && !TickActive { TickActive=true; return tickCmd }
        return m, nil
    }
```

Processes **synchronously**: `Cursor=mousePosToLogical(); refreshViewportContent(); return nil` — **no `tea.Cmd`, no tick, no async, no frame behind pointer**. `Press:2840` similarly synchronous. Render is immediate (`refreshViewportContent` rebuilds `PreRenderedHistory` + `SetContent` before next `View()`).

* Auto-scroll: bounded `80ms` single-loop, `TickActive` guard (`selection.go:35`, `80ms` at `selection.go:38`), velocity `1..2`, recomputes `maxOff` from `prefix+Σ renderedLineCount`. It extends selection via `Cursor=mousePosToLogical(X,Y)` on each tick (`selection.go:330`) — correct ownership, not fighting streaming (`gotoBottomIfAllowed:3969` bails if `userIsScrollingUp||mouseSel.Dragging`).

* No viewport async update — `Viewport.Update(msg)` only for wheel (`update.go:2830`).

**Conclusion:** subjective “lightweight” feel is **geometry, not latency**. Highlight visibly lags by `1-3` cells because `injectStyleRange` highlights the wrong rune range (Phase 2). There is no one-frame highlight lag; if there were, `Motion` would need a `tea.Cmd` — it returns `nil`. The `80ms` tick only drives edge auto-scroll, which is correct to remain rate-limited.

Do **not** increase global render frequency; keep synchronous `Motion → hit-test → selection update → render` and keep edge auto-scroll `80ms` bounded.

---

## Phase 9 — Do Not Regress Auto-Scroll

Audited invariants — all preserved and tested (`selection_geometry_test.go:298-379`):

* **One active loop:** `TickActive bool` (`selection.go:31`) + `handleSelectionAutoScroll:268` early-exit `if !Active||!Dragging → TickActive=false`.
* **Velocity-based:** `dist = edgeRows - relY` → `delta -1/-2` or `+1/+2` (`selection.go:276-298`).
* **Anchor stability:** `Anchor` never mutated after `Press:2841`; only `Cursor` moves on motion/tick. `TestAutoScroll_AnchorStability` passes.
* **Streaming ownership:** `gotoBottomIfAllowed:3969` (`model.go:3969`) bails if `userIsScrollingUp||mouseSel.Dragging`; `userIsScrollingUp=true` on `Press:2843`.
* **No duplicate ticks:** `if inEdge && !TickActive { TickActive=true; return tickCmd }` (`update.go:2858`) and tick handler re-arms only if `TickActive` still true and `delta≠0` and not clamped (`selection.go:302-345`).

Only safe responsiveness improvement: tick recomputes `Cursor` from **current** `msg.X/Y` (already at `selection.go:330`) so viewport move doesn't leave cursor stale — already correct.

---

## Golden Manual Test — Expected Outcome Under Current Code

Long assistant response with paragraph + heading + blank + ordered/unordered lists + code block + indented code + inline code + link + Unicode:

* **A-C (paragraph):** highlight ≈ correct (plain paragraph uses same `width-4` wrap as selection; no markdown prefix, so cell mapping error `0`).
* **Heading:** click on heading's leading `│ ` prefix — maps to `col 0` but heading's logical col is `0` after stripped `# ` — **off by `2`** due to leading `\n` separator row mapping to wrong `recordRow`.
* **Lists:** ordered `1.` drift `1`, bullets drift `2` — drag across 5 list items accumulates visible offset, paste contains neighboring lines.
* **Code block:** indented `fmt.Println` requires pointer `4` cells right of visible `f` to hit `f`; plain `package` requires `2` cells right — **non-constant** drift.
* **E-H edge scroll:** drag to bottom edge → viewport scrolls `80ms` velocity steps, anchor stable, but `Cursor` on scrolled rows still drifts by same cell error, so paste after hold is **wrong by same per-row delta**.

Highlighted content ≠ clipboard — invariant fails for every structured block.

**Performance (current):** `Motion` does `O(n)` `phys[]` rebuild + `renderRecordForViewport` per motion → at `25k` rows, per-motion cost `O(25k)` string renders, will spike CPU. Current tests at `50` records pass; at `25k` expect `>16ms` motion latency → frame drops. Layout caching (Phase 7) fixes this.

---

## Files Referenced

* `internal/ui/selection.go:58-168` — drift source (`wrapOffset*wrapWidth`, global `gutterWidth`, plain-text `cellToRuneCol`, `renderRecordsWithMouseSelection:369` highlight vs `serializeMouseSelection:183` copy divergence)
* `internal/ui/geometry.go:26-108` — authoritative `viewportGeometry` (correct `Top`/`Height`/`Left`/`Width`, `viewportContentPrefixHeight` prefix)
* `internal/ui/model.go:2923-2990` `renderRecordForViewport`, `3852` `renderedLineCount` (`count("\n")+1`), `3453-3650` `refreshViewportContent` (prefix + `SetContent` + `Partition`), `3969` `gotoBottomIfAllowed`, `4009` `renderWorkspaceHeader`, `3798` `injectStyleRange`
* `internal/ui/stream_renderer.go:40-109` `RenderDeterministicPipeline` (per-line `wrapW=innerW-markdownLinePrefixWidth`, `ansi.Wordwrap`, heading `"\n"+mdH*`), `173-284` `renderCodeBlock` (`codeWidth=width-6`, header `+1` row, `│ ` per line, `runewidth` hard-wrap), `377-661` `renderStreamingContent` (double gutter composition at `395` `gutter="│ "+line`, widget `gutter`)
* `internal/ui/render_helper.go:34-168` `sanitizeText`/`expandTabs`/`wrapText`/`markdownLinePrefixWidth` (prefix width table selection duplicates)
* `internal/ui/view.go:50` `renderContextHeader`, `76` `renderWorkspace`, `111` `assembleScreen` (`Partition` ownership), `1203` `printRecord` (legacy scrollback gutter)
* `internal/ui/styles.go:485` `gutterFor` (constant `2` vs per-row `PrefixCells`)
* `internal/ui/update.go:2815-2843` `MouseMsg` handling (synchronous, no lag), `2847` `Motion` path
* `internal/ui/selection_geometry_test.go` — current tests pass only for plain single-line AI records without headings/lists/code; they do not cover drift cases (wrapped second-row `Col`, CJK, gutter constant `2`, `Top==0` with `nil` ctx).

---

## Final Quality Target — Gap Analysis

Desired:

```
mouse pointer → exact terminal cell → exact visible char/span → highlight follows pointer → drag→edge → smooth 80ms auto-scroll → release → exact highlighted text copied
```

Current gap: **`terminal cell → rendered span → logical rune` is missing.** The rest of the chain (geometry `Top/Left/Height`, synchronous motion, `80ms` single-loop scroll, `userIsScrollingUp` streaming freeze) is correct. Filling the missing layer via renderer-owned `HitMap` closes the gap without touching unrelated rendering, clipboard, or Markdown rewriting and without reintroducing screen parsing.

---

*End of report. No code was changed. Next step is the shared `RenderedContent`/`HitMap` implementation described in Phases 7 & 10.*
