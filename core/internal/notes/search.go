package notes

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Match markers in snippets: private-use characters that cannot occur in normal text; the client
// turns them into highlights, so no markup is ever produced by the server (SEC-CNT-1).
const (
	MarkStart = ""
	MarkEnd   = ""
)

// SearchInput describes a search (CORE-N13, WEB-14).
type SearchInput struct {
	Query         string
	Scope         string // "active" (default), "trash" or "all"
	PageID        *uuid.UUID
	CategoryID    *uuid.UUID
	HasAttachment *bool
	HasReminder   *bool
	Limit         int
	Cursor        string
}

// SearchHit is one result.
type SearchHit struct {
	Note    Note
	Snippet string
}

// SearchPage is one page of results.
type SearchPage struct {
	Hits       []SearchHit
	NextCursor string
}

const (
	maxQueryTokens = 8
	maxTokenLength = 40
	maxSearchDepth = 2000 // offset paging stops here: nobody scrolls further, and it bounds the cost
)

// tokens splits user input into words made of letters and digits. Only those characters reach the
// text-search query, so there is no query syntax to inject into (SEC-API-1).
func tokens(q string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		r := []rune(f)
		if len(r) > maxTokenLength {
			r = r[:maxTokenLength]
		}
		out = append(out, string(r))
		if len(out) == maxQueryTokens {
			break
		}
	}
	return out
}

