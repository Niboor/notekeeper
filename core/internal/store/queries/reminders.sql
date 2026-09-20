-- name: InsertReminder :one
insert into reminders (id, user_id, note_id, due_at, rrule, tz, created_at) values ($1, $2, $3, $4, $5, $6, $7) returning *;

-- name: GetReminder :one
select * from reminders where id = $1 and user_id = $2;

-- name: CountNoteReminders :one
select count(*) from reminders where user_id = $1 and note_id = $2;

-- name: CountUserReminders :one
select count(*) from reminders where user_id = $1 and state in ('pending', 'suspended');

-- name: ListRemindersForNotes :many
select * from reminders where note_id = any(@ids::uuid[]) order by due_at, id;

-- name: SetReminder :one
update reminders set due_at = $3, rrule = $4, tz = $5, state = 'pending', claimed_until = null, version = version + 1
where id = $1 and user_id = $2 returning *;

-- name: SnoozeReminder :one
update reminders set due_at = $3, state = 'pending', claimed_until = null, version = version + 1
where id = $1 and user_id = $2 and state in ('pending', 'fired', 'done') returning *;

-- name: FinishReminder :one
update reminders set state = 'done', claimed_until = null, version = version + 1
where id = $1 and user_id = $2 and state in ('pending', 'fired') returning *;

-- name: DeleteReminder :one
delete from reminders where id = $1 and user_id = $2 returning note_id;

-- name: ListUpcomingReminders :many
select r.*, coalesce((select left(p.text, 140) from note_parts p where p.user_id = r.user_id and p.note_id = r.note_id and p.kind = 'text'
                      order by p.ordinal limit 1), '')::text as excerpt
from reminders r join notes n on n.user_id = r.user_id and n.id = r.note_id
where r.user_id = $1 and r.state = 'pending' and n.state = 'active'
order by r.due_at, r.id limit 200;

-- name: ClaimDueReminders :many
update reminders set claimed_until = @lease::timestamptz
where id in (
  select r.id from reminders r
  where r.state = 'pending' and r.due_at <= @now::timestamptz
    and (r.claimed_until is null or r.claimed_until < @now::timestamptz)
    and exists (select 1 from users u where u.id = r.user_id and u.status = 'active')
  order by r.due_at limit 100
  for update skip locked)
returning id, user_id;

-- name: GetDueReminder :one
select * from reminders where id = $1 and user_id = $2 and state = 'pending' and due_at <= $3;

-- name: MarkReminderFired :exec
update reminders set state = 'fired', last_fired_at = $2, claimed_until = null, version = version + 1 where id = $1;

-- name: AdvanceReminder :exec
update reminders set due_at = $2, last_fired_at = $3, claimed_until = null, version = version + 1 where id = $1;

-- name: SuspendNoteReminders :many
update reminders set state = 'suspended', claimed_until = null, version = version + 1
where user_id = $1 and note_id = $2 and state = 'pending' returning id;

-- name: SuspendedNoteReminders :many
select * from reminders where user_id = $1 and note_id = $2 and state = 'suspended';

-- name: RestoreReminder :exec
update reminders set state = $2, due_at = $3, version = version + 1 where id = $1;

-- name: ReminderTargets :many
select id, bot_instance_id, external_user_id, conversation_id from external_identities
where user_id = $1 and reminder_target and conversation_id is not null order by linked_at;

-- name: NoticeTargets :many
select id, bot_instance_id, external_user_id, conversation_id from external_identities
where user_id = $1 and conversation_id is not null order by linked_at;

-- name: InsertReminderOutbox :exec
insert into bot_outbox (id, bot_instance_id, kind, user_id, external_user_id, conversation_id, payload, next_attempt_at, due_at, reminder_id)
values ($1, $2, 'reminder', $3, $4, $5, $6, $7, $8, $9);

-- name: ReminderByChatMessage :one
select o.reminder_id from outbox_messages m join bot_outbox o on o.id = m.outbox_id
where m.bot_instance_id = $1 and m.conversation_id = $2 and m.message_id = $3 and o.user_id = $4 and o.reminder_id is not null;

-- name: ListNotifications :many
select * from notifications where user_id = $1 and (not @unread_only::boolean or read_at is null) order by created_at desc, id limit 100;

-- name: CountUnreadNotifications :one
select count(*) from notifications where user_id = $1 and read_at is null;

-- name: MarkNotificationsRead :many
update notifications set read_at = $2 where user_id = $1 and read_at is null and (sqlc.narg('ids')::uuid[] is null or id = any(sqlc.narg('ids')::uuid[])) returning id;

-- name: NoteExcerpt :one
select coalesce((select left(p.text, 300) from note_parts p where p.user_id = $1 and p.note_id = $2 and p.kind = 'text' order by p.ordinal limit 1), '')::text;

-- name: NoteAttachmentInfo :many
select a.id, a.filename, a.media_type, b.size_bytes from note_parts p
join attachments a on a.user_id = p.user_id and a.id = p.attachment_id
join blobs b on b.user_id = a.user_id and b.id = a.blob_id
where p.user_id = $1 and p.note_id = $2 and p.kind = 'attachment' order by p.ordinal;
