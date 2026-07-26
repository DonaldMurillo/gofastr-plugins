// RedactPanel — the redaction surface (mode === "redact").
//
// Owns: the pending-redactions list (with jump-to + delete), the "other
// occurrences" assist, the arm/confirm modal that names the irreversible
// consequence ("N pages will be permanently rasterized at D DPI and their text
// removed"), a progress indicator while rasterization runs on the main thread,
// and a clear result state showing the verification report.
//
// The panel never touches the bridge or the doc model directly — it calls back
// to the editor, which routes through OverlayDoc commands and the viewer's
// requestExport. Token-only CSS, keyboard-operable, ≥44px tap targets.

import { el } from "../dom";
import type { Redaction } from "../doc";
import type { VerifyReport } from "./verify";
import type { OtherOccurrence } from "./capture";

export interface RedactPanelCallbacks {
  onJumpTo: (page: number) => void;
  onDelete: (id: string) => void;
  onApply: () => void;
  onAddOccurrences: (needle: string, occ: OtherOccurrence[]) => void;
}

export class RedactPanel {
  readonly root: HTMLElement;
  private readonly listEl: HTMLElement;
  private readonly bodyEl: HTMLElement;
  private readonly cb: RedactPanelCallbacks;
  private modal: HTMLElement | null = null;

  constructor(cb: RedactPanelCallbacks) {
    this.cb = cb;
    this.root = el("section", { cls: "pdf-redact-panel", attrs: { "aria-label": "Redaction" } });
    this.root.appendChild(el("h3", { cls: "pdf-redact-title", text: "Redaction" }));
    const hint = el("p", { cls: "pdf-redact-hint" });
    hint.appendChild(el("span", { text: "Draw rectangles over content to remove. Redaction is " }));
    hint.appendChild(el("strong", { text: "irreversible" }));
    hint.appendChild(el("span", { text: " — covered pages become images and lose text searchability." }));
    this.root.appendChild(hint);
    this.listEl = el("ul", { cls: "pdf-redact-list", role: "list", ariaLabel: "Pending redactions" });
    this.root.appendChild(this.listEl);
    this.bodyEl = el("div", { cls: "pdf-redact-body", attrs: { "aria-live": "polite" } });
    this.root.appendChild(this.bodyEl);
    this.setPending([], new Map());
  }

  /** Re-render the pending list. `previews` maps redaction id → captured preview. */
  setPending(redactions: Redaction[], previews: Map<string, string>): void {
    this.listEl.replaceChildren();
    if (redactions.length === 0) {
      this.listEl.appendChild(el("li", { cls: "pdf-redact-empty", text: "No redactions drawn yet." }));
      return;
    }
    for (const r of redactions) {
      const li = el("li", { cls: "pdf-redact-item", attrs: { "data-rid": r.id } });
      const meta = el("button", {
        cls: "pdf-edit-btn pdf-redact-jump",
        type: "button",
        title: `Go to page ${r.page}`,
        ariaLabel: `Redaction on page ${r.page}${r.reason ? ": " + r.reason : ""}`,
        on: { click: () => this.cb.onJumpTo(r.page) },
      });
      meta.appendChild(el("span", { cls: "pdf-redact-page", text: "p" + r.page }));
      const reasonText = r.reason || "redaction";
      const preview = previews.get(r.id);
      meta.appendChild(el("span", { cls: "pdf-redact-label", text: reasonText }));
      if (preview) meta.appendChild(el("span", { cls: "pdf-redact-preview", text: "“" + preview + "”" }));
      li.appendChild(meta);
      li.appendChild(el("button", {
        cls: "pdf-edit-btn pdf-redact-del",
        type: "button",
        title: "Delete this redaction",
        ariaLabel: "Delete redaction on page " + r.page,
        text: "✕",
        on: { click: () => this.cb.onDelete(r.id) },
      }));
      this.listEl.appendChild(li);
    }
  }

