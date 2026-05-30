import axios from 'axios'

const api = axios.create({ baseURL: '/api/v1', withCredentials: true })

// Read a cookie value by name (non-HttpOnly cookies only).
function getCookie(name: string): string | null {
  const m = document.cookie.match(
    new RegExp('(?:^|; )' + name.replace(/[()[\]{}*+?.\\^$|]/g, '\\$&') + '=([^;]*)')
  )
  return m ? decodeURIComponent(m[1]) : null
}

// Inject X-CSRF-Token for all state-changing requests (double-submit cookie).
api.interceptors.request.use(config => {
  if (config.method && !['get', 'head', 'options'].includes(config.method.toLowerCase())) {
    const csrf = getCookie('cs_csrf')
    if (csrf) config.headers['X-CSRF-Token'] = csrf
  }
  return config
})

// Redirect to login on 401 responses from protected API calls.
api.interceptors.response.use(
  res => res,
  err => {
    if (err.response?.status === 401 && !err.config.url?.startsWith('/auth/')) {
      window.dispatchEvent(new Event('cs:unauthorized'))
    }
    return Promise.reject(err)
  },
)

// ── Auth ──────────────────────────────────────────────────────────────────────

export interface AuthStatus {
  initialized: boolean
  logged_in: boolean
}

export const getAuthStatus = () =>
  api.get<{ data: AuthStatus }>('/auth/status').then(r => r.data.data)

export const initAdmin = (password: string, confirm_password: string) =>
  api.post('/auth/init', { password, confirm_password })

export const login = (username: string, password: string) =>
  api.post<{ data: { username: string; is_admin: boolean } }>('/auth/login', { username, password })

export const logout = () =>
  api.post('/auth/logout')

export const changePassword = (current_password: string, new_password: string, confirm_password: string) =>
  api.post('/auth/change-password', { current_password, new_password, confirm_password })

// ── Current user ──────────────────────────────────────────────────────────────

export interface RolePermission {
  role_name: string
  resource: string
  can_create: boolean
  can_read: boolean
  can_update: boolean
  can_delete: boolean
}

export interface MeInfo {
  username: string
  is_admin: boolean
  role_name: string
  permissions: RolePermission[]
}

export const getMe = () =>
  api.get<{ data: MeInfo }>('/me').then(r => r.data.data)

// ── User management ───────────────────────────────────────────────────────────

export interface User {
  username: string
  is_admin: boolean
  role_name: string
  updated_at: string
}

export const listUsers = () =>
  api.get<{ data: User[] }>('/users').then(r => r.data.data)

export const createUser = (username: string, password: string, role_name?: string) =>
  api.post('/users', { username, password, role_name: role_name ?? '' })

export const deleteUser = (username: string) =>
  api.delete(`/users/${encodeURIComponent(username)}`)

export const resetUserPassword = (username: string, password: string) =>
  api.put(`/users/${encodeURIComponent(username)}/password`, { password })

export const assignUserRole = (username: string, role_name: string) =>
  api.put(`/users/${encodeURIComponent(username)}/role`, { role_name })

// ── Role management ───────────────────────────────────────────────────────────

export interface Role {
  name: string
  description: string
  updated_at: string
}

export const listRoles = () =>
  api.get<{ data: Role[] }>('/roles').then(r => r.data.data)

export const createRole = (name: string, description: string) =>
  api.post('/roles', { name, description })

export const deleteRole = (name: string) =>
  api.delete(`/roles/${encodeURIComponent(name)}`)

export const getRolePermissions = (name: string) =>
  api.get<{ data: RolePermission[] }>(`/roles/${encodeURIComponent(name)}/permissions`).then(r => r.data.data)

export const setRolePermissions = (name: string, perms: RolePermission[]) =>
  api.put(`/roles/${encodeURIComponent(name)}/permissions`, perms)

export interface Config {
  name: string
  version: number
  detail: string
  created_at: string
  updated_at: string
}

export interface AgentGroup {
  Name: string
  Description: string
  IPSelectorJSON: string
  CreatedAt: string
  UpdatedAt: string
}

export interface GroupIPSelector {
  ips: string[]
}

export interface GroupTag {
  GroupName: string
  TagName: string
  TagValue: string
}

export interface GroupConfigMapping {
  GroupName: string
  ConfigName: string
  ConfigType: string
}

export interface Agent {
  InstanceID: string
  AgentType: string
  IP: string
  Hostname: string
  Version: string
  RunningStatus: string
  LastHeartbeat: string
}

// Pipeline configs
export const listPipelineConfigs = () => api.get<{ data: Config[] }>('/pipeline-configs').then(r => r.data.data ?? [])
export const createPipelineConfig = (name: string, detail: string) =>
  api.post('/pipeline-configs', { name, detail })
export const updatePipelineConfig = (name: string, detail: string) =>
  api.put(`/pipeline-configs/${encodeURIComponent(name)}`, { detail })
export const deletePipelineConfig = (name: string) =>
  api.delete(`/pipeline-configs/${encodeURIComponent(name)}`)

