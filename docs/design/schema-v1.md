# Block schema v1 — the anti-drift contract (`schemaVersion = "wysiwyg-v1"`)

Canonical document = **ProseMirror doc JSON**. This file is the SINGLE schema both
renderers implement:

- the **editor** (in-frame ProseMirror `Schema`, Worker D), and
- the **server-side SSR read renderer** (pure Go `Render(doc) → HTML`, Worker E).

A block exists in v1 only if BOTH renderers handle it. Adding a block = update this
file + both renderers in the same change. Markdown export is a *lossy projection*
(§4); block-JSON is lossless and canonical.

Doc envelope: `{ "type": "doc", "content": [ <block>… ] }`.

---

## 1. Nodes

Group `block` unless noted. `inline*` = inline content (text + inline nodes).

| node | attrs | content | group | notes |
|---|---|---|---|---|
| `doc` | — | `block+` | — | root |
| `paragraph` | — | `inline*` | block | |
| `heading` | `{ level: 1..6 }` | `inline*` | block | |
| `blockquote` | — | `block+` | block | |
| `code_block` | `{ language: string\|null }` | `text*` (code, no marks) | block | fenced code |
| `divider` | — | — | block | horizontal rule |
| `bullet_list` | — | `list_item+` | block | |
| `ordered_list` | `{ start: int = 1 }` | `list_item+` | block | |
| `list_item` | — | `paragraph block*` | — | |
| `task_list` | — | `task_item+` | block | |
| `task_item` | `{ checked: bool = false }` | `paragraph block*` | — | checkbox |
| `callout` | `{ variant: "info"\|"warn"\|"success"\|"danger"\|"note" = "info", icon: string\|null }` | `block+` | block | admonition |
| `toggle` | `{ open: bool = false }` | `toggle_summary content` | block | collapsible `<details>` |
| `toggle_summary` | — | `inline*` | — | the summary line |
| `content` | — | `block+` | — | toggle body wrapper |
| `columns` | `{ count: 2\|3 = 2 }` | `column+` | block | multi-column row |
| `column` | — | `block+` | — | one column |
| `image` | `{ src, alt: string="", title: string\|null, width: int\|null }` | — | block | via `upload:images` |
| `table` | — | `table_row+` | block | prosemirror-tables model |
| `table_row` | — | `(table_cell \| table_header)+` | — | |
| `table_header` | `{ colspan=1, rowspan=1, colwidth:[int]\|null }` | `block+` | — | |
| `table_cell` | `{ colspan=1, rowspan=1, colwidth:[int]\|null }` | `block+` | — | |
| `text` | — | — | inline | text node |

`code_block` text must carry NO marks. Tables use the standard `prosemirror-tables`
node spec (`tableNodes({ tableGroup:"block", cellContent:"block+", cellAttributes:{}})`)
so the editor gets column resize/nav for free; the SSR renderer reads the same
`colspan/rowspan/colwidth` attrs.

## 2. Marks

| mark | attrs | notes |
|---|---|---|
| `strong` | — | bold |
| `em` | — | italic |
| `code` | — | inline code |
| `strike` | — | strikethrough |
| `underline` | — | |
| `link` | `{ href, title: string\|null }` | `rel="noopener"`, sanitized href |
| `textColor` | `{ color: string }` | must be a token ref, see §3 |
| `bgColor` | `{ color: string }` | highlight; token ref |

Mark order for serialization is canonical: `link` outermost, then
`strong, em, underline, strike, code`, then `textColor, bgColor` innermost.

## 3. Color values (token-only — Hard Rule 7)

`textColor.color` / `bgColor.color` are NOT raw hex. They are **named palette
slots** that map to design tokens, so colored text recolors with the theme and the
SSR renderer stays token-only:

`default, gray, brown, orange, yellow, green, blue, purple, pink, red`

- Editor renders them as `var(--wysiwyg-fg-<name>)` / `var(--wysiwyg-bg-<name>)`.
- The host token bridge (§7 of protocol) must supply these; if the host theme
  lacks them, the editor CSS provides `var(--wysiwyg-fg-blue, <fallback>)`
  fallbacks AND this is logged as a missing-token finding (add upstream later).
- The SSR renderer emits the same `var(--wysiwyg-fg-<name>)` via its registered
  stylesheet. Storing the *slot name* (not hex) is what keeps JSON portable and
  theme-correct.

## 4. Markdown projection (lossy, documented)

Markdown is export/interchange + the no-JS SSR fallback. Mapping:

| block | markdown | lossless? |
|---|---|---|
| paragraph, heading, blockquote, divider | native | ✅ |
| code_block | fenced ```lang | ✅ |
| bullet/ordered list | native | ✅ |
| task_list | `- [ ] ` / `- [x] ` | ✅ |
| image | `![alt](src "title")` | ✅ (width dropped) |
| table | GFM table | ✅ (only if cells are single-paragraph; nested blocks flattened) |
| link, strong, em, code, strike | native / `~~` | ✅ |
| underline | `<u>…</u>` (HTML passthrough) | ⚠️ |
| callout | `> **[variant]** …` blockquote form | ⚠️ variant kept as a marker line |
| toggle | `<details><summary>…</summary>…</details>` | ⚠️ HTML passthrough |
| columns | columns flattened to sequential blocks | ❌ layout lost |
| textColor/bgColor | dropped → plain text | ❌ color lost |

**Rule:** never *fail* on export. Non-representable structure degrades to the
closest markdown/HTML-passthrough form above; full fidelity always remains in the
JSON. The editor owns markdown serialization (it knows the blocks); the SSR
renderer prefers the JSON path and only falls back to `ui.Markdown` for the
representable core when JSON is absent.

## 5. SSR renderer contract (Worker E)

`func Render(doc map[string]any) render.HTML` — pure, deterministic, token-only:

- Walks the JSON; emits semantic, design-token HTML (`var(--*)` only, zero bespoke
  hex). Registers its stylesheet via `registry.RegisterStyle("wysiwyg-read", …)`
  (see core-ui). No inline styles except token variable assignment for colors.
- Escapes all text; sanitizes `link.href` (allow `http/https/mailto/relative`,
  drop `javascript:`); `image.src` likewise.
- Unknown node/mark types render their `content` (or nothing) rather than throwing
  — forward-compatible with a newer editor schema.
- Output is the no-JS first paint: real content, SEO-safe, hydrate-swappable by the
  editor iframe.

## 6. Editor schema contract (Worker D)

- Build a ProseMirror `Schema` with exactly the nodes/marks above (compose
  `prosemirror-schema-list` + `prosemirror-tables` where useful).
- `toDOM`/`parseDOM` token-only; colors via `var(--wysiwyg-*)`.
- Serialize to this JSON on `docChanged`/`save`; also emit markdown per §4.
- Slash menu, bubble toolbar, drag handles, table controls operate on these nodes.