  /** Show the irreversible-consequence modal with the occurrences assist. */
  showConfirm(opts: {
    redactionCount: number;
    pages: number;
    dpi: number;
    occurrences: OtherOccurrence[];
    needle: string;
    onConfirm: () => void;
    onCancel: () => void;
  }): void {
    this.closeConfirm();
    const overlay = el("div", { cls: "pdf-modal-overlay", role: "dialog", ariaModal: true, ariaLabel: "Confirm redaction" });
    const panel = el("div", { cls: "pdf-modal-panel pdf-redact-confirm" });
    panel.appendChild(el("h2", { cls: "pdf-modal-title", text: "Apply redaction?" }));

    const warn = el("p", { cls: "pdf-redact-consequence" });
    warn.appendChild(el("strong", { text: `${opts.pages} page${opts.pages === 1 ? "" : "s"}` }));
    warn.appendChild(el("span", { text: ` will be permanently rasterized at ${opts.dpi} DPI and their text removed. This cannot be undone.` }));
    panel.appendChild(warn);

    panel.appendChild(el("p", { cls: "pdf-redact-meta", text: `${opts.redactionCount} redaction rectangle${opts.redactionCount === 1 ? "" : "s"} across ${opts.pages} page${opts.pages === 1 ? "" : "s"}.` }));

    // Occurrences assist — the likeliest real-world failure is redacting one
    // instance and missing three. Offer to add rects for every other occurrence.
    if (opts.occurrences.length > 0) {
      const occ = el("div", { cls: "pdf-redact-occ" });
      occ.appendChild(el("p", { cls: "pdf-redact-occ-title", text: `${opts.occurrences.length} other occurrence${opts.occurrences.length === 1 ? "" : "s"} of “${truncate(opts.needle, 40)}” not redacted:` }));
      const ul = el("ul", { cls: "pdf-redact-occ-list" });
      for (const o of opts.occurrences.slice(0, 8)) {
        ul.appendChild(el("li", { text: `p${o.page}: “${truncate(o.preview, 50)}”` }));
      }
      if (opts.occurrences.length > 8) ul.appendChild(el("li", { text: `…and ${opts.occurrences.length - 8} more` }));
      occ.appendChild(ul);
      const addBtn = el("button", {
        cls: "pdf-edit-btn pdf-redact-add-occ",
        type: "button",
        text: `Redact all ${opts.occurrences.length} occurrence${opts.occurrences.length === 1 ? "" : "s"}`,
        on: { click: () => { this.cb.onAddOccurrences(opts.needle, opts.occurrences); this.closeConfirm(); } },
      }) as HTMLButtonElement;
      occ.appendChild(addBtn);
      panel.appendChild(occ);
    }

    const actions = el("div", { cls: "pdf-modal-actions" });
    const cancelBtn = el("button", { cls: "pdf-edit-btn", type: "button", text: "Cancel", on: { click: () => { this.closeConfirm(); opts.onCancel(); } } }) as HTMLButtonElement;
    const confirmBtn = el("button", { cls: "pdf-edit-btn pdf-export-btn pdf-redact-confirm-btn", type: "button", text: "Rasterize & remove", on: { click: () => { this.closeConfirm(); opts.onConfirm(); } } }) as HTMLButtonElement;
    actions.appendChild(cancelBtn);
    actions.appendChild(confirmBtn);
    panel.appendChild(actions);

    overlay.appendChild(panel);
    overlay.addEventListener("click", (e) => { if (e.target === overlay) { this.closeConfirm(); opts.onCancel(); } });
    overlay.addEventListener("keydown", (e) => {
      if ((e as KeyboardEvent).key === "Escape") { this.closeConfirm(); opts.onCancel(); }
    });
    document.body.appendChild(overlay);
    this.modal = overlay;
    // Focus the confirm button so keyboard users act deliberately (this is the
    // destructive action — make it the default but require an explicit Enter).
    setTimeout(() => { try { confirmBtn.focus(); } catch { /* ignore */ } }, 0);
  }
  closeConfirm(): void {
    if (this.modal) { this.modal.remove(); this.modal = null; }
  }

  setProgress(done: number, total: number, page: number): void {
    this.bodyEl.replaceChildren();
    const pct = total > 0 ? Math.round((done / total) * 100) : 0;
    const bar = el("div", {
      cls: "pdf-progress",
      role: "progressbar",
      ariaLabel: "Rasterizing pages",
      attrs: { "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": String(pct) },
    });
    bar.appendChild(el("div", { cls: "pdf-progress-fill", style: { width: pct + "%" } }));
    this.bodyEl.appendChild(bar);
    this.bodyEl.appendChild(el("p", { cls: "pdf-redact-progress-text", text: `Rasterizing page ${page}… (${done}/${total})` }));
  }

  setResult(report: VerifyReport): void {
    this.bodyEl.replaceChildren();
    const verdict = el("p", { cls: "pdf-redact-verdict " + (report.ok ? "pdf-redact-ok" : "pdf-redact-fail") });
    verdict.appendChild(el("strong", { text: report.ok ? "Redaction verified ✓" : "Verification FAILED — no file emitted" }));
    this.bodyEl.appendChild(verdict);
    const list = el("ul", { cls: "pdf-redact-checks", role: "list" });
    for (const c of report.checks) {
      const li = el("li", { cls: "pdf-redact-check " + (c.ok ? "ok" : "fail") });
      li.appendChild(el("span", { cls: "pdf-redact-check-name", text: (c.ok ? "✓ " : "✗ ") + c.name }));
      li.appendChild(el("span", { cls: "pdf-redact-check-detail", text: c.detail.slice(0, 160) }));
      list.appendChild(li);
    }
    this.bodyEl.appendChild(list);
    if (report.warnings.length > 0) {
      const w = el("p", { cls: "pdf-redact-warn", text: "⚠ " + report.warnings.length + " warning(s): " + report.warnings.slice(0, 3).join("; ") });
      this.bodyEl.appendChild(w);
    }
    if (report.rasterizedPages.length > 0) {
      this.bodyEl.appendChild(el("p", { cls: "pdf-redact-rasterized", text: `Rasterized pages: ${report.rasterizedPages.join(", ")}` }));
    }
  }

  setError(msg: string): void {
    this.bodyEl.replaceChildren();
    this.bodyEl.appendChild(el("p", { cls: "pdf-redact-verdict pdf-redact-fail" })).appendChild(el("strong", { text: "Redaction failed" }));
    this.bodyEl.appendChild(el("p", { cls: "pdf-redact-error", text: msg.slice(0, 300) }));
  }

  /** Clear the body (called when arming starts / state resets). */
  clearBody(): void { this.bodyEl.replaceChildren(); }

  dispose(): void { this.closeConfirm(); this.root.remove(); }
}

function truncate(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n - 1) + "…";
}
