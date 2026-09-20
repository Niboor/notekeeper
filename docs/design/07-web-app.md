# 7. Web app

React 19 + TypeScript + Vite, built to static files and served by nginx (tech-stack §5). It contains the application and the **share page**; both are routes of one build. Covers WEB-*, CORE-SH10, CORE-N17, WEB-N*.

## 1. Structure and routes

```
web/src/
  api/          generated client (openapi-typescript / openapi-fetch) + fetch wrapper with auth renewal
  auth/         session state, login, activation, cross-tab refresh coordination
  realtime/     SSE connection, change → query cache updates
  features/     board, inbox, note, search, trash, reminders, settings, admin, share (creation UI)
  share/        the public share route (separate entry chunk, no auth code)
  components/   note content renderer, dialogs, drag and drop wrappers
  i18n/         message catalogue (English only in v1)
```

| Route | View | Notes |
|---|---|---|
| `/login`, `/activate#<token>` | Sign in; set a password from an activation link | Activation token in the URL **fragment** (never sent to a server) |
| `/`, `/p/:pageId` | Board of a page with the Inbox tray | WEB-1, WEB-2 |
| `/trash` | Trash | WEB-7 |
| `/search?q=` | Global search results | WEB-14 |
| `/settings/*` | Account, sessions, chat links, notices, share links | WEB-12, AUTH-U10, CORE-SH14 |
| `/admin/*` | Users, bot instances | WEB-19 (route hidden and API-guarded unless admin) |
| `/s#<token>` | **Share page** (share hostname only) | CORE-SH10 |

## 2. Layout

- **Desktop:** a page switcher, the selected page's categories as horizontal columns, and the **Inbox as a persistent side tray** with a live count; notes are dragged from the tray into columns (WEB-2, WEB-3). A global search field and shortcut (`/` or `Ctrl+K`) are always available (WEB-14, WEB-16).
- **Phone:** one column at a time (swipe or tabs), the Inbox as a tab; moving uses the "Move to…" menu rather than drag (WEB-5, WEB-N1). The exact visual design is deliberately deferred (WEB-N7); this structure is what the data model and API assume.
- **Note card:** rendered content (§5), attachment previews (inline images, file chips), creation time, origin ("via Matrix"), a checklist progress badge (2/5), reminder indicator, share indicator, Dismiss button (WEB-6, WEB-8).

## 3. Authentication in the browser

- The wrapper around `fetch` sends cookies (same origin) plus the `X-Notekeeper-Client: web` header on unsafe methods ([03](03-auth.md) §2.4).
- **Silent renewal (AUTH-C10):** the wrapper refreshes at about 80% of the access token lifetime and on any `401`, then retries the request once. Refreshes are **single-flight** within a tab and serialised across tabs with `navigator.locks.request('nk-refresh', …)`; the server's 60-second grace window is the backstop for races (SEC-AUTH-7). Unsaved drafts live in component state and are never lost by a renewal.
- When the session is really gone (`401` from refresh), the app keeps any draft in `sessionStorage`, shows the login page, and restores the draft afterwards.
- No tokens are ever readable by JavaScript (HttpOnly cookies); no token is stored in web storage.

## 4. Data flow, realtime and optimistic updates

- **TanStack Query** holds server state, keyed by resource (`['board', pageId]`, `['inbox']`, `['note', id]`, `['trash']`, …).
- **SSE:** a single `EventSource('/api/v1/events')` per tab. On `change` events the handler patches or invalidates the matching keys (a note upsert updates the card in place; a page or category change invalidates the board); on `resync` it invalidates everything; the browser's automatic reconnect sends `Last-Event-ID` so no change is missed (WEB-11).
- **Optimistic updates (WEB-3):** every mutation that changes what is on screen (move, dismiss, create, edit) applies its change to the cached views at once, and again after in-flight refetches were cancelled (cancelling reverts a query to its earlier state, which would undo the change), then calls the API; on failure the cache rolls back to the captured snapshot and a toast explains. Responses and SSE echoes reconcile. A ticked checklist box is additionally remembered locally until the text changes, because the cache notifies React one tick late and a controlled checkbox would otherwise flash back.
- **Creating a note (WEB-9):** every lane ends with a quiet "Add a note" row, and the top bar has a **New note** button and the `N` key. Each opens an inline composer at the top of that lane: a text area (Markdown, `- [ ]` makes a checklist), attach and reminder buttons, `Ctrl+Enter` to add, `Esc` to cancel. On a phone a floating **+** opens the composer in the visible lane. The note is created with a client-generated id, appears immediately (optimistic) and is marked as created in the app ("App · just now").
- **Dismiss and undo (WEB-6, CORE-N8):** dismissing removes the card optimistically and shows a toast with Undo for 10 seconds; Undo calls `:restore`. The same restore is available later in the Trash.
- **Editing a note while it changes (WEB-11):** the editor keeps the draft locally. If a change for that note arrives while there are unsaved edits, a banner ("This note changed elsewhere", *View incoming*) appears and the draft is never overwritten. Saving is a normal later edit (latest wins); the response's `stale` flag and the version history keep the other text recoverable (EDT-5).
- **Reminders and notifications:** in-app notifications arrive through the same stream; the indicator and the upcoming list read from `['reminders']` and `['notifications']`.

## 5. Note content: one renderer

A single React component renders note text in the application and on the share page (SEC-CNT-1, SEC-CNT-2):

