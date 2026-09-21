# Notekeeper for people who use it

Notekeeper turns what you send to a chat bot into notes on your own boards. You write in your chat app as you
always do; the notes appear in the web app, where you sort them.

## Read this first: who can see your notes

Your chat is end-to-end encrypted, and **the notes made from it are not**. The bot is a member of the chat, so it
receives your messages, decrypts them, and passes them to the Notekeeper server, which stores them so you can
read, search and organise them. That means:

* **The person who runs this Notekeeper server can read your notes**, your files and their history, through
  the database and its backups. The administrator's *screens* show only accounts and storage, never notes, but
  the person with access to the machines is a person you must trust the way you trust a mail server's owner.
* Only what you send **to the bot** is stored. The rest of your chats is untouched.
* Anyone you give a **share link** to can read that one note until the link expires or you end it.
* Your notes are not shared with other accounts on the server. Accounts are isolated from each other.
* You can take everything out (*Settings → Export my data*) and delete your account with everything in it
  (*Settings → Delete my account*). Backups the operator made earlier keep the data until they expire.

If that is not acceptable for something, do not send it to the bot.

## Getting started

1. The administrator creates your account and gives you an **activation link**. Open it and choose a password
   of at least the length shown.
2. Under *Settings → Chats* choose **Create a code**. In your chat app, start a direct message with the
   Notekeeper bot (Settings shows its address, for example `@notekeeper:example.org`; only direct messages between you and the bot are used) and send `!link ABCD-1234` with your code.
3. Send something. It appears in your **Inbox** within seconds, with a check mark on your message in the chat.

## Notes from chat

* Every message becomes a note. **Text** is kept as you typed it, including Markdown (`**bold**`, links,
  `- [ ]` task lists).
* **Photos and files**: several sent in a burst, or a caption sent right after a photo, become one note. Two
  text messages are never merged, so a quick list of separate todos stays separate. A **reply** to a message that
  is already a note adds to that note, however long ago. You can change how long a note stays open for a photo
  or caption under *Settings*.
* **Edit** a message in your chat and the note changes with it; **delete** it and the part disappears (if it was
  the whole note, the note goes to the Trash so nothing is lost by accident). Earlier texts stay in the note's *History*.
* Voice messages, images, files and videos are kept as files. A shared location becomes text with a map link. Things Notekeeper cannot show (stickers, polls) become a short placeholder. Files that could not be
  saved (too big, storage full) are marked on the note and the bot tells you why. The text is always kept.
* Only messages sent after you linked the chat are used.

## Organising

* **Pages** are boards, each with **columns** (categories). The **Inbox** is where new notes land; drag a note
  into a column, or use the **⋯** menu → *Move to*, which also works on a phone. With the keyboard: focus a note,
  press **Space**, move with the arrow keys, **Space** to drop, **Escape** to cancel.
* Drag a page's tab to put the page elsewhere among the tabs, or use *Move left* and *Move right* in the page's **⋯**
  menu.
* Drag a column by its title to put it elsewhere on the page, or onto another page's tab to move it there (hold it
  over the tab to open that page and drop it between its columns). The column's **⋯** menu does the same with
  *Move left*, *Move right* and *Move to page*.
* **✕** dismisses a note into the **Trash** with an *Undo*; from the Trash you can restore it or delete it for good.
* Tick checklist items right on the card. Edit a note from its menu. **Merge** two notes, or **split** a part
  out, to repair how the bot grouped things.
* **Search** (press **/**) looks through everything, including the Trash if you ask, and finds whole words first and, when there are none, parts of words (from three characters).

## Reminders

Set a reminder from a note's menu (*Remind me…*): pick a quick time, or choose a date and time yourself (with shortcuts for today, tomorrow, next week and the usual hours, and a line that says when it will arrive), once or repeating. It
arrives in the chat you chose in *Settings → Chats* (with the note text, its files and a link) and in the bell.
From the chat, in English:

* reply to a message with `!remind tomorrow 9am` (or `in 2 hours`, `friday 18:30`, `12 dec 8:00`);
* `!remind in 2 hours call the dentist` makes a new note and a reminder;
* reply to a reminder with `!snooze 30m` (or `!snooze tomorrow`), or `!done`.

Times are understood in your time zone (*Settings*). If Notekeeper cannot understand a time, it says so and nothing is created.

## Sharing

*Share…* on a note makes a link that anyone can open without an account, until it expires (an hour to thirty
days). They see the note as it is now, including later edits, and nothing else: not your other notes, page,
column, chat or history. You can list and end links under *Settings → Share links*; ending is immediate, and so
is moving the note to the Trash.

## Security and your account

*Settings* shows your sessions and lets you sign out everywhere, change your password and mute the notices for new
sign-ins and share links. Notices for password changes and for chats being linked or unlinked cannot be muted:
you are told in the app and in your chats, so you notice if someone else acts on your account.
