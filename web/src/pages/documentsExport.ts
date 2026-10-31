import type { BundleView, ExportReport } from "../protocol/api";

/**
 * The wording of the documents panel, kept out of the component so it can be
 * tested directly. The panel's only job is to be honest about a preview: a list
 * of paths is not an export, and a button press is not a success until the
 * report says so.
 */

/** documentsNote says what the list means: a preview, not a write. */
export function documentsNote(view: BundleView): string {
  if (!view.available) return view.problem ?? "Nothing to export yet: the plan has to be approved first.";
  const count = view.files.length;
  if (count === 0) return "Nothing would be written: the plan has no documents to generate.";
  return `${String(count)} file${count === 1 ? "" : "s"} would be written. Nothing is written until you press the button, and an existing file is never replaced.`;
}

/** canExport is true only when there is something to write. */
export function canExport(view: BundleView | null, busy: boolean): boolean {
  return view !== null && view.available && view.files.length > 0 && !busy;
}

/** exportLabel is the button's text for the current state. */
export function exportLabel(busy: boolean): string {
  return busy ? "Writing…" : "Write documents";
}

/**
 * exportResult says what happened, and never dresses a refusal as a success.
 * A collision names the file that is in the way, because the owner's next move
 * depends on which file it is.
 */
export function exportResult(report: ExportReport): { tone: "ok" | "warn"; message: string } {
  if (report.state === "exported") {
    const count = report.committed?.length ?? 0;
    return { tone: "ok", message: `Wrote ${String(count)} file${count === 1 ? "" : "s"} into the repository.` };
  }
  return { tone: "warn", message: report.problem ?? report.state };
}

