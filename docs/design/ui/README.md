# UI design

Visual design of the web app. Structure and behaviour are in [../07-web-app.md](../07-web-app.md); this
directory holds the look.

## Direction proposals (open)

`proposals/` contains one prototype with three visual directions, each in light and dark, responsive
from desktop to phone. It is static HTML and JavaScript with made-up data; it exists only to choose a
direction.

```bash
python3 -m http.server 4173 --directory docs/design/ui/proposals
# then open http://localhost:4173/?d=paper|quiet|ink&t=light|dark
```

| Direction | Character |
|---|---|
| `paper` | Warm and calm. Off-white paper, notes set in a book serif, rounded cards with a soft shadow, terracotta accent. |
| `quiet` | Cool, flat and efficient. System sans, hairline borders, no shadows, small-caps lane titles, teal accent. |
| `ink` | Editorial and typographic. Black on white, notes are ruled lines instead of boxes, monospace metadata, vermilion accent. |

All token pairs used for text meet WCAG 2.1 AA contrast in both themes (checked by script). Once a
direction is chosen, this directory will hold the tokens, the component inventory and the remaining
screens (Trash, search, settings, admin, login, share page).