- Parsing with a CommonMark parser with **raw HTML disabled**, then a sanitising pass on the resulting tree; link URLs restricted to `http`, `https`, `mailto`, `tel`; external links `rel="noopener noreferrer"`; auto-linking of bare URLs.
- **Checklists (CORE-N17, WEB-20):** task-list items render as checkboxes. The parser keeps source offsets, so toggling a box rewrites exactly that `[ ]` or `[x]` in the part's text and sends `PATCH /notes/{id}/parts/{partId}` with `base_version`; the optimistic update is immediate, and rapid toggles coalesce server-side into one history entry. On the share page checkboxes render read-only.
- Nothing is rendered with `dangerouslySetInnerHTML`; a lint rule forbids it.

## 6. Drag and drop and accessibility (WEB-3, WEB-5, WEB-N4)

`dnd-kit` (stable packages) handles the **mouse** (`MouseSensor`, 5 px activation distance, `DragOverlay`, hold-over-page-tab). The **keyboard** interaction is our own, on top of the same pure arrangement functions (`features/board/dnd.ts`, unit-tested): Space on a focused note lifts it, the arrows move it within and between columns (also into empty ones), Space drops it, Escape cancels, and every step is announced through one live region ("Moved to Done, position 1 of 3"). dnd-kit's keyboard sensor was tried first and dropped: its coordinate-based lane jumping is unreliable across empty columns and scroll containers, while a plain list-of-ids model is deterministic and testable. There is no touch sensor (dragging competes with scrolling; phones use the Move-to menu). Every drag has a menu equivalent ("Move to page → category", "Move to Inbox"). Radix primitives supply focus management for dialogs and menus. Colours meet WCAG 2.1 AA, and `prefers-reduced-motion` and dark mode are respected. Playwright runs axe-core checks on the main views.

## 7. The share page (CORE-SH4, CORE-SH10, SEC-SHR-*)

The share route is a **separate build entry** loaded lazily on the share hostname, so it contains only the renderer, the public API client and the page; it has no auth or admin code.

1. Read the token from `location.hash`. The fragment **stays in the address bar**: it is never sent to any server or in a `Referer`, and keeping it means reloading or bookmarking the page keeps working.
2. `GET /api/public/v1/share` with `X-Share-Token`. On `404` show one generic "This link is no longer available" (the same for unknown, expired, revoked, or note dismissed).
3. Render the note: text through the shared renderer (read-only), attachments fetched with the header (`GET /api/public/v1/share/attachments/{id}`) into **blob URLs**: images inline, other files as download links, audio and video through native elements. No `Range`, so no seeking in large media (design decision D2; acceptable at the 25 MiB cap).
4. Show the notice that this content was shared by a Notekeeper user and is not verified (SEC-SHR-10). Nothing about the owner, page, category, source or reminders is present in the data at all (CORE-SH4).

Isolation is by hostname, not by code: the share host serves the same static files but no application routes work there, it never sets or reads cookies, its Content-Security-Policy is `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob:; media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'`, and the ingress routes only `/api/public/v1` to Core ([08](08-deployment-and-testing.md) §1).

## 8. PWA and offline

`vite-plugin-pwa` (Workbox) precaches the app shell for instant loads and installability (WEB-N8). **API responses and attachments are never cached by the service worker** (privacy, SEC-DATA-6); offline the app shows a banner and read-only cached query data from memory only. Full offline editing is out of scope (§2.2 of the requirements).

## 9. Localisation and performance

- All user-visible strings go through a message catalogue (`t('board.moveTo')`); only English exists in v1 (NFR-Q3).
- Performance budget (WEB-N3): the board endpoint returns categories with their first notes in one request; long categories load further pages on scroll; images use `loading="lazy"`; the main bundle excludes the share and admin chunks; optimistic updates keep interactions under 100 ms.

## 10. Testing

Vitest and Testing Library for units (renderer XSS corpus, checklist toggling offsets, renewal single-flight), Playwright end-to-end against the compose stack: sign in, drag between columns by mouse and by keyboard, dismiss and undo, live arrival of a note sent through the bot, share link creation and viewing on the share hostname, session survival across restart of Core, axe accessibility checks ([08](08-deployment-and-testing.md) §5).

## 11. Drag and drop specification

Designed and tried in the `docs/design/ui/proposals` prototype (see [ui/README.md](ui/README.md)); implemented with `dnd-kit` (§6).

| Aspect | Behaviour |
|---|---|
| Mouse | Press and move about 5 px to lift a note. The note follows the pointer, slightly enlarged with a deeper shadow; the original position dims. A dashed drop slot in the accent colour shows where it will land, inside any lane including an empty one (which says "Drop here"). Release to drop; `Esc` cancels. |
| Other pages | Dragging over a page tab highlights it ("hold to open"); after about 0.65 s the board switches to that page so the note can be dropped into one of its columns. |
| Edges | Near the edge of the board or a lane the view scrolls automatically. |
| Keyboard | Focus a note, `Space` lifts it, arrow keys move it between lanes (left, right) and within a lane (up, down), `Space` drops, `Esc` returns it. Every step is announced in a live region, for example "Moved to This week, position 3 of 4" (shown as a caption in the prototype, visually hidden in the app). |
| Touch and phones | No drag on touch (it competes with scrolling). Each note's `⋯` menu has **Move to** with the lanes and pages; the lanes are tabs, one at a time. |
| After a drop | The note keeps its place with a brief highlight; the change is optimistic and reconciled by the server ([README](README.md) §3, `POST /notes/{id}/move`). |
