// Visual-direction prototype. Static data, no backend: it only exists to judge the look.
// ?d=paper | quiet | ink selects the direction; the sun/moon button switches light and dark.

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
  file: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/>',
  repeat: '<path d="M17 1l4 4-4 4"/><path d="M3 11V9a4 4 0 0 1 4-4h14M7 23l-4-4 4-4"/><path d="M21 13v2a4 4 0 0 1-4 4H3"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  chev: '<path d="M9 18l6-6-6-6"/>',
  check: '<path d="M20 6L9 17l-5-5"/>',
};
const icon = (n, cls = '') =>
  `<svg class="ic ${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[n]}</svg>`;

const DATA = {
  pages: ['Work', 'Chores', 'Travel'],
  lanes: [
    { id: 'inbox', name: 'Inbox', notes: [
      { text: 'buy milk, eggs', when: '2 min ago', fresh: true },
      { text: 'whiteboard after standup', when: '14 min ago', image: true },
      { text: 'call dentist about the crown\nask for Thursday afternoon', when: 'yesterday' },
      { text: 'https://example.org/blog/postgres-listen-notify-in-practice', when: 'Mon', link: true },
    ]},
    { id: 'week', name: 'This week', notes: [
      { text: 'Concert Friday\ndoors at 19:30, gate C', when: 'Tue', file: { name: 'tickets.pdf', size: '212 KB' }, reminder: 'Fri 09:00', shared: true },
      { text: 'Trip packing', when: 'Sun', checklist: [['passport', true], ['charger', true], ['rain jacket', false], ['sunscreen', false], ['book', false]], lifted: true },
      { text: 'Renew passport', when: 'Sat', reminder: 'Mon 09:00', repeat: true },
    ]},
    { id: 'waiting', name: 'Waiting', notes: [
      { text: 'Contractor quote\nfollow up if nothing by the 25th', when: 'last week' },
      { text: 'Order replacement filter for the boiler (model 42-B)', when: 'last week' },
    ]},
    { id: 'someday', name: 'Someday', notes: [
      { text: 'Learn to bake sourdough', when: 'Aug' },
      { text: 'Repaint the shed', when: 'Jul', image: true },
    ]},
  ],
};

const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

function renderText(text, link) {
  const [first, ...rest] = text.split('\n');
  if (link) return `<p class="text"><a href="#" tabindex="-1">${esc(first)}</a></p>`;
  if (!rest.length) return `<p class="text">${esc(first)}</p>`;
  return `<p class="text"><strong>${esc(first)}</strong><br>${rest.map(esc).join('<br>')}</p>`;
}

function renderNote(n) {
  const cls = ['note', n.fresh && 'is-new', n.lifted && 'is-lifted'].filter(Boolean).join(' ');
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
    `<span class="origin">${icon('chat')}<span>Matrix · ${n.when}</span></span>`,
    n.reminder ? `<span class="chip reminder">${icon('bell')}${n.reminder}${n.repeat ? icon('repeat') : ''}</span>` : '',
    n.shared ? `<span class="chip shared" title="Shared by link">${icon('link')}</span>` : '',
  ].join('');
  return `<article class="${cls}" tabindex="0">
    <div class="note-body">${body}</div>
    <footer class="note-meta">${meta}</footer>
    <div class="note-actions">
      <button class="icon-btn more" aria-label="Note actions" aria-haspopup="menu">${icon('more')}</button>
      <button class="icon-btn dismiss" aria-label="Dismiss note">${icon('x')}</button>
    </div>
  </article>`;
}

function renderLane(l) {
  const drop = l.id === 'week' ? '<div class="drop-slot" aria-hidden="true"></div>' : '';
  const notes = l.notes.map(renderNote);
  // the lifted card is followed by the drop slot to show where it will land
  const html = notes.map((h, i) => h + (l.notes[i].lifted ? drop : '')).join('');
  return `<section class="lane ${l.id === 'inbox' ? 'inbox' : ''}" data-lane="${l.id}" aria-label="${l.name}">
    <header class="lane-head"><h2>${l.id === 'inbox' ? icon('inbox') : ''}${l.name}</h2><span class="count">${l.notes.length}</span>
      ${l.id === 'inbox' ? '' : `<button class="icon-btn add" aria-label="Add note to ${l.name}">${icon('plus')}</button>`}</header>
    <div class="lane-notes">${html}</div>
  </section>`;
}

