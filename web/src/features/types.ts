import type { Schemas } from '../api/client'

export type Note = Schemas['Note']
export type NotePart = Schemas['NotePart']
export type Page = Schemas['Page']
export type Category = Schemas['Category']
export type Board = Schemas['Board']
export type BoardCategory = Schemas['BoardCategory']
export type NotePage = Schemas['NotePage']

/** The id used for the Inbox where a lane id is needed. */
export const INBOX = 'inbox'
