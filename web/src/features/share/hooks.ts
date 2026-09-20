import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, unwrapEmpty } from '../../api/client'
import type { Schemas } from '../../api/client'

export type ShareLink = Schemas['ShareLink']
export type Expiry = '1h' | '1d' | '7d' | '30d'

/** The user's active links, for one note or all (CORE-SH3). */
export function useShareLinks(noteId?: string) {
  return useQuery({
    queryKey: ['share-links', noteId ?? 'all'],
    queryFn: async () =>
      unwrap(await api.GET('/api/v1/share-links', { params: { query: noteId ? { note_id: noteId } : {} } })),
  })
}

/** Ids of notes that have a working link, for the indicator on cards (WEB-21). */
export function useSharedNoteIds(): Set<string> {
  const { data } = useShareLinks()
  return new Set((data?.items ?? []).filter((l) => l.note_active).map((l) => l.note_id))
}

export function useCreateShare(noteId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (expiresIn: Expiry) =>
      unwrap(await api.POST('/api/v1/notes/{id}/share-links', { params: { path: { id: noteId } }, body: { expires_in: expiresIn } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['share-links'] }),
  })
}

export function useRevokeShare() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: string) => unwrapEmpty(await api.DELETE('/api/v1/share-links/{id}', { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['share-links'] }),
  })
}

export function useRevokeAllShares() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => unwrap(await api.POST('/api/v1/share-links/revoke-all')),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['share-links'] }),
  })
}