function render() {
  const params = new URLSearchParams(location.search);
  const dir = ['paper', 'quiet', 'ink'].includes(params.get('d')) ? params.get('d') : 'paper';
  document.documentElement.dataset.direction = dir;
  document.title = `Notekeeper · ${dir}`;
  const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  document.documentElement.dataset.theme = params.get('t') || (prefersDark ? 'dark' : 'light');

  const lanes = DATA.lanes;
  document.getElementById('root').innerHTML = `
  <div class="app" data-tab="week">
    <header class="topbar">
      <div class="brand"><span class="logo" aria-hidden="true"></span><span class="brand-name">Notekeeper</span></div>
      <nav class="pages" aria-label="Pages">
        ${DATA.pages.map((p, i) => `<a href="#" class="page ${i === 0 ? 'is-current' : ''}">${p}</a>`).join('')}
        <button class="icon-btn" aria-label="New page">${icon('plus')}</button>
      </nav>
      <label class="search">${icon('search')}<input type="search" placeholder="Search notes" aria-label="Search notes"><kbd>/</kbd></label>
      <div class="actions">
        <button class="icon-btn" aria-label="Trash">${icon('trash')}</button>
        <button class="icon-btn theme" aria-label="Toggle light and dark theme"></button>
        <span class="avatar" aria-label="Signed in as Robin">R</span>
      </div>
    </header>
    <nav class="tabs" aria-label="Lanes">
      ${lanes.map((l) => `<button class="tab ${l.id === 'week' ? 'is-current' : ''}" data-tab="${l.id}">${l.name}<span class="count">${l.notes.length}</span></button>`).join('')}
    </nav>
    <main class="layout">
      ${renderLane(lanes[0])}
      <div class="board">${lanes.slice(1).map(renderLane).join('')}</div>
    </main>
    <div class="toast" role="status"><span>Note dismissed</span><button class="undo">Undo</button></div>
    <div class="menu" role="menu" hidden>
      <button role="menuitem">Move to<span class="grow"></span>${icon('chev')}</button>
      <div class="menu-sub">${lanes.filter((l) => l.id !== 'inbox').map((l) => `<button role="menuitem">${l.name}</button>`).join('')}<button role="menuitem">Back to Inbox</button></div>
      <button role="menuitem">Set reminder</button>
      <button role="menuitem">Share by link</button>
      <hr>
      <button role="menuitem" class="danger">Dismiss</button>
    </div>
    <nav class="switcher" aria-label="Design direction">
      ${['paper', 'quiet', 'ink'].map((d) => `<a href="?d=${d}&t=${document.documentElement.dataset.theme}" class="${d === dir ? 'is-current' : ''}">${d}</a>`).join('')}
    </nav>
  </div>`;
  wire();
}

function setThemeButton() {
  const dark = document.documentElement.dataset.theme === 'dark';
  document.querySelector('.theme').innerHTML = icon(dark ? 'sun' : 'moon');
}

function wire() {
  const root = document.querySelector('.app');
  setThemeButton();
  root.querySelector('.theme').addEventListener('click', () => {
    document.documentElement.dataset.theme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    setThemeButton();
    root.querySelectorAll('.switcher a').forEach((a) => {
      const u = new URL(a.href); u.searchParams.set('t', document.documentElement.dataset.theme); a.href = u.toString();
    });
  });
  root.querySelectorAll('.tab').forEach((b) => b.addEventListener('click', () => {
    root.dataset.tab = b.dataset.tab;
    root.querySelectorAll('.tab').forEach((t) => t.classList.toggle('is-current', t === b));
  }));
  const toast = root.querySelector('.toast');
  root.querySelectorAll('.dismiss').forEach((b) => b.addEventListener('click', (e) => {
    const note = e.currentTarget.closest('.note');
    note.classList.add('is-gone');
    toast.classList.add('is-visible');
    toast.querySelector('.undo').onclick = () => { note.classList.remove('is-gone'); toast.classList.remove('is-visible'); };
    clearTimeout(toast._t); toast._t = setTimeout(() => toast.classList.remove('is-visible'), 8000);
  }));
  const menu = root.querySelector('.menu');
  root.querySelectorAll('.more').forEach((b) => b.addEventListener('click', (e) => {
    e.stopPropagation();
    const r = b.getBoundingClientRect();
    menu.hidden = false;
    menu.classList.toggle('open-sub', false);
    if (window.innerWidth > 820) {
      menu.style.left = `${Math.min(r.right - 200, window.innerWidth - 220)}px`;
      menu.style.top = `${r.bottom + 6}px`;
    } else { menu.style.left = menu.style.top = ''; }
  }));
  menu.querySelector('button').addEventListener('click', (e) => { e.stopPropagation(); menu.classList.toggle('open-sub'); });
  document.addEventListener('click', () => { menu.hidden = true; });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') menu.hidden = true; });
}

render();