// Instance configs
export const listInstanceConfigs = () => api.get<{ data: Config[] }>('/instance-configs').then(r => r.data.data ?? [])
export const createInstanceConfig = (name: string, detail: string) =>
  api.post('/instance-configs', { name, detail })
export const updateInstanceConfig = (name: string, detail: string) =>
  api.put(`/instance-configs/${encodeURIComponent(name)}`, { detail })
export const deleteInstanceConfig = (name: string) =>
  api.delete(`/instance-configs/${encodeURIComponent(name)}`)

// Groups
export const listGroups = () => api.get<{ data: AgentGroup[] }>('/groups').then(r => r.data.data ?? [])
export const createGroup = (name: string, description: string) =>
  api.post('/groups', { Name: name, Description: description })
export const updateGroup = (name: string, description: string) =>
  api.put(`/groups/${encodeURIComponent(name)}`, { description })
export const deleteGroup = (name: string) =>
  api.delete(`/groups/${encodeURIComponent(name)}`)
export const getGroupTags = (name: string) =>
  api.get<{ data: GroupTag[] }>(`/groups/${encodeURIComponent(name)}/tags`).then(r => r.data.data ?? [])
export const setGroupTags = (name: string, tags: { TagName: string; TagValue: string }[]) =>
  api.put(`/groups/${encodeURIComponent(name)}/tags`, tags)
export const getGroupIPSelector = (name: string) =>
  api.get<{ data: GroupIPSelector }>(`/groups/${encodeURIComponent(name)}/ip-selector`)
    .then(r => ({ ips: r.data.data?.ips ?? [] }))
export const setGroupIPSelector = (name: string, selector: GroupIPSelector) =>
  api.put(`/groups/${encodeURIComponent(name)}/ip-selector`, selector)
export const deleteGroupIPSelector = (name: string) =>
  api.delete(`/groups/${encodeURIComponent(name)}/ip-selector`)
export const getGroupConfigs = (name: string) =>
  api.get<{ data: GroupConfigMapping[] }>(`/groups/${encodeURIComponent(name)}/configs`).then(r => r.data.data ?? [])
export const addGroupConfig = (groupName: string, type: string, configName: string) =>
  api.put(`/groups/${encodeURIComponent(groupName)}/configs/${type}/${encodeURIComponent(configName)}`)
export const removeGroupConfig = (groupName: string, type: string, configName: string) =>
  api.delete(`/groups/${encodeURIComponent(groupName)}/configs/${type}/${encodeURIComponent(configName)}`)

// Agents
export interface AgentsPagedResult {
  agents: Agent[]
  total: number
  page: number
  page_size: number
}

export const listAgents = (opts?: { group?: string; search?: string; page?: number; pageSize?: number }) => {
  const params: Record<string, string | number> = {}
  if (opts?.group) params.group = opts.group
  if (opts?.search) params.search = opts.search
  if (opts?.page) params.page = opts.page
  if (opts?.pageSize) params.page_size = opts.pageSize
  return api.get<{ data: AgentsPagedResult }>('/agents', { params }).then(r => r.data.data)
}

export const getAgentDetail = (instanceID: string) =>
  api.get<{ data: { agent: Agent; config_statuses: AgentConfigStatus[] } }>(`/agents/${encodeURIComponent(instanceID)}`).then(r => r.data.data)

// Onetime commands
export interface OnetimeCommand {
  name: string
  detail: string
  expire_time: number
  created_at: string
}

export const listOnetimeCommands = () =>
  api.get<{ data: OnetimeCommand[] }>('/onetime-commands').then(r => r.data.data ?? [])
export const createOnetimeCommand = (name: string, detail: string, expire_time: number) =>
  api.post('/onetime-commands', { name, detail, expire_time })
export const deleteOnetimeCommand = (name: string) =>
  api.delete(`/onetime-commands/${encodeURIComponent(name)}`)

export interface AgentConfigStatus {
  ConfigName: string
  ConfigType: string
  Status: number
  Message: string
  UpdatedAt: string
}

// Config history
export interface ConfigHistory {
  id: number
  resource_type: string
  resource_name: string
  version: number
  action: string
  detail: string
  changed_by: string
  changed_at: string
}

export const listConfigHistory = (type: string, name: string) =>
  api.get<{ data: ConfigHistory[] }>(`/history/${encodeURIComponent(type)}/${encodeURIComponent(name)}`).then(r => r.data.data ?? [])

export const listDeletedConfigs = (type: string) =>
  api.get<{ data: ConfigHistory[] }>(`/deleted-history/${encodeURIComponent(type)}`).then(r => r.data.data ?? [])

export const rollbackConfig = (type: string, name: string, id: number) =>
  api.post(`/history/${encodeURIComponent(type)}/${encodeURIComponent(name)}/${id}/rollback`)

// Audit logs
export interface AuditLog {
  id: number
  username: string
  action: string
  resource_type: string
  resource_name: string
  detail: string
  client_ip: string
  created_at: string
}

export interface AuditLogsResult {
  logs: AuditLog[]
  total: number
  page: number
  page_size: number
}

export const listAuditLogs = (page = 1, pageSize = 50) =>
  api.get<{ data: AuditLogsResult }>('/audit-logs', { params: { page, page_size: pageSize } }).then(r => r.data.data)