// tsQuery turns words into an AND of prefix terms: "conc fri" finds "concert friday".
func tsQuery(words []string) string {
	terms := make([]string, len(words))
	for i, w := range words {
		terms[i] = "'" + strings.ReplaceAll(w, "'", "''") + "':*"
	}
	return strings.Join(terms, " & ")
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Search finds notes for a user. The index row is maintained by a trigger in the same transaction
// as every change, so results are never stale (CORE-N13). Row-level security confines it to the
// user's own notes; the user id in the query is the first barrier, not the only one.
func (s *Service) Search(ctx context.Context, user uuid.UUID, in SearchInput) (SearchPage, error) {
	words := tokens(in.Query)
	if len(words) == 0 {
		return SearchPage{}, nil
	}
	limit := clampLimit(in.Limit)
	offset := 0
	if in.Cursor != "" {
		raw, err := decodeCursorRaw(in.Cursor)
		if err != nil {
			return SearchPage{}, err
		}
		n, err := strconv.Atoi(strings.TrimPrefix(raw, "o:"))
		if err != nil || !strings.HasPrefix(raw, "o:") || n < 0 || n > maxSearchDepth {
			return SearchPage{}, ErrInvalidCursor
		}
		offset = n
	}
	states := []string{"active"}
	switch in.Scope {
	case "", "active":
	case "trash":
		states = []string{"deleted"}
	case "all":
		states = []string{"active", "deleted"}
	default:
		return SearchPage{}, invalid("unknown scope")
	}

	var page SearchPage
	err := s.St.InUserReadTx(ctx, user, func(tx pgx.Tx, q *dbq.Queries) error {
		rows, err := s.runSearch(ctx, tx, user, words, in, states, limit+1, offset)
		if err != nil {
			return err
		}
		if len(rows) == 0 && offset == 0 && len([]rune(strings.Join(words, " "))) >= 3 {
			// Nothing matched as words: look for the text anywhere inside notes (partial words).
			rows, err = s.runSubstring(ctx, tx, user, words, in, states, limit+1)
			if err != nil {
				return err
			}
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.id
		}
		notes, err := loadNotes(ctx, q, user, ids)
		if err != nil {
			return err
		}
		if err := attachLocations(ctx, q, user, notes); err != nil {
			return err
		}
		byID := make(map[uuid.UUID]Note, len(notes))
		for _, n := range notes {
			byID[n.Note.ID] = n
		}
		for _, r := range rows { // matched by id: a hit whose note is gone is dropped, never mispaired
			if n, ok := byID[r.id]; ok {
				page.Hits = append(page.Hits, SearchHit{Note: n, Snippet: r.snippet})
			}
		}
		if more {
			page.NextCursor = encodeCursorRaw("o:" + strconv.Itoa(offset+limit))
		}
		return nil
	})
	return page, err
}

type hit struct {
	id      uuid.UUID
	snippet string
}

// filters appends the optional conditions and returns them with the next placeholder index.
func filters(in SearchInput, args []any) (string, []any) {
	var sb strings.Builder
	add := func(cond string, v any) {
		args = append(args, v)
		sb.WriteString(" and " + strings.ReplaceAll(cond, "$$", "$"+strconv.Itoa(len(args))))
	}
	if in.CategoryID != nil {
		add("n.category_id = $$", *in.CategoryID)
	}
	if in.PageID != nil {
		add("c.page_id = $$", *in.PageID)
	}
	if in.HasAttachment != nil {
		cond := "exists (select 1 from note_parts p where p.note_id = n.id and p.attachment_id is not null)"
		if !*in.HasAttachment {
			cond = "not " + cond
		}
		sb.WriteString(" and " + cond)
	}
	if in.HasReminder != nil {
		cond := "exists (select 1 from reminders r where r.note_id = n.id and r.state in ('pending','fired','suspended'))"
		if !*in.HasReminder {
			cond = "not " + cond
		}
		sb.WriteString(" and " + cond)
	}
	return sb.String(), args
}

func (s *Service) runSearch(ctx context.Context, tx pgx.Tx, user uuid.UUID, words []string, in SearchInput, states []string, limit, offset int) ([]hit, error) {
	args := []any{tsQuery(words), user, states, MarkStart, MarkEnd}
	cond, args := filters(in, args)
	args = append(args, limit, offset)
	sql := fmt.Sprintf(`
select n.id,
       ts_headline('english', s.body, query.q, 'StartSel=' || $4 || ', StopSel=' || $5 || ', MaxFragments=2, MinWords=6, MaxWords=24, FragmentDelimiter= … ') as snippet
from note_search s
join notes n on n.user_id = s.user_id and n.id = s.note_id
left join categories c on c.user_id = n.user_id and c.id = n.category_id
cross join lateral (select to_tsquery('english', nk_unaccent($1)) as q) query
where s.user_id = $2 and n.state = any($3) and s.doc @@ query.q %s
order by ts_rank_cd(s.doc, query.q) desc, n.created_at desc, n.id desc
limit $%d offset $%d`, cond, len(args)-1, len(args))
	return scanHits(ctx, tx, sql, args)
}

func (s *Service) runSubstring(ctx context.Context, tx pgx.Tx, user uuid.UUID, words []string, in SearchInput, states []string, limit int) ([]hit, error) {
	needle := escapeLike(strings.ToLower(strings.Join(words, " ")))
	args := []any{needle, user, states}
	cond, args := filters(in, args)
	args = append(args, limit)
	sql := fmt.Sprintf(`
select n.id, left(s.body, 200) as snippet
from note_search s
join notes n on n.user_id = s.user_id and n.id = s.note_id
left join categories c on c.user_id = n.user_id and c.id = n.category_id
where s.user_id = $2 and n.state = any($3) and nk_unaccent(lower(s.body)) like '%%' || nk_unaccent($1) || '%%' %s
order by n.created_at desc, n.id desc
limit $%d`, cond, len(args))
	return scanHits(ctx, tx, sql, args)
}

func scanHits(ctx context.Context, tx pgx.Tx, sql string, args []any) ([]hit, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (hit, error) {
		var h hit
		err := r.Scan(&h.id, &h.snippet)
		return h, err
	})
}

// loadNotes returns the notes in the order of ids.
func loadNotes(ctx context.Context, q *dbq.Queries, user uuid.UUID, ids []uuid.UUID) ([]Note, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.NotesByIDs(ctx, dbq.NotesByIDsParams{UserID: user, Column2: ids})
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]dbq.Note{}
	for _, n := range rows {
		byID[n.ID] = n
	}
	ordered := make([]dbq.Note, 0, len(ids))
	for _, id := range ids {
		if n, ok := byID[id]; ok {
			ordered = append(ordered, n)
		}
	}
	return withParts(ctx, q, ordered)
}
