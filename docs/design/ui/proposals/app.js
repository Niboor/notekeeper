// Visual-direction prototype. Static data, no backend: it exists to judge the look and feel.
// ?d=paper | quiet selects the direction; the sun/moon button switches light and dark.
// Working interactions: drag and drop (mouse; hold over a page tab to switch page), keyboard
// drag (Space to lift, arrows to move, Space to drop, Esc to cancel), "Move to" menu, dismiss with
// undo, and adding notes to the Inbox or any column (bottom "Add a note" row, New note button, N).

const ICONS = {
  search: '<circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/>',
  x: '<path d="M18 6L6 18M6 6l12 12"/>',
  more: '<circle cx="5" cy="12" r="1.2"/><circle cx="12" cy="12" r="1.2"/><circle cx="19" cy="12" r="1.2"/>',
  clip: '<path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"/>',
  bell: '<path d="M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/>',
  link: '<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>',
  inbox: '<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5.45 5.11L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v6M14 11v6"/>',
  chat: '<path d="M21 11.5a8.4 8.4 0 0 1-9 8.4 8.5 8.5 0 0 1-3.8-.9L3 21l1.9-5.2A8.4 8.4 0 0 1 12 3a8.4 8.4 0 0 1 9 8.5z"/>',
  pen: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
  file: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/>',
  repeat: '<path d="M17 1l4 4-4 4"/><path d="M3 11V9a4 4 0 0 1 4-4h14M7 23l-4-4 4-4"/><path d="M21 13v2a4 4 0 0 1-4 4H3"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  chev: '<path d="M9 18l6-6-6-6"/>',
  check: '<path d="M20 6L9 17l-5-5"/>',
};
const icon = (n, cls = '') =>
  `<svg class="ic ${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[n]}</svg>`;

let nextId = 1;
const N = (o) => ({ id: `n${nextId++}`, ...o });
const state = {
  page: 'Work',
  inbox: [
    N({ text: 'buy milk, eggs', when: '2 min ago', fresh: true }),
    N({ text: 'whiteboard after standup', when: '14 min ago', image: true }),
    N({ text: 'call dentist about the crown\nask for Thursday afternoon', when: 'yesterday' }),
    N({ text: 'https://example.org/blog/postgres-listen-notify-in-practice', when: 'Mon', link: true }),
  ],
  pages: {
    Work: [
      { id: 'week', name: 'This week', notes: [
        N({ text: 'Concert Friday\ndoors at 19:30, gate C', when: 'Tue', file: { name: 'tickets.pdf', size: '212 KB' }, reminder: 'Fri 09:00', shared: true }),
        N({ text: 'Trip packing', when: 'Sun', checklist: [['passport', true], ['charger', true], ['rain jacket', false], ['sunscreen', false], ['book', false]] }),
        N({ text: 'Renew passport', when: 'Sat', reminder: 'Mon 09:00', repeat: true }),
      ]},
      { id: 'waiting', name: 'Waiting', notes: [
        N({ text: 'Contractor quote\nfollow up if nothing by the 25th', when: 'last week' }),
        N({ text: 'Order replacement filter for the boiler (model 42-B)', when: 'last week' }),
      ]},
      { id: 'someday', name: 'Someday', notes: [
        N({ text: 'Learn to bake sourdough', when: 'Aug' }),
        N({ text: 'Repaint the shed', when: 'Jul', image: true }),
      ]},
    ],
    Chores: [
      { id: 'today', name: 'Today', notes: [N({ text: 'Take out the recycling', when: 'Mon' }), N({ text: 'Water the plants', when: 'Mon', repeat: true, reminder: 'Daily 18:00' })] },
      { id: 'weekend', name: 'Weekend', notes: [N({ text: 'Clean the gutters', when: 'last week' })] },
      { id: 'later', name: 'Later', notes: [] },
    ],
    Travel: [
      { id: 'plan', name: 'Planning', notes: [N({ text: 'Lisbon in October\nlook at flights + a place near Alfama', when: 'Sep' })] },
      { id: 'booked', name: 'Booked', notes: [] },
    ],
  },
  composer: null, // lane id with the composer open
  drag: null,     // { id } while a mouse drag is in progress
  lift: null,     // { id, origin: {lane, index} } during a keyboard drag
  hiddenId: null, // dismissed note (for undo)
  announce: '',
  lastLane: 'inbox',
  tab: 'inbox',
};

