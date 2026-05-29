import { useEffect, useState, useRef, useCallback } from 'react'
import { Table, Tag, Typography, Button, Descriptions, Spin, Input, Space, Tooltip } from 'antd'
import { ReloadOutlined, SearchOutlined, QuestionCircleOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import type { Agent, AgentConfigStatus } from '../api'
import { listAgents, getAgentDetail } from '../api'
import { useResizableColumns, tableComponents } from '../components/ResizableColumns'

const { Text } = Typography

const STATUS_LABEL: Record<number, string> = {
  0: 'UNKNOWN',
  1: 'APPLYING',
  2: 'APPLIED',
  3: 'FAILED',
}

const STATUS_CLASS: Record<number, string> = {
  1: 'status-applying',
  2: 'status-applied',
  3: 'status-failed',
}

const TYPE_COLORS: Record<string, string> = {
  pipeline: 'geekblue',
  instance: 'cyan',
  onetime:  'orange',
}

function ConfigStatusTable({ instanceID }: { instanceID: string }) {
  const [data, setData] = useState<AgentConfigStatus[] | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const load = useCallback(async () => {
    setRefreshing(true)
    try {
      const r = await getAgentDetail(instanceID)
      const statuses = r?.config_statuses ?? []
      setData([...statuses].sort((a, b) => a.ConfigName.localeCompare(b.ConfigName)))
    } finally { setRefreshing(false) }
  }, [instanceID])

  useEffect(() => { load() }, [load])

  if (!data) return <Spin size="small" />

  const cols = [
    { title: 'Config', dataIndex: 'ConfigName', key: 'ConfigName' },
    { title: 'Type', dataIndex: 'ConfigType', key: 'ConfigType', width: 110,
      render: (v: string) => <Tag color={TYPE_COLORS[v] ?? 'default'}>{v || 'unknown'}</Tag> },

    {
      title: (
        <Space size={4}>
          Status
          <Tooltip
            overlayInnerStyle={{ minWidth: 320 }}
            title={
            <div style={{ fontSize: 12, lineHeight: '1.8', whiteSpace: 'nowrap' }}>
              <b>APPLYING</b> — Config is being applied<br />
              <b>APPLIED</b> — Config applied successfully<br />
              <b>FAILED</b> — Config failed to apply (see Message)<br />
              <b>UNKNOWN</b> — Status not yet reported
            </div>
          }>
            <QuestionCircleOutlined style={{ color: '#8c8c8c', cursor: 'help' }} />
          </Tooltip>
        </Space>
      ),
      dataIndex: 'Status', key: 'Status', width: 130,
      render: (v: number) => (
        <Tag className={STATUS_CLASS[v] ?? 'status-unknown'}>{v} ({STATUS_LABEL[v] ?? 'UNKNOWN'})</Tag>
      ),
    },
    { title: 'Message', dataIndex: 'Message', key: 'Message' },
    { title: 'Updated At', dataIndex: 'UpdatedAt', key: 'UpdatedAt', width: 170,
      render: (v: string) => (!v || v.startsWith('0001-')) ? '—' : dayjs(v).format('YYYY-MM-DD HH:mm:ss') },
  ]

  const header = (
    <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 6, color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: 4 }}>
      Config Statuses
      <Button size="small" type="text" icon={<ReloadOutlined spin={refreshing} />}
        onClick={load} style={{ color: 'var(--text-secondary)' }} />
    </div>
  )

  if (data.length === 0) {
    return <>
      {header}
      <Text type="secondary" style={{ fontSize: 13 }}>No config statuses reported yet.</Text>
    </>
  }

  return (
    <>
      {header}
      <Table
        rowKey="ConfigName"
        columns={cols}
        dataSource={data}
        size="small"
        pagination={false}
        style={{ margin: '0 0 8px' }}
      />
    </>
  )
}

