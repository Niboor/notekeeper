-- +goose Up
-- Listing a user's links and counting the recent and active ones (CORE-SH3, SEC-SHR-11).
create index share_links_user_created on share_links (user_id, created_at desc);
create index share_links_note on share_links (user_id, note_id);

-- +goose Down
drop index share_links_note;
drop index share_links_user_created;