const lanesOfPage = () => state.pages[state.page];
const allLanes = () => [{ id: 'inbox', name: 'Inbox', notes: state.inbox }, ...lanesOfPage()];
const laneById = (id) => allLanes().find((l) => l.id === id);
function locate(id) {
  for (const [pname, lanes] of Object.entries(state.pages)) for (const l of lanes) {
    const i = l.notes.findIndex((n) => n.id === id); if (i >= 0) return { lane: l, index: i, page: pname };
  }
  const i = state.inbox.findIndex((n) => n.id === id);
  return i >= 0 ? { lane: { id: 'inbox', name: 'Inbox', notes: state.inbox }, index: i, page: null } : null;
}
function moveNote(id, toLane, toIndex) {
  const at = locate(id); if (!at) return;
  const [note] = at.lane.notes.splice(at.index, 1);
  toLane.notes.splice(Math.max(0, Math.min(toIndex, toLane.notes.length)), 0, note);
  note.moved = true;
  return note;
}

const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

function renderText(text, link) {
  const [first, ...rest] = text.split('\n');
  if (link) return `<p class="text"><a href="#" tabindex="-1">${esc(first)}</a></p>`;
  if (!rest.length) return `<p class="text">${esc(first)}</p>`;
  return `<p class="text"><strong>${esc(first)}</strong><br>${rest.map(esc).join('<br>')}</p>`;
}

function renderNote(n) {
  const cls = ['note', (n.fresh || n.moved) && 'is-new', state.lift?.id === n.id && 'is-lifted', state.drag?.id === n.id && 'is-dragging-source', state.hiddenId === n.id && 'is-gone'].filter(Boolean).join(' ');
  const done = n.checklist ? n.checklist.filter((c) => c[1]).length : 0;
  const body = [
    n.checklist
      ? `<div class="check-head"><strong>${esc(n.text)}</strong><span class="progress" aria-label="${done} of ${n.checklist.length} done">${done}/${n.checklist.length}</span></div>
         <ul class="checklist">${n.checklist.map(([t, d]) => `<li class="${d ? 'done' : ''}"><span class="box">${d ? icon('check') : ''}</span><span>${esc(t)}</span></li>`).join('')}</ul>`
      : renderText(n.text, n.link),
    n.image ? '<div class="thumb" role="img" aria-label="Photo attachment"></div>' : '',
    n.file ? `<div class="file">${icon('file')}<span class="fname">${esc(n.file.name)}</span><span class="fsize">${n.file.size}</span></div>` : '',
  ].join('');
  const meta = [
    n.app ? `<span class="origin">${icon('pen')}<span>App · ${n.when}</span></span>` : `<span class="origin">${icon('chat')}<span>Matrix · ${n.when}</span></span>`,
    n.reminder ? `<span class="chip reminder">${icon('bell')}${n.reminder}${n.repeat ? icon('repeat') : ''}</span>` : '',
    n.shared ? `<span class="chip shared" title="Shared by link">${icon('link')}</span>` : '',
  ].join('');
  n.fresh = false; n.moved = false;
  return `<article class="${cls}" tabindex="0" data-id="${n.id}" aria-roledescription="draggable note" aria-label="${esc(n.text.split('\n')[0])}">
    <div class="note-body">${body}</div>
    <footer class="note-meta">${meta}</footer>
    <div class="note-actions">
      <button class="icon-btn more" aria-label="Note actions" aria-haspopup="menu">${icon('more')}</button>
      <button class="icon-btn dismiss" aria-label="Dismiss note">${icon('x')}</button>
    </div>
  </article>`;
}

