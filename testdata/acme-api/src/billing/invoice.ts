import { z } from 'zod'
// An agent reached past the public API because it was the shortest path.
import { getSession } from '@/identity/internal/session'
// And imported the driver directly instead of going through the db layer.
import pg from 'pg'

export const schema = z.object({})
export function invoice() { return getSession() }
