import { useEffect, useState } from 'react'
import {
  Table, Button, Modal, Form, Input, Space, message, Tag, Select,
} from 'antd'
import { PlusOutlined, KeyOutlined } from '@ant-design/icons'
import type { User, Role } from '../api'
import {
  listUsers, createUser, deleteUser, resetUserPassword, assignUserRole, listRoles,
} from '../api'
import DeleteConfirmModal from '../components/DeleteConfirmModal'

// ── Main page ─────────────────────────────────────────────────────────────────

export default function UsersPage() {
  const [users, setUsers]           = useState<User[]>([])
  const [roles, setRoles]           = useState<Role[]>([])
  const [loading, setLoading]       = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [resetTarget, setReset]     = useState<User | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<User | null>(null)
  const [form] = Form.useForm()
  const [resetForm] = Form.useForm()

  const refresh = async () => {
    setLoading(true)
    try {
      const [us, rs] = await Promise.all([listUsers(), listRoles()])
      setUsers(us)
      setRoles(rs)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { refresh() }, [])

  const handleCreate = async () => {
    const { username, password, role_name } = await form.validateFields()
    try {
      await createUser(username, password, role_name || undefined)
      message.success('User created')
      setCreateOpen(false)
      form.resetFields()
      refresh()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to create user')
    }
  }

  const handleDelete = async (u: User) => {
    try {
      await deleteUser(u.username)
      message.success('User deleted')
      setDeleteTarget(null)
      refresh()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to delete user')
    }
  }

  const handleReset = async () => {
    const { password } = await resetForm.validateFields()
    try {
      await resetUserPassword(resetTarget!.username, password)
      message.success('Password reset')
      setReset(null)
      resetForm.resetFields()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to reset password')
    }
  }

  const handleRoleChange = async (username: string, roleName: string) => {
    try {
      await assignUserRole(username, roleName)
      message.success('Role updated')
      setUsers(prev => prev.map(u => u.username === username ? { ...u, role_name: roleName } : u))
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to update role')
    }
  }

  const roleOptions = [
    { label: '(none)', value: '' },
    ...roles.map(r => ({ label: r.name, value: r.name })),
  ]

  const columns = [
    {
      title: 'Username', dataIndex: 'username', key: 'username', width: 200,
      render: (name: string, u: User) => (
        <Space size={6}>
          {name}
          {u.is_admin && <Tag color="gold">Admin</Tag>}
        </Space>
      ),
    },
    {
      title: 'Role', key: 'role_name', width: 200,
      render: (_: unknown, u: User) =>
        u.is_admin ? <Tag color="blue">Admin (all access)</Tag> : (
          <Select
            size="small"
            style={{ width: 180 }}
            value={u.role_name || ''}
            options={roleOptions}
            onChange={val => handleRoleChange(u.username, val)}
          />
        ),
    },
    {
      title: 'Actions', key: 'actions', width: 200,
      render: (_: unknown, u: User) => (
        <Space size={4}>
          <Button
            size="small"
            icon={<KeyOutlined />}
            onClick={() => { setReset(u); resetForm.resetFields() }}
          >
            Reset Password
          </Button>
          {!u.is_admin && (
            <Button size="small" danger onClick={() => setDeleteTarget(u)}>Delete</Button>
          )}
        </Space>
      ),
    },
  ]

  return (
    <>
      <div className="page-header">
        <h2>User Management</h2>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => { setCreateOpen(true); form.resetFields() }}>
          Create User
        </Button>
      </div>

      <Table
        rowKey="username"
        size="small"
        loading={loading}
        columns={columns}
        dataSource={users}
      />

      {/* Create user modal */}
      <Modal title="Create User" open={createOpen} onOk={handleCreate} onCancel={() => setCreateOpen(false)}>
        <Form form={form} layout="vertical">
          <Form.Item name="username" label="Username" rules={[{ required: true }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item
            name="password"
            label="Password"
            rules={[{ required: true }, { min: 8, message: 'At least 8 characters' }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="confirm"
            label="Confirm Password"
            dependencies={['password']}
            rules={[
              { required: true, message: 'Please confirm the password' },
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (!value || getFieldValue('password') === value) {
                    return Promise.resolve()
                  }
                  return Promise.reject(new Error('Passwords do not match'))
                },
              }),
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="role_name" label="Role (optional)">
            <Select allowClear placeholder="No role" options={roles.map(r => ({ label: r.name, value: r.name }))} />
          </Form.Item>
        </Form>
      </Modal>

      {/* Reset password modal */}
      <Modal
        title={`Reset password for "${resetTarget?.username}"`}
        open={!!resetTarget}
        onOk={handleReset}
        onCancel={() => setReset(null)}
      >
        <Form form={resetForm} layout="vertical">
          <Form.Item
            name="password"
            label="New Password"
            rules={[{ required: true }, { min: 8, message: 'At least 8 characters' }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="confirm"
            label="Confirm Password"
            dependencies={['password']}
            rules={[
              { required: true, message: 'Please confirm the password' },
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (!value || getFieldValue('password') === value) {
                    return Promise.resolve()
                  }
                  return Promise.reject(new Error('Passwords do not match'))
                },
              }),
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>

      {/* Delete confirmation modal */}
      <DeleteConfirmModal
        open={deleteTarget !== null}
        targetName={deleteTarget?.username ?? ''}
        entityType="User"
        onConfirm={() => handleDelete(deleteTarget!)}
        onCancel={() => setDeleteTarget(null)}
      />
    </>
  )
}