const composerHTML = (lane) => `<form class="composer" data-lane="${lane.id}">
  <textarea rows="3" placeholder="Write a note…  (- [ ] for a checklist)" aria-label="New note in ${esc(lane.name)}" autofocus></textarea>
  <div class="composer-bar">
    <button type="button" class="icon-btn" aria-label="Attach a file" title="Attach a file">${icon('clip')}</button>
    <button type="button" class="icon-btn" aria-label="Add a reminder" title="Add a reminder">${icon('bell')}</button>
    <span class="grow"></span><span class="hint">Ctrl+Enter</span>
    <button type="button" class="btn cancel">Cancel</button><button type="submit" class="btn primary">Add note</button>
  </div></form>`;

function renderLane(l) {
  const inbox = l.id === 'inbox';
  const showDrag = state.drag != null;
  return `<section class="lane ${inbox ? 'inbox' : ''}" data-lane="${l.id}" aria-label="${l.name}">
    <header class="lane-head"><h2>${inbox ? icon('inbox') : ''}${l.name}</h2><span class="count">${l.notes.length}</span></header>
    <div class="lane-notes">
      ${state.composer === l.id ? composerHTML(l) : ''}
      ${l.notes.map(renderNote).join('')}
      ${l.notes.length === 0 && state.composer !== l.id ? `<div class="empty">${showDrag ? 'Drop here' : 'Nothing here yet'}</div>` : ''}
      ${state.composer === l.id ? '' : `<button class="add-row" data-add="${l.id}">${icon('plus')}<span>Add a note</span></button>`}
    </div>
  </section>`;
}

function render() {
  const params = new URLSearchParams(location.search);
  const dir = ['paper', 'quiet'].includes(params.get('d')) ? params.get('d') : 'paper';
  document.documentElement.dataset.direction = dir;
  document.title = `Notekeeper · ${dir}`;
  if (!document.documentElement.dataset.theme) {
    const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    document.documentElement.dataset.theme = params.get('t') || (prefersDark ? 'dark' : 'light');
  }
  const lanes = allLanes();
  if (!lanes.find((l) => l.id === state.tab)) state.tab = lanes[1] ? lanes[1].id : 'inbox';
  const focusId = document.activeElement?.dataset?.id;
  document.getElementById('root').innerHTML = `
  <div class="app" data-tab="${state.tab}">
    <header class="topbar">
      <div class="brand"><span class="logo" aria-hidden="true"></span><span class="brand-name">Notekeeper</span></div>
      <nav class="pages" aria-label="Pages">
        ${Object.keys(state.pages).map((p) => `<a href="#" data-page="${p}" class="page ${p === state.page ? 'is-current' : ''}">${p}</a>`).join('')}
        <button class="icon-btn" aria-label="New page">${icon('plus')}</button>
      </nav>
      <label class="search">${icon('search')}<input type="search" placeholder="Search notes" aria-label="Search notes"><kbd>/</kbd></label>
      <div class="actions">
        <button class="btn primary new-note">${icon('plus')}<span>New note</span><kbd>N</kbd></button>
        <button class="icon-btn" aria-label="Trash">${icon('trash')}</button>
        <button class="icon-btn theme" aria-label="Toggle light and dark theme"></button>
        <span class="avatar" aria-label="Signed in as Robin">R</span>
      </div>
    </header>
    <nav class="tabs" aria-label="Lanes">
      ${lanes.map((l) => `<button class="tab ${l.id === state.tab ? 'is-current' : ''}" data-tab="${l.id}">${l.name}<span class="count">${l.notes.length}</span></button>`).join('')}
    </nav>
    <main class="layout">
      ${renderLane(lanes[0])}
      <div class="board">${lanes.slice(1).map(renderLane).join('')}</div>
    </main>
    <button class="fab" aria-label="New note">${icon('plus')}</button>
    <div class="toast" role="status"><span>Note dismissed</span><button class="undo">Undo</button></div>
    <div class="menu" role="menu" hidden></div>
    <div class="announcer" role="status" aria-live="polite">${state.announce ? `<span class="tag">screen reader</span> ${esc(state.announce)}` : ''}</div>
    <nav class="switcher" aria-label="Design direction">
      ${['paper', 'quiet'].map((d) => `<a href="?d=${d}&t=${document.documentElement.dataset.theme}" class="${d === dir ? 'is-current' : ''}">${d}</a>`).join('')}
    </nav>
  </div>`;
  wire();
  if (focusId) document.querySelector(`[data-id="${focusId}"]`)?.focus();
  const ta = document.querySelector('.composer textarea'); if (ta && !document.activeElement?.closest('.composer')) ta.focus();
}