export default function AgentsPage() {
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(false)
  const [selectedRow, setSelectedRow] = useState<string | undefined>()
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(50)
  const [search, setSearch] = useState('')
  const [searchInput, setSearchInput] = useState('')
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const refresh = useCallback(async (p = page, ps = pageSize, s = search) => {
    setLoading(true)
    try {
      const result = await listAgents({ page: p, pageSize: ps, search: s })
      setAgents(result?.agents ?? [])
      setTotal(result?.total ?? 0)
    } finally { setLoading(false) }
  }, [page, pageSize, search])

  useEffect(() => {
    refresh(page, pageSize, search)
    timerRef.current = setInterval(() => refresh(page, pageSize, search), 30_000)
    return () => { if (timerRef.current) clearInterval(timerRef.current) }
  }, [page, pageSize, search])

  const baseColumns = [
    { title: 'Instance ID', dataIndex: 'InstanceID', key: 'InstanceID', width: 280, ellipsis: true,
      sorter: (a: Agent, b: Agent) => a.InstanceID.localeCompare(b.InstanceID),
      render: (v: string) => <Text copyable code style={{ fontSize: 12 }}>{v}</Text> },
    { title: 'Type', dataIndex: 'AgentType', key: 'AgentType', width: 100,
      sorter: (a: Agent, b: Agent) => a.AgentType.localeCompare(b.AgentType) },
    { title: 'IP', dataIndex: 'IP', key: 'IP', width: 130,
      sorter: (a: Agent, b: Agent) => a.IP.localeCompare(b.IP) },
    { title: 'Hostname', dataIndex: 'Hostname', key: 'Hostname', width: 160, ellipsis: true,
      sorter: (a: Agent, b: Agent) => a.Hostname.localeCompare(b.Hostname) },
    { title: 'Version', dataIndex: 'Version', key: 'Version', width: 100,
      sorter: (a: Agent, b: Agent) => a.Version.localeCompare(b.Version) },
    {
      title: 'Status', dataIndex: 'RunningStatus', key: 'RunningStatus', width: 100,
      sorter: (a: Agent, b: Agent) => a.RunningStatus.localeCompare(b.RunningStatus),
      render: (v: string) => (
        <Tag color={v === 'running' ? 'success' : 'default'}>{v || 'unknown'}</Tag>
      ),
    },
    { title: 'Last Heartbeat', dataIndex: 'LastHeartbeat', key: 'LastHeartbeat', width: 170,
      sorter: (a: Agent, b: Agent) => a.LastHeartbeat.localeCompare(b.LastHeartbeat),
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm:ss') },
  ]
  const columns = useResizableColumns(baseColumns)

  const handleSearch = () => {
    setSearch(searchInput)
    setPage(1)
  }

  const handleTableChange = (pagination: { current?: number; pageSize?: number }) => {
    const newPage = pagination.current ?? 1
    const newPageSize = pagination.pageSize ?? pageSize
    setPage(newPage)
    setPageSize(newPageSize)
  }

  return (
    <>
      <div className="page-header">
        <h2>Agents</h2>
        <Space>
          <Input
            placeholder="Search by ID / hostname / IP / version"
            prefix={<SearchOutlined style={{ color: '#aaa' }} />}
            value={searchInput}
            onChange={e => setSearchInput(e.target.value)}
            onPressEnter={handleSearch}
            allowClear
            onClear={() => { setSearchInput(''); setSearch(''); setPage(1) }}
            style={{ width: 280 }}
          />
          <Button icon={<SearchOutlined />} onClick={handleSearch}>Search</Button>
          <Button icon={<ReloadOutlined />} onClick={() => refresh(page, pageSize, search)} loading={loading}>Refresh</Button>
        </Space>
      </div>
      <Table
        rowKey="InstanceID"
        components={tableComponents}
        columns={columns}
        dataSource={agents}
        loading={loading}
        size="small"
        scroll={{ x: 'max-content' }}
        pagination={{
          current: page,
          pageSize,
          total,
          showSizeChanger: true,
          pageSizeOptions: ['20', '50', '100', '200'],
          showTotal: (t, range) => `${range[0]}-${range[1]} / ${t}`,
        }}
        onChange={handleTableChange}
        onRow={r => ({
          onClick: () => setSelectedRow(prev => prev === r.InstanceID ? undefined : r.InstanceID),
          style: { cursor: 'pointer' },
        })}
        rowClassName={r => r.InstanceID === selectedRow ? 'row-selected' : ''}
        expandable={{
          expandedRowRender: (record: Agent) => (
            <div style={{ padding: '8px 16px', background: 'var(--bg-elevated)', borderRadius: 6 }}>
              <Descriptions size="small" column={3} style={{ marginBottom: 12 }}>
                <Descriptions.Item label="Instance ID">
                  <Text code copyable style={{ fontSize: 12 }}>{record.InstanceID}</Text>
                </Descriptions.Item>
                <Descriptions.Item label="Host ID">{(record as Agent & { Hostid?: string }).Hostid || '—'}</Descriptions.Item>
                <Descriptions.Item label="Agent Type">{record.AgentType}</Descriptions.Item>
              </Descriptions>
              <ConfigStatusTable instanceID={record.InstanceID} />
            </div>
          ),
        }}
      />
    </>
  )
}
