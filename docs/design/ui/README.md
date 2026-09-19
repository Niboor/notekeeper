# UI design

Visual design of the web app. Structure and behaviour are in [../07-web-app.md](../07-web-app.md); this
directory holds the look.

## Decision

**Paper** is the visual direction (decisions log entry 39): warm and calm, an off-white paper background,
notes set in a book serif, rounded cards with a soft shadow, terracotta accent, light and dark themes,
mobile as tabs. **Quiet** (cool, flat, hairline borders, teal accent) is kept in `proposals/` as a possible
alternative theme; Ink was dropped.

## The prototype

`proposals/` is one static HTML and JavaScript prototype with made-up data. It is used to judge the look
and to specify interactions before they are built. It really works:

- drag and drop with the mouse, including holding over a page tab to switch pages;
- keyboard drag (Space, arrows, Space, Esc) with the spoken announcements shown as a caption;
- **Move to** menu, dismiss with undo;
- adding notes into the Inbox or any column (bottom "Add a note" row, New note button, the `N` key, a floating
  button on phones), including turning `- [ ]` lines into a checklist;
- light and dark themes, responsive down to a phone.

```bash
python3 -m http.server 4173 --directory docs/design/ui/proposals
# open http://localhost:4173/?d=paper&t=light   (or d=quiet, t=dark)
```

All text and background colour pairs meet WCAG 2.1 AA contrast in every direction and theme (checked by script).

## Still to design

Trash, search results, note editing, settings, admin, login and activation, the share page. Their look
follows the Paper tokens; the prototype will grow to cover them when the web app reaches those screens.