function announce(msg) { state.announce = msg; const a = document.querySelector('.announcer'); if (a) a.innerHTML = `<span class="tag">screen reader</span> ${esc(msg)}`; }

// ---- wiring ---------------------------------------------------------------------------------
function wire() {
  const root = document.querySelector('.app');
  const setTheme = () => { root.querySelector('.theme').innerHTML = icon(document.documentElement.dataset.theme === 'dark' ? 'sun' : 'moon'); };
  setTheme();
  root.querySelector('.theme').onclick = () => {
    document.documentElement.dataset.theme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'; setTheme();
    root.querySelectorAll('.switcher a').forEach((a) => { const u = new URL(a.href); u.searchParams.set('t', document.documentElement.dataset.theme); a.href = u.toString(); });
  };
  root.querySelectorAll('.tab').forEach((b) => b.onclick = () => { state.tab = b.dataset.tab; render(); });
  root.querySelectorAll('.page').forEach((p) => p.onclick = (e) => { e.preventDefault(); if (!state.drag) { state.page = p.dataset.page; state.tab = 'inbox'; render(); } });

  // dismiss + undo
  const toast = root.querySelector('.toast');
  root.querySelectorAll('.dismiss').forEach((b) => b.onclick = (e) => {
    const note = e.currentTarget.closest('.note'); state.hiddenId = note.dataset.id; note.classList.add('is-gone');
    toast.classList.add('is-visible');
    toast.querySelector('.undo').onclick = () => { state.hiddenId = null; note.classList.remove('is-gone'); toast.classList.remove('is-visible'); };
    clearTimeout(toast._t); toast._t = setTimeout(() => toast.classList.remove('is-visible'), 8000);
  });

  // "more" menu with a working "Move to"
  const menu = root.querySelector('.menu');
  root.querySelectorAll('.more').forEach((b) => b.onclick = (e) => {
    e.stopPropagation();
    const id = b.closest('.note').dataset.id;
    const at = locate(id);
    const targets = allLanes().filter((l) => l.id !== at.lane.id);
    menu.innerHTML = `<button role="menuitem" class="has-sub">Move to<span class="grow"></span>${icon('chev')}</button>
      <div class="menu-sub">${targets.map((l) => `<button role="menuitem" data-move="${l.id}">${l.name}</button>`).join('')}</div>
      <button role="menuitem">Set reminder</button><button role="menuitem">Share by link</button><hr><button role="menuitem" class="danger" data-dismiss="${id}">Dismiss</button>`;
    menu.hidden = false; menu.classList.remove('open-sub');
    const r = b.getBoundingClientRect();
    if (window.innerWidth > 820) { menu.style.left = `${Math.min(r.right - 200, window.innerWidth - 220)}px`; menu.style.top = `${r.bottom + 6}px`; } else { menu.style.left = menu.style.top = ''; }
    menu.querySelector('.has-sub').onclick = (ev) => { ev.stopPropagation(); menu.classList.toggle('open-sub'); };
    menu.querySelectorAll('[data-move]').forEach((m) => m.onclick = () => {
      const lane = laneById(m.dataset.move); moveNote(id, lane, 0); menu.hidden = true;
      announce(`Moved to ${lane.name}, position 1 of ${lane.notes.length}`); render();
    });
    menu.querySelector('[data-dismiss]').onclick = () => root.querySelector(`[data-id="${id}"] .dismiss`).click();
  });
  document.onclick = () => { menu.hidden = true; };

  // composer: bottom "Add a note" row, New note button, FAB, N key
  const openComposer = (laneId) => { state.composer = laneId; state.lastLane = laneId; render(); };
  root.querySelectorAll('.add-row').forEach((b) => b.onclick = () => openComposer(b.dataset.add));
  root.querySelector('.new-note').onclick = () => openComposer(state.lastLane === 'inbox' || !laneById(state.lastLane) ? 'inbox' : state.lastLane);
  root.querySelector('.fab').onclick = () => openComposer(state.tab);
  const form = root.querySelector('.composer');
  if (form) {
    const lane = laneById(form.dataset.lane);
    const submit = () => {
      const text = form.querySelector('textarea').value.trim();
      if (text) { lane.notes.unshift(buildNote(text)); announce(`Added a note to ${lane.name}`); }
      state.composer = null; render();
    };
    form.onsubmit = (e) => { e.preventDefault(); submit(); };
    form.querySelector('.cancel').onclick = () => { state.composer = null; render(); };
    form.querySelector('textarea').onkeydown = (e) => {
      if (e.key === 'Escape') { state.composer = null; render(); }
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); submit(); }
    };
  }
  wireDrag(root);
  wireKeyboard(root);
}

