import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { api, ApiError, unwrap, unwrapEmpty } from '../api/client'
import { CLIENT_HEADER } from '../api/session'
import { useToast } from '../components/Toast'
import { t } from '../i18n'
import {
  insertIntoBoard, insertIntoInbox, removeFromBoard, removeFromInbox, replaceInInbox, replaceOnBoard, withInboxTotal,
  type InboxData,
} from './cache'
import type { Board, Note } from './types'

// ---- queries --------------------------------------------------------------------------------

export function usePages() {
  return useQuery({ queryKey: ['pages'], queryFn: async () => unwrap(await api.GET('/api/v1/pages')).items })
}

export function useBoard(pageId: string | undefined) {
  return useQuery({
    queryKey: ['board', pageId],
    enabled: !!pageId,
    queryFn: async () =>
      unwrap(await api.GET('/api/v1/pages/{id}/board', { params: { path: { id: pageId! }, query: { notes_per_category: 100 } } })),
  })
}

export function useInbox() {
  return useInfiniteQuery({
    queryKey: ['inbox'],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) =>
      unwrap(await api.GET('/api/v1/inbox/notes', { params: { query: { cursor: pageParam, limit: 50 } } })),
    getNextPageParam: (last) => last.next_cursor,
  })
}

export function useTrash() {
  return useInfiniteQuery({
    queryKey: ['trash'],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => unwrap(await api.GET('/api/v1/trash/notes', { params: { query: { cursor: pageParam, limit: 50 } } })),
    getNextPageParam: (last) => last.next_cursor,
  })
}

// ---- optimistic plumbing ---------------------------------------------------------------------

interface Snapshot {
  boards: [readonly unknown[], Board | undefined][]
  inbox: InboxData | undefined
}

async function snapshot(qc: QueryClient): Promise<Snapshot> {
  await qc.cancelQueries({ queryKey: ['board'] })
  await qc.cancelQueries({ queryKey: ['inbox'] })
  return { boards: qc.getQueriesData<Board>({ queryKey: ['board'] }), inbox: qc.getQueryData<InboxData>(['inbox']) }
}

function rollback(qc: QueryClient, s: Snapshot | undefined) {
  if (!s) return
  for (const [key, data] of s.boards) qc.setQueryData(key, data)
  qc.setQueryData(['inbox'], s.inbox)
}

function refresh(qc: QueryClient) {
  void qc.invalidateQueries({ queryKey: ['board'] })
  void qc.invalidateQueries({ queryKey: ['inbox'] })
  void qc.invalidateQueries({ queryKey: ['pages'] })
}

function mapBoards(qc: QueryClient, fn: (b: Board) => Board) {
  for (const [key, data] of qc.getQueriesData<Board>({ queryKey: ['board'] })) {
    if (data) qc.setQueryData(key, fn(data))
  }
}

/** Takes a note out of every cached view. Returns whether it was an Inbox note, which changes the Inbox count. */
function detach(qc: QueryClient, note: Note) {
  mapBoards(qc, (b) => {
    let out = removeFromBoard(b, note.id)
    if (!note.category_id) out = withInboxTotal(out, -1)
    return out
  })
  qc.setQueryData<InboxData | undefined>(['inbox'], (d) => removeFromInbox(d, note.id))
}

// ---- moving ----------------------------------------------------------------------------------

export interface MoveVars {
  note: Note
  /** null moves the note to the Inbox. */
  categoryId: string | null
  /** Where in the target column it lands, for the optimistic view. */
  index: number
  afterId?: string | null
  beforeId?: string | null
}

export function useMoveNote() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: MoveVars) =>
      unwrap(
        await api.POST('/api/v1/notes/{id}/move', {
          params: { path: { id: v.note.id } },
          body: { category_id: v.categoryId, after_id: v.afterId ?? null, before_id: v.beforeId ?? null },
        }),
      ),
    onMutate: async (v) => {
      const snap = await snapshot(qc)
      // A note can be dropped onto the very spot it left: nothing changes then.
      const moved = { ...v.note, category_id: v.categoryId }
      mapBoards(qc, (b) => {
        let out = removeFromBoard(b, v.note.id)
        if (!v.note.category_id) out = withInboxTotal(out, -1)
        if (v.categoryId) out = insertIntoBoard(out, v.categoryId, moved, v.index)
        else out = withInboxTotal(out, 1)
        return out
      })
      qc.setQueryData<InboxData | undefined>(['inbox'], (d) => {
        const without = removeFromInbox(d, v.note.id)
        return v.categoryId ? without : insertIntoInbox(without, moved)
      })
      return snap
    },
    onError: (_e, _v, snap) => {
      rollback(qc, snap)
      toast({ message: t('toast.failed') })
    },
    onSettled: () => refresh(qc),
  })
}

// ---- dismiss, restore, delete ---------------------------------------------------------------

export function useRestoreNote() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST('/api/v1/notes/{id}/restore', { params: { path: { id } } })),
    onError: () => toast({ message: t('toast.failed') }),
    onSettled: () => {
      refresh(qc)
      void qc.invalidateQueries({ queryKey: ['trash'] })
    },
  })
}

