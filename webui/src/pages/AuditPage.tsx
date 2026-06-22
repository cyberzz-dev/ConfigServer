import { useEffect, useState } from 'react'
import { Table, Tag, Tooltip } from 'antd'
import dayjs from 'dayjs'
import type { AuditLog } from '../api'
import { listAuditLogs } from '../api'

const ACTION_CLASS: Record<string, string> = {
  create: 'action-create',
  update: 'action-update',
  delete: 'action-delete',
  rollback: 'action-rollback',
}

const TYPE_CLASS: Record<string, string> = {
  pipeline: 'type-pipeline',
  instance: 'type-instance',
  onetime: 'type-onetime',
  group: 'type-group',
}

export default function AuditPage() {
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(50)

  const refresh = async (p = page, ps = pageSize) => {
    setLoading(true)
    try {
      const res = await listAuditLogs(p, ps)
      setLogs(res?.logs ?? [])
      setTotal(res?.total ?? 0)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { refresh() }, [page, pageSize])

  const columns = [
    {
      title: 'Time', dataIndex: 'created_at', key: 'created_at', width: 170,
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm:ss'),
    },
    { title: 'User', dataIndex: 'username', key: 'username', width: 130 },
    {
      title: 'Action', dataIndex: 'action', key: 'action', width: 100,
      render: (v: string) => <Tag className={ACTION_CLASS[v] ?? 'action-unknown'}>{v || 'unknown'}</Tag>,
    },
    {
      title: 'Resource Type', dataIndex: 'resource_type', key: 'resource_type', width: 130,
      render: (v: string) => <Tag className={TYPE_CLASS[v] ?? 'type-unknown'}>{v || 'unknown'}</Tag>,
    },
    { title: 'Resource Name', dataIndex: 'resource_name', key: 'resource_name', width: 220, ellipsis: true },
    {
      title: 'Detail', dataIndex: 'detail', key: 'detail', width: 360,
      render: (v: string) => {
        if (!v) return '—'
        const lines = v.split('\n')
        const firstLine = lines[0]
        const hasMore = lines.length > 1
        return (
          <Tooltip
            title={<pre style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: 320, overflowY: 'auto', fontSize: 12 }}>{v}</pre>}
            overlayStyle={{ maxWidth: 540 }}
            overlayInnerStyle={{ padding: '8px 10px' }}
          >
            <span style={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', cursor: hasMore ? 'help' : 'default' }}>
              {firstLine}{hasMore ? ' …' : ''}
            </span>
          </Tooltip>
        )
      },
    },
    { title: 'Client IP', dataIndex: 'client_ip', key: 'client_ip', width: 140 },
  ]

  return (
    <>
      <div className="page-header">
        <h2>Audit Log</h2>
      </div>
      <Table
        rowKey="id"
        columns={columns}
        dataSource={logs}
        loading={loading}
        size="small"
        scroll={{ x: 1300 }}
        pagination={{
          current: page,
          pageSize,
          total,
          showSizeChanger: true,
          showTotal: t => `${t} records`,
          pageSizeOptions: ['20', '50', '100'],
          onChange: (p, ps) => {
            setPage(p)
            setPageSize(ps)
          },
        }}
      />
    </>
  )
}