function buildNote(text) {
  const lines = text.split('\n'); const items = lines.slice(1).map((l) => l.match(/^\s*[-*]\s*\[( |x)\]\s*(.*)$/i));
  if (lines.length > 1 && items.every(Boolean)) {
    return N({ text: lines[0], when: 'just now', app: true, fresh: true, checklist: items.map((m) => [m[2], m[1].toLowerCase() === 'x']) });
  }
  return N({ text, when: 'just now', app: true, fresh: true });
}

// ---- mouse drag and drop -----------------------------------------------------------------------
function wireDrag(root) {
  root.querySelectorAll('.note').forEach((el) => el.addEventListener('pointerdown', (e) => {
    if (e.pointerType !== 'mouse' || e.button !== 0 || e.target.closest('button, a, textarea, input')) return;
    const startX = e.clientX, startY = e.clientY, id = el.dataset.id;
    let ghost = null, slot = null, hoverPage = null, hoverTimer = null, offX = 0, offY = 0;
    const move = (ev) => {
      if (!ghost) {
        if (Math.hypot(ev.clientX - startX, ev.clientY - startY) < 5) return;
        const r = el.getBoundingClientRect(); offX = startX - r.left; offY = startY - r.top;
        ghost = el.cloneNode(true); ghost.classList.add('drag-ghost', 'is-lifted'); ghost.removeAttribute('tabindex');
        ghost.style.width = `${r.width}px`; document.body.appendChild(ghost); document.body.classList.add('is-dragging');
        state.drag = { id }; el.classList.add('is-dragging-source');
        slot = document.createElement('div'); slot.className = 'drop-slot'; announce('Picked up note. Drag to a column and release to drop.');
      }
      ghost.style.left = `${ev.clientX - offX}px`; ghost.style.top = `${ev.clientY - offY}px`;
      ghost.style.pointerEvents = 'none';
      const under = document.elementFromPoint(ev.clientX, ev.clientY);
      // page tabs: hold to switch page
      const tab = under?.closest('.page');
      if (tab !== hoverPage) {
        hoverPage?.classList.remove('is-drop-target'); clearTimeout(hoverTimer); hoverPage = tab;
        if (tab && tab.dataset.page !== state.page) {
          tab.classList.add('is-drop-target');
          hoverTimer = setTimeout(() => { state.page = tab.dataset.page; state.tab = 'inbox'; slot.remove(); render(); announce(`Now viewing ${state.page}. Keep dragging to a column.`); hoverPage = null; }, 650);
        }
      }
      // lanes: place the drop slot
      root.querySelectorAll('.lane').forEach((l) => l.classList.remove('is-drop-target'));
      const laneEl = under?.closest('.lane');
      if (laneEl) {
        laneEl.classList.add('is-drop-target');
        const container = laneEl.querySelector('.lane-notes');
        const cards = [...container.querySelectorAll('.note:not(.is-dragging-source):not(.is-gone)')];
        const before = cards.find((c) => { const b = c.getBoundingClientRect(); return ev.clientY < b.top + b.height / 2; });
        const anchor = before || container.querySelector('.add-row, .empty');
        if (anchor) container.insertBefore(slot, anchor); else container.appendChild(slot);
        container.querySelector('.empty')?.classList.add('is-hidden');
      } else slot.remove();
    };
    const up = (ev) => {
      document.removeEventListener('pointermove', move); document.removeEventListener('pointerup', up); document.removeEventListener('keydown', esc);
      clearTimeout(hoverTimer);
      if (!ghost) return;
      const laneEl = slot.closest('.lane');
      ghost.remove(); document.body.classList.remove('is-dragging'); state.drag = null;
      if (laneEl) {
        const lane = laneById(laneEl.dataset.lane);
        const cards = [...laneEl.querySelectorAll('.note:not(.is-dragging-source), .drop-slot')];
        const index = cards.indexOf(slot);
        moveNote(id, lane, index);
        announce(`Moved to ${lane.name}, position ${index + 1} of ${lane.notes.length}`);
      } else announce('Drop cancelled, note returned to its column.');
      render();
    };
    const esc = (ev) => { if (ev.key === 'Escape') { slot && slot.remove(); up(ev); } };
    document.addEventListener('pointermove', move); document.addEventListener('pointerup', up); document.addEventListener('keydown', esc);
  }));
}

