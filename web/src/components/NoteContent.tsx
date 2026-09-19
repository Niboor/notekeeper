// The one component that renders note text (SEC-CNT-1, SEC-CNT-2). Milestone M2 replaces the
// body with the sanitising Markdown renderer; until then text is shown as plain text, which
// React escapes, so nothing a note contains can become markup.
export function NoteContent({ text }: { text: string }) {
  return <p className="text">{text}</p>
}
