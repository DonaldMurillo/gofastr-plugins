// Search — full-document text search with match counting, next/previous, and
// visible highlight of the current match (scroll-into-view).
//
// The text index is built INCREMENTALLY, yielding to the event loop between
// pages so a large document never freezes the main thread (there are no workers
// under the framed CSP). Indexing is idempotent and lazy: it runs on first
// search, reporting progress ("Indexing 12 / 240") so the user sees activity.

import type { PdfModel } from "./pdfdoc";
import { castTextContent, isTextItem, resetSpanText, type PdfTextContent, type TextSpanRef } from "./textlayer";
import { el } from "./dom";

export interface SearchStatus {
  query: string;
  count: number;        // total matches (0 until index + scan complete)
  index: number;        // 1-based current match; 0 when none
  indexing: boolean;
  indexedPages: number;
  totalPages: number;
}

interface Match {
  pageIndex: number;
  offset: number;       // char offset into the page's joined text
  length: number;
}

export class SearchController {
  private pageText: string[] = [];
  private indexed = 0;
  private indexedOnce = false;
  private indexPromise: Promise<void> | null = null;
  private matches: Match[] = [];
  private current = -1;
  private query = "";
  private caseSensitive = false;
  private wholeWord = false;

  constructor(
    private readonly model: PdfModel,
    private readonly onPageJump: (page: number) => void,
    private readonly onChange: (s: SearchStatus) => void
  ) {}

  status(): SearchStatus {
    return {
      query: this.query,
      count: this.matches.length,
      index: this.current < 0 ? 0 : this.current + 1,
      indexing: this.indexPromise !== null && !this.indexedOnce,
      indexedPages: this.indexed,
      totalPages: this.model.pageCount,
    };
  }

  // Build the per-page text index incrementally. Idempotent; concurrent callers
  // share one indexing pass.
  ensureIndex(onProgress?: (loaded: number, total: number) => void): Promise<void> {
    if (this.indexedOnce) return Promise.resolve();
    if (!this.indexPromise) this.indexPromise = this.runIndex(onProgress);
    return this.indexPromise;
  }

  private async runIndex(onProgress?: (loaded: number, total: number) => void): Promise<void> {
    this.pageText = new Array(this.model.pageCount).fill("");
    for (let i = 0; i < this.model.pageCount; i++) {
      const p = this.model.getPage(i);
      if (p) {
        try {
          const tc = (await p.getTextContent()) as unknown as PdfTextContent;
          this.pageText[i] = castTextContent(tc).items.filter(isTextItem).map((it) => it.str).join("");
        } catch {
          this.pageText[i] = "";
        }
      }
      this.indexed = i + 1;
      onProgress?.(i + 1, this.model.pageCount);
      this.emit();
      if (i + 1 < this.model.pageCount) await new Promise<void>((r) => setTimeout(r, 0));
    }
    this.indexedOnce = true;
    this.indexPromise = null;
  }

  async search(query: string, opts: { caseSensitive?: boolean; wholeWord?: boolean }): Promise<void> {
    this.query = query;
    this.caseSensitive = !!opts.caseSensitive;
    this.wholeWord = !!opts.wholeWord;
    this.matches = [];
    this.current = -1;
    if (!query) {
      this.emit();
      return;
    }
    await this.ensureIndex();
    const needle = this.caseSensitive ? query : query.toLowerCase();
    for (let i = 0; i < this.pageText.length; i++) {
      const hay = this.caseSensitive ? this.pageText[i] : this.pageText[i].toLowerCase();
      let from = 0;
      while (from <= hay.length) {
        const idx = hay.indexOf(needle, from);
        if (idx < 0) break;
        if (!this.wholeWord || this.isWordBoundary(hay, idx, idx + needle.length)) {
          this.matches.push({ pageIndex: i, offset: idx, length: needle.length });
        }
        from = idx + needle.length;
      }
    }
    if (this.matches.length > 0) {
      this.current = 0;
      await this.gotoCurrent();
    }
    this.emit();
  }

  private isWordBoundary(s: string, start: number, end: number): boolean {
    const re = /[\p{L}\p{N}]/u;
    const before = start > 0 ? s[start - 1] : "";
    const after = end < s.length ? s[end] : "";
    const lb = before === "" || !re.test(before);
    const rb = after === "" || !re.test(after);
    return lb && rb;
  }

  next(): Promise<void> {
    if (this.matches.length === 0) return Promise.resolve();
    this.current = (this.current + 1) % this.matches.length;
    return this.gotoCurrent();
  }

  prev(): Promise<void> {
    if (this.matches.length === 0) return Promise.resolve();
    this.current = (this.current - 1 + this.matches.length) % this.matches.length;
    return this.gotoCurrent();
  }

  clear(): void {
    this.query = "";
    this.matches = [];
    this.current = -1;
    this.emit();
  }

  private async gotoCurrent(): Promise<void> {
    if (this.current < 0 || this.current >= this.matches.length) return;
    const m = this.matches[this.current];
    // Jump first (renders the page + builds its text layer); the viewer then
    // calls applyHighlight() for that page to mark the match.
    this.onPageJump(m.pageIndex + 1);
    this.emit();
  }

  // Does the current match live on this page? The viewer asks after building a
  // page's text layer so it can mark + scroll the match into view.
  activeMatchForPage(pageIndex: number): Match | null {
    if (this.current < 0) return null;
    const m = this.matches[this.current];
    return m && m.pageIndex === pageIndex ? m : null;
  }

  // Mark the current match within a page's built spans. Clears any prior mark in
  // the same spans first. No-op when the active match is on another page.
  applyHighlight(pageIndex: number, spans: TextSpanRef[]): void {
    for (const ref of spans) resetSpanText(ref);
    const m = this.activeMatchForPage(pageIndex);
    if (!m) return;
    let consumed = 0;
    const matchStart = m.offset;
    const matchEnd = m.offset + m.length;
    for (const ref of spans) {
      const len = ref.str.length;
      if (matchEnd > consumed && matchStart < consumed + len) {
        const localStart = Math.max(0, matchStart - consumed);
        const localEnd = Math.min(len, matchEnd - consumed);
        const before = ref.str.slice(0, localStart);
        const mark = ref.str.slice(localStart, localEnd);
        const after = ref.str.slice(localEnd);
        ref.span.replaceChildren(document.createTextNode(before));
        const mk = el("mark", {
          cls: "pdf-search-mark is-current",
          text: mark,
          attrs: { role: "mark" },
        });
        ref.span.appendChild(mk);
        ref.span.appendChild(document.createTextNode(after));
        mk.scrollIntoView({ block: "center", inline: "center" });
        return;
      }
      consumed += len;
    }
  }

  private emit(): void {
    this.onChange(this.status());
  }
}