// ---- keyboard drag: Space lifts, arrows move, Space drops, Esc cancels ---------------------------
function wireKeyboard(root) {
  root.querySelectorAll('.note').forEach((el) => el.addEventListener('keydown', (e) => {
    if (e.target !== el) return;
    const id = el.dataset.id;
    if (!state.lift && e.key === ' ') {
      e.preventDefault(); const at = locate(id);
      state.lift = { id, origin: { lane: at.lane.id, index: at.index } };
      announce(`Picked up ${el.getAttribute('aria-label')}. Use arrow keys to move, space to drop, escape to cancel.`); render(); return;
    }
    if (!state.lift || state.lift.id !== id) return;
    const at = locate(id); const lanes = allLanes(); const li = lanes.findIndex((l) => l.id === at.lane.id);
    const go = (lane, index) => { moveNote(id, lane, index); announce(`${lane.name}, position ${Math.min(index, lane.notes.length - 1) + 1} of ${lane.notes.length}`); render(); };
    if (e.key === 'ArrowRight' && lanes[li + 1]) { e.preventDefault(); go(lanes[li + 1], Math.min(at.index, lanes[li + 1].notes.length)); }
    else if (e.key === 'ArrowLeft' && lanes[li - 1]) { e.preventDefault(); go(lanes[li - 1], Math.min(at.index, lanes[li - 1].notes.length)); }
    else if (e.key === 'ArrowUp' && at.index > 0) { e.preventDefault(); go(at.lane, at.index - 1); }
    else if (e.key === 'ArrowDown' && at.index < at.lane.notes.length - 1) { e.preventDefault(); go(at.lane, at.index + 1); }
    else if (e.key === ' ') { e.preventDefault(); const l = locate(id); state.lift = null; announce(`Dropped in ${l.lane.name}, position ${l.index + 1} of ${l.lane.notes.length}`); render(); }
    else if (e.key === 'Escape') { e.preventDefault(); const o = state.lift.origin; moveNote(id, laneById(o.lane) || allLanes()[0], o.index); state.lift = null; announce('Move cancelled, note returned to where it was.'); render(); }
  }));
}
document.addEventListener('keydown', (e) => {
  if (e.target.matches('input, textarea')) return;
  if (e.key === 'n' || e.key === 'N') { e.preventDefault(); state.composer = state.lastLane || 'inbox'; render(); }
  if (e.key === '/') { e.preventDefault(); document.querySelector('.search input')?.focus(); }
  if (e.key === 'Escape') document.querySelector('.menu')?.setAttribute('hidden', '');
});

render();
