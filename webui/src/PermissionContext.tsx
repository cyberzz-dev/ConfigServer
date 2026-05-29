import { createContext, useContext, useState, useCallback, type ReactNode } from 'react'
import type { MeInfo, RolePermission } from './api'

// ── Types ─────────────────────────────────────────────────────────────────────

interface PermissionContextValue {
  /** Current user info, or null when not loaded. */
  me: MeInfo | null
  /** Load (or reload) current user info from the server. */
  loadMe: () => Promise<void>
  /** Whether the current user has the given action on a resource. Admin always returns true. */
  can: (resource: string, action: 'create' | 'read' | 'update' | 'delete') => boolean
}

// ── Context ───────────────────────────────────────────────────────────────────

const PermissionContext = createContext<PermissionContextValue>({
  me: null,
  loadMe: async () => {},
  can: () => false,
})

// ── Provider ──────────────────────────────────────────────────────────────────

interface Props {
  children: ReactNode
  getMe: () => Promise<MeInfo>
}

export function PermissionProvider({ children, getMe }: Props) {
  const [me, setMe] = useState<MeInfo | null>(null)

  const loadMe = useCallback(async () => {
    try {
      const info = await getMe()
      setMe(info)
    } catch {
      setMe(null)
    }
  }, [getMe])

  const can = useCallback(
    (resource: string, action: 'create' | 'read' | 'update' | 'delete'): boolean => {
      if (!me) return false
      if (me.is_admin) return true
      const perm: RolePermission | undefined = me.permissions?.find(p => p.resource === resource)
      if (!perm) return false
      switch (action) {
        case 'create': return perm.can_create
        case 'read':   return perm.can_read
        case 'update': return perm.can_update
        case 'delete': return perm.can_delete
      }
    },
    [me],
  )

  return (
    <PermissionContext.Provider value={{ me, loadMe, can }}>
      {children}
    </PermissionContext.Provider>
  )
}

// ── Hooks ─────────────────────────────────────────────────────────────────────

export function usePermission(resource: string, action: 'create' | 'read' | 'update' | 'delete') {
  const { can } = useContext(PermissionContext)
  return can(resource, action)
}

export function useCurrentUser() {
  const { me } = useContext(PermissionContext)
  return me
}

export function useLoadMe() {
  const { loadMe } = useContext(PermissionContext)
  return loadMe
}
