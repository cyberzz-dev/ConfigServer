import { useEffect, useState } from 'react'
import {
  Table, Button, Modal, Form, Input, Space, message, Checkbox,
} from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import type { Role, RolePermission } from '../api'
import { listRoles, createRole, deleteRole, getRolePermissions, setRolePermissions } from '../api'
import DeleteConfirmModal from '../components/DeleteConfirmModal'

const ALL_RESOURCES = [
  { key: 'pipeline_configs', label: 'Pipeline Configs' },
  { key: 'instance_configs', label: 'Instance Configs' },
  { key: 'onetime_commands', label: 'Onetime Commands' },
  { key: 'agent_groups',     label: 'Agent Groups' },
  { key: 'agents',           label: 'Agents' },
]

const ACTIONS: Array<{ key: keyof PermRow; label: string }> = [
  { key: 'can_read',   label: 'Read' },
  { key: 'can_create', label: 'Create' },
  { key: 'can_update', label: 'Update' },
  { key: 'can_delete', label: 'Delete' },
]

interface PermRow {
  resource:   string
  can_read:   boolean
  can_create: boolean
  can_update: boolean
  can_delete: boolean
}

function defaultPermRows(): PermRow[] {
  return ALL_RESOURCES.map(r => ({
    resource: r.key, can_read: false, can_create: false, can_update: false, can_delete: false,
  }))
}

function permsToRows(perms: RolePermission[]): PermRow[] {
  return ALL_RESOURCES.map(r => {
    const p = perms.find(x => x.resource === r.key)
    return {
      resource:   r.key,
      can_read:   p?.can_read   ?? false,
      can_create: p?.can_create ?? false,
      can_update: p?.can_update ?? false,
      can_delete: p?.can_delete ?? false,
    }
  })
}

function rowsToPerms(roleName: string, rows: PermRow[]): RolePermission[] {
  return rows.map(r => ({ ...r, role_name: roleName }))
}

// ── Permission matrix panel ───────────────────────────────────────────────────

function PermissionPanel({ roleName }: { roleName: string }) {
  const [rows, setRows]     = useState<PermRow[]>(defaultPermRows())
  const [loading, setLoad]  = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setLoad(true)
    getRolePermissions(roleName)
      .then(p => setRows(permsToRows(p)))
      .catch(() => message.error('Failed to load permissions'))
      .finally(() => setLoad(false))
  }, [roleName])

  const toggle = (resource: string, action: keyof PermRow) => {
    setRows(prev => prev.map(r =>
      r.resource === resource ? { ...r, [action]: !r[action] } : r
    ))
  }

  const save = async () => {
    setSaving(true)
    try {
      await setRolePermissions(roleName, rowsToPerms(roleName, rows))
      message.success('Permissions saved')
    } catch {
      message.error('Failed to save permissions')
    } finally {
      setSaving(false)
    }
  }

  const columns = [
    {
      title: 'Resource', dataIndex: 'resource', key: 'resource', width: 180,
      render: (v: string) => ALL_RESOURCES.find(r => r.key === v)?.label ?? v,
    },
    ...ACTIONS.map(a => ({
      title: a.label, key: a.key, width: 90, align: 'center' as const,
      render: (_: unknown, row: PermRow) => (
        <Checkbox checked={row[a.key] as boolean} onChange={() => toggle(row.resource, a.key)} />
      ),
    })),
  ]

  return (
    <div style={{ padding: '12px 0' }}>
      <Table
        rowKey="resource"
        size="small"
        loading={loading}
        columns={columns}
        dataSource={rows}
        pagination={false}
      />
      <div style={{ marginTop: 12, textAlign: 'right' }}>
        <Button type="primary" size="small" loading={saving} onClick={save}>
          Save Permissions
        </Button>
      </div>
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export default function RolesPage() {
  const [roles, setRoles]           = useState<Role[]>([])
  const [loading, setLoading]       = useState(true)
  const [createOpen, setCreateOpen] = useState(false)
  const [expandedKey, setExpandedKey] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Role | null>(null)
  const [form] = Form.useForm()

  const refresh = async () => {
    setLoading(true)
    try { setRoles(await listRoles()) } finally { setLoading(false) }
  }

  useEffect(() => { refresh() }, [])

  const handleCreate = async () => {
    const { name, description } = await form.validateFields()
    try {
      await createRole(name, description ?? '')
      message.success('Role created')
      setCreateOpen(false)
      form.resetFields()
      refresh()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to create role')
    }
  }

  const handleDelete = async (r: Role) => {
    try {
      await deleteRole(r.name)
      message.success('Role deleted')
      if (expandedKey === r.name) setExpandedKey(null)
      setDeleteTarget(null)
      refresh()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to delete role')
    }
  }

  const columns = [
    { title: 'Name', dataIndex: 'name', key: 'name', width: 200 },
    { title: 'Description', dataIndex: 'description', key: 'description' },
    {
      title: 'Actions', key: 'actions', width: 180,
      render: (_: unknown, r: Role) => (
        <Space size={4}>
          <Button
            size="small"
            onClick={() => setExpandedKey(prev => prev === r.name ? null : r.name)}
          >
            {expandedKey === r.name ? 'Hide Permissions' : 'Edit Permissions'}
          </Button>
          <Button size="small" danger onClick={() => setDeleteTarget(r)}>Delete</Button>
        </Space>
      ),
    },
  ]

  return (
    <>
      <div className="page-header">
        <h2>Roles</h2>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => { setCreateOpen(true); form.resetFields() }}>
          Create Role
        </Button>
      </div>

      <Table
        rowKey="name"
        size="small"
        loading={loading}
        columns={columns}
        dataSource={roles}
        expandable={{
          expandedRowKeys: expandedKey ? [expandedKey] : [],
          onExpand: (_, r) => setExpandedKey(prev => prev === r.name ? null : r.name),
          expandedRowRender: r => <PermissionPanel roleName={r.name} />,
        }}
      />

      <Modal title="Create Role" open={createOpen} onOk={handleCreate} onCancel={() => setCreateOpen(false)}>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="Role Name" rules={[{ required: true }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="description" label="Description">
            <Input autoComplete="off" />
          </Form.Item>
        </Form>
      </Modal>

      <DeleteConfirmModal
        open={deleteTarget !== null}
        targetName={deleteTarget?.name ?? ''}
        entityType="Role"
        onConfirm={() => handleDelete(deleteTarget!)}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  )
}