/** Dismisses a note at once (no confirmation) and offers Undo for 10 seconds (WEB-6, CORE-N8). */
export function useDismissNote() {
  const qc = useQueryClient()
  const { toast } = useToast()
  const restore = useRestoreNote()
  return useMutation({
    mutationFn: async (note: Note) => unwrap(await api.POST('/api/v1/notes/{id}/dismiss', { params: { path: { id: note.id } } })),
    onMutate: async (note) => {
      const snap = await snapshot(qc)
      detach(qc, note)
      return snap
    },
    onSuccess: (_d, note) => {
      toast({ message: t('toast.dismissed'), action: { label: t('toast.undo'), onClick: () => restore.mutate(note.id) } })
    },
    onError: (_e, _n, snap) => {
      rollback(qc, snap)
      toast({ message: t('toast.failed') })
    },
    onSettled: () => {
      refresh(qc)
      void qc.invalidateQueries({ queryKey: ['trash'] })
    },
  })
}

export function useDeleteForever() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (id: string) => unwrapEmpty(await api.DELETE('/api/v1/notes/{id}', { params: { path: { id } } })),
    onSuccess: () => toast({ message: t('toast.dismissedDeletedForever') }),
    onError: () => toast({ message: t('toast.failed') }),
    onSettled: () => void qc.invalidateQueries({ queryKey: ['trash'] }),
  })
}

// ---- creating and editing ------------------------------------------------------------------

export interface CreateVars {
  /** null creates the note in the Inbox. */
  categoryId: string | null
  text: string
  attachmentIds: string[]
}

export function useCreateNote() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: CreateVars & { id: string }) => {
      const parts: { type: 'text' | 'attachment'; text?: string; attachment_id?: string }[] = []
      if (v.text.trim()) parts.push({ type: 'text', text: v.text })
      for (const a of v.attachmentIds) parts.push({ type: 'attachment', attachment_id: a })
      return unwrap(await api.POST('/api/v1/notes', { body: { id: v.id, category_id: v.categoryId, parts } }))
    },
    onMutate: async (v) => {
      const snap = await snapshot(qc)
      const now = new Date().toISOString()
      const temp: Note = {
        id: v.id, category_id: v.categoryId, state: 'active', created_at: now, updated_at: now, version: 1,
        parts: v.text.trim()
          ? [{ id: v.id + '-t', kind: 'text', text: v.text, attach_reason: 'app', created_at: now }]
          : [],
      }
      if (temp.parts.length > 0) {
        if (v.categoryId) mapBoards(qc, (b) => insertIntoBoard(b, v.categoryId!, temp, 0))
        else {
          qc.setQueryData<InboxData | undefined>(['inbox'], (d) => insertIntoInbox(d, temp))
          mapBoards(qc, (b) => withInboxTotal(b, 1))
        }
      }
      return snap
    },
    onError: (_e, _v, snap) => {
      rollback(qc, snap)
      toast({ message: t('toast.failed') })
    },
    onSettled: () => refresh(qc),
  })
}

export function useEditPart() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: { note: Note; partId: string; text: string }) =>
      unwrap(
        await api.PATCH('/api/v1/notes/{id}/parts/{partId}', {
          params: { path: { id: v.note.id, partId: v.partId } },
          body: { text: v.text, base_version: v.note.version },
        }),
      ),
    onMutate: async (v) => {
      const snap = await snapshot(qc)
      const edited: Note = { ...v.note, parts: v.note.parts.map((p) => (p.id === v.partId ? { ...p, text: v.text } : p)) }
      mapBoards(qc, (b) => replaceOnBoard(b, edited))
      qc.setQueryData<InboxData | undefined>(['inbox'], (d) => replaceInInbox(d, edited))
      return snap
    },
    onError: (_e, _v, snap) => {
      rollback(qc, snap)
      toast({ message: t('toast.failed') })
    },
    onSettled: () => refresh(qc),
  })
}

export function useAddPart() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: { noteId: string; text?: string; attachmentId?: string }) =>
      unwrap(
        await api.POST('/api/v1/notes/{id}/parts', {
          params: { path: { id: v.noteId } },
          body: v.attachmentId ? { type: 'attachment', attachment_id: v.attachmentId } : { type: 'text', text: v.text },
        }),
      ),
    onError: () => toast({ message: t('toast.failed') }),
    onSettled: () => refresh(qc),
  })
}

export function useRemovePart() {
  const qc = useQueryClient()
  const { toast } = useToast()
  return useMutation({
    mutationFn: async (v: { noteId: string; partId: string }) =>
      unwrap(await api.DELETE('/api/v1/notes/{id}/parts/{partId}', { params: { path: { id: v.noteId, partId: v.partId } } })),
    onError: () => toast({ message: t('toast.failed') }),
    onSettled: () => refresh(qc),
  })
}

// ---- attachments -----------------------------------------------------------------------------

export interface UploadedFile {
  id: string
  filename: string
  media_type: string
  size: number
}

/** Uploads one file as a raw streamed body (docs/design/02 section 1.3). */
export async function uploadFile(file: File): Promise<UploadedFile> {
  const id = crypto.randomUUID()
  const res = await fetch(`/api/v1/attachments/${id}`, {
    method: 'PUT',
    headers: {
      ...CLIENT_HEADER,
      'Content-Type': 'application/octet-stream',
      'X-Filename': encodeURIComponent(file.name),
      'X-Media-Type': file.type || 'application/octet-stream',
    },
    body: file,
  })
  if (!res.ok) {
    const problem = (await res.json().catch(() => ({}))) as { code?: string }
    throw new ApiError(res.status, problem.code ?? 'error')
  }
  return (await res.json()) as UploadedFile
}
