import { useEffect, useState, useMemo } from 'react'
import { Table, Button, Modal, Form, Input, Space, message, DatePicker, Tag, Popover, Drawer, List, Typography, Alert } from 'antd'
import DeleteConfirmModal from '../components/DeleteConfirmModal'
import LineNumberedEditor from '../components/LineNumberedEditor'
import { PlusOutlined, HistoryOutlined, RollbackOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import * as yaml from 'js-yaml'
import type { Config, OnetimeCommand, ConfigHistory } from '../api'
import {
  listPipelineConfigs, createPipelineConfig, updatePipelineConfig, deletePipelineConfig,
  listInstanceConfigs, createInstanceConfig, updateInstanceConfig, deleteInstanceConfig,
  listOnetimeCommands, createOnetimeCommand, deleteOnetimeCommand,
  listConfigHistory, rollbackConfig, listDeletedConfigs,
} from '../api'
import { useResizableColumns, tableComponents } from '../components/ResizableColumns'
import { usePermission } from '../PermissionContext'

// Parse JSON or YAML input and return pretty-printed JSON string
function toJSON(input: string): string {
  const trimmed = input.trim()
  if (!trimmed) throw new Error('Content is empty')
  // Try JSON first
  try {
    const parsed = JSON.parse(trimmed)
    return JSON.stringify(parsed, null, 2)
  } catch {
    // Not JSON — try YAML and convert to JSON
    const parsed = yaml.load(trimmed)
    if (parsed === undefined) throw new Error('YAML content has no value (comments only?)')
    return JSON.stringify(parsed, null, 2)
  }
}

// Detect format of current content
function detectFormat(input: string): 'json' | 'yaml' | 'unknown' {
  const trimmed = input.trim()
  if (!trimmed) return 'unknown'
  try { JSON.parse(trimmed); return 'json' } catch { /* not json */ }
  try { const r = yaml.load(trimmed); if (r !== undefined) return 'yaml' } catch { /* not yaml */ }
  return 'unknown'
}

// ── Diff utilities ──────────────────────────────────────────────────────────────

type DiffLine = { type: 'equal' | 'add' | 'remove'; value: string }

function computeDiff(oldStr: string, newStr: string): DiffLine[] {
  const a = oldStr.split('\n')
  const b = newStr.split('\n')
  const m = a.length, n = b.length
  const dp: number[][] = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0))
  for (let i = 1; i <= m; i++)
    for (let j = 1; j <= n; j++)
      dp[i][j] = a[i - 1] === b[j - 1] ? dp[i - 1][j - 1] + 1 : Math.max(dp[i - 1][j], dp[i][j - 1])
  const path: DiffLine[] = []
  let i = m, j = n
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && a[i - 1] === b[j - 1]) {
      path.push({ type: 'equal', value: a[i - 1] }); i--; j--
    } else if (j > 0 && (i === 0 || dp[i][j - 1] >= dp[i - 1][j])) {
      path.push({ type: 'add', value: b[j - 1] }); j--
    } else {
      path.push({ type: 'remove', value: a[i - 1] }); i--
    }
  }
  return path.reverse()
}

function formatForDiff(detail: string): string {
  try {
    const parsed = JSON.parse(detail)
    return yaml.dump(parsed, { indent: 2, lineWidth: -1 }).trimEnd()
  } catch {
    return detail.trimEnd()
  }
}

function DiffModal({
  entry,
  currentDetail,
  configType,
  onSuccess,
  onCancel,
}: {
  entry: ConfigHistory
  currentDetail: string
  configType: 'pipeline' | 'instance' | 'onetime'
  onSuccess: () => Promise<void>
  onCancel: () => void
}) {
  const [confirming, setConfirming] = useState(false)
  const isRestoreFromRecycleBin = entry.action === 'delete'
  const isOverride = isRestoreFromRecycleBin && currentDetail !== ''
  const oldFmt = formatForDiff(currentDetail)
  const newFmt = formatForDiff(entry.detail)
  const diffLines = computeDiff(oldFmt, newFmt)
  const hasChanges = diffLines.some(l => l.type !== 'equal')

  const confirm = async () => {
    setConfirming(true)
    try {
      await rollbackConfig(configType, entry.resource_name, entry.id)
      if (isRestoreFromRecycleBin) {
        message.success(`"${entry.resource_name}" restored from Recycle Bin`)
      } else {
        message.success(`Rolled back to version from ${dayjs(entry.changed_at).format('YYYY-MM-DD HH:mm:ss')}`)
      }
      await onSuccess()
    } catch (e) {
      message.error('Restore failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setConfirming(false)
    }
  }

  const title = isOverride
    ? `Restore Preview — Will Override Existing "${entry.resource_name}"`
    : isRestoreFromRecycleBin ? 'Restore Preview' : 'Rollback Preview'

  const btnLabel = isOverride ? 'Override & Restore' : isRestoreFromRecycleBin ? 'Confirm Restore' : 'Confirm Rollback'

  return (
    <Modal
      title={title}
      open
      width={820}
      onCancel={onCancel}
      footer={[
        <Button key="cancel" onClick={onCancel}>Cancel</Button>,
        <Button key="confirm" type="primary" danger={isOverride || !isRestoreFromRecycleBin}
          disabled={isRestoreFromRecycleBin && isOverride && !hasChanges}
          loading={confirming} onClick={confirm}>
          {btnLabel}
        </Button>,
      ]}
    >
      {isOverride && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={<>A config named <strong>{entry.resource_name}</strong> already exists. Restoring will <strong>override</strong> it with the deleted snapshot.</>}
        />
      )}
      <div style={{ marginBottom: 10, fontSize: 13 }}>
        {isOverride
          ? <>Snapshot taken on{' '}
              <strong>{dayjs(entry.changed_at).format('YYYY-MM-DD HH:mm:ss')}</strong>{' '}
              by <strong>{entry.changed_by}</strong></>
          : isRestoreFromRecycleBin
          ? <>Recreating <strong>{entry.resource_name}</strong> from snapshot taken on{' '}
              <strong>{dayjs(entry.changed_at).format('YYYY-MM-DD HH:mm:ss')}</strong>{' '}
              by <strong>{entry.changed_by}</strong></>
          : <>Restoring <strong>{entry.resource_name}</strong> to the version saved on{' '}
              <strong>{dayjs(entry.changed_at).format('YYYY-MM-DD HH:mm:ss')}</strong>{' '}
              by <strong>{entry.changed_by}</strong></>
        }
      </div>
      {!hasChanges ? (
        <Alert message="No changes — this version is identical to the current config." type="info" showIcon />
      ) : (
        <>
          <div style={{ marginBottom: 6, fontSize: 12, color: '#9aa0a6' }}>
            <span style={{ background: 'rgba(242, 139, 130, 0.2)', padding: '1px 8px', borderRadius: 3, marginRight: 10, color: '#f28b82' }}>− will be removed</span>
            <span style={{ background: 'rgba(38, 166, 91, 0.2)', padding: '1px 8px', borderRadius: 3, color: '#81c995' }}>+ will be added</span>
          </div>
          <div style={{
            fontFamily: 'monospace', fontSize: 12.5,
            border: '1px solid #3c4043', borderRadius: 6,
            overflow: 'auto', maxHeight: 480, background: '#202124',
          }}>
            {diffLines.map((line, idx) => (
              <div key={idx} style={{
                display: 'flex', alignItems: 'baseline',
                background: line.type === 'add' ? 'rgba(38, 166, 91, 0.15)' : line.type === 'remove' ? 'rgba(242, 139, 130, 0.15)' : 'transparent',
                padding: '1px 10px', lineHeight: '1.65',
              }}>
                <span style={{
                  color: line.type === 'add' ? '#81c995' : line.type === 'remove' ? '#f28b82' : '#9aa0a6',
                  marginRight: 10, userSelect: 'none', flexShrink: 0, width: '0.9em',
                }}>
                  {line.type === 'add' ? '+' : line.type === 'remove' ? '−' : ' '}
                </span>
                <pre style={{
                  margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all',
                  color: line.type === 'add' ? '#81c995' : line.type === 'remove' ? '#f28b82' : '#e8eaed',
                }}>{line.value}</pre>
              </div>
            ))}
          </div>
        </>
      )}
    </Modal>
  )
}

// ── Pipeline Configs panel ────────────────────────────────────────────────────

function PipelinePanel() {
  const [configs, setConfigs] = useState<Config[]>([])
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<Config | null>(null)
  const [detail, setDetail] = useState('')
  const [selectedRow, setSelectedRow] = useState<string | undefined>()
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const [historyTarget, setHistoryTarget] = useState<string | null>(null)
  const [historyList, setHistoryList] = useState<ConfigHistory[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const [diffEntry, setDiffEntry] = useState<ConfigHistory | null>(null)
  const [recycleOpen, setRecycleOpen] = useState(false)
  const [recycleList, setRecycleList] = useState<ConfigHistory[]>([])
  const [recycleLoading, setRecycleLoading] = useState(false)
  const [recycleDiffEntry, setRecycleDiffEntry] = useState<ConfigHistory | null>(null)
  const [form] = Form.useForm()
  const canCreate = usePermission('pipeline_configs', 'create')
  const canUpdate = usePermission('pipeline_configs', 'update')
  const canDelete = usePermission('pipeline_configs', 'delete')

  const refresh = async () => {
    setLoading(true)
    try { setConfigs(await listPipelineConfigs()) } finally { setLoading(false) }
  }
  useEffect(() => { refresh() }, [])

  const openCreate = () => {
    setEditing(null); setDetail(''); form.resetFields(); setOpen(true)
  }
  const openEdit = (cfg: Config) => {
    setSelectedRow(cfg.name)
    setEditing(cfg)
    let formatted = cfg.detail
    try { formatted = JSON.stringify(JSON.parse(cfg.detail), null, 2) } catch { /* keep as-is */ }
    setDetail(formatted)
    form.setFieldsValue({ name: cfg.name }); setOpen(true)
  }
  const openHistory = async (name: string) => {
    setHistoryTarget(name)
    setHistoryLoading(true)
    try { setHistoryList(await listConfigHistory('pipeline', name)) }
    finally { setHistoryLoading(false) }
  }
  const openRecycleBin = async () => {
    setRecycleOpen(true)
    setRecycleLoading(true)
    try { setRecycleList(await listDeletedConfigs('pipeline')) }
    finally { setRecycleLoading(false) }
  }
  const fmt = useMemo(() => detectFormat(detail), [detail])

  const handleToYaml = () => {
    try {
      let parsed: unknown
      try { parsed = JSON.parse(detail.trim()) }
      catch { parsed = yaml.load(detail.trim()) }
      if (parsed == null) { message.error('Content is empty'); return }
      setDetail(yaml.dump(parsed, { indent: 2, lineWidth: -1 }))
    } catch (e) { message.error('Conversion failed: ' + (e instanceof Error ? e.message : String(e))) }
  }
  const handleToJson = () => {
    try { setDetail(toJSON(detail)) }
    catch (e) { message.error('Conversion failed: ' + (e instanceof Error ? e.message : String(e))) }
  }

  const save = async () => {
    const vals = await form.validateFields()
    let jsonDetail: string
    try {
      jsonDetail = toJSON(detail)
    } catch (e) {
      message.error('Invalid format: ' + (e instanceof Error ? e.message : String(e)))
      return
    }
    if (editing) { await updatePipelineConfig(editing.name, jsonDetail); message.success(`Pipeline Config "${editing.name}" updated`) }
    else { await createPipelineConfig(vals.name, jsonDetail); message.success(`Pipeline Config "${vals.name}" created`) }
    setOpen(false); refresh()
  }
  const del = async (name: string) => {
    await deletePipelineConfig(name)
    message.success(`Pipeline Config "${name}" deleted`)
    setDeleteTarget(null)
    refresh()
  }

  const baseCols = [
    { title: 'Name', dataIndex: 'name', key: 'name', width: 280, ellipsis: true,
      sorter: (a: Config, b: Config) => a.name.localeCompare(b.name),
      defaultSortOrder: 'ascend' as const },
    { title: 'Version', dataIndex: 'version', key: 'version', width: 150,
      sorter: (a: Config, b: Config) => a.version - b.version },
    { title: 'Updated At', dataIndex: 'updated_at', key: 'updated_at', width: 170,
      sorter: (a: Config, b: Config) => a.updated_at.localeCompare(b.updated_at),
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm:ss') },
    {
      title: 'Actions', key: 'act', width: 180,
      render: (_: unknown, r: Config) => (
        <Space size={4} onClick={e => e.stopPropagation()}>
          {canUpdate && <Button size="small" onClick={() => openEdit(r)}>Edit</Button>}
          {canDelete && (
            <Button size="small" danger onClick={() => { setSelectedRow(r.name); setDeleteTarget(r.name) }}>Delete</Button>
          )}
          <Button size="small" icon={<HistoryOutlined />} onClick={() => openHistory(r.name)}>History</Button>
        </Space>
      ),
    },
  ]
  const cols = useResizableColumns(baseCols)

  return (
    <>
      <div className="page-header">
        <h2>Pipeline Configs</h2>
        <Space>
          <Button icon={<RollbackOutlined />} onClick={openRecycleBin}>Recycle Bin</Button>
          {canCreate && <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>Create</Button>}
        </Space>
      </div>
      <Table
        rowKey="name"
        components={tableComponents}
        columns={cols}
        dataSource={configs}
        loading={loading}
        size="small"
        scroll={{ x: 'max-content' }}
        onRow={r => ({
          onClick: () => setSelectedRow(prev => prev === r.name ? undefined : r.name),
          style: { cursor: 'pointer' },
        })}
        rowClassName={r => r.name === selectedRow ? 'row-selected' : ''}
      />
      <Modal title={editing ? <span>Edit Pipeline Config for <span style={{ display: 'inline-block', background: 'rgba(22,119,255,0.12)', color: '#1677ff', borderRadius: 4, padding: '1px 8px', fontWeight: 600, fontFamily: 'monospace', fontSize: '0.92em' }}>{editing.name}</span></span> : 'Create Pipeline Config'} open={open}
        onOk={save} onCancel={() => setOpen(false)} width="90vw" style={{ maxWidth: 1200 }}>
        <Form form={form} layout="vertical">
          {!editing && (
            <Form.Item name="name" label="Name" rules={[{ required: true }]}>
              <Input placeholder="config-name" />
            </Form.Item>
          )}
          <Form.Item label="Detail" style={{ marginBottom: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
              {fmt === 'yaml' && <Tag color="blue">YAML → saved as JSON</Tag>}
              {fmt === 'json' && <Tag color="green">JSON</Tag>}
              {fmt === 'unknown' && detail.trim() && <Tag color="red">Invalid format</Tag>}
              <Button size="small" onClick={handleToYaml} disabled={fmt !== 'json'}>To YAML</Button>
              <Button size="small" onClick={handleToJson} disabled={fmt !== 'yaml'}>To JSON</Button>
            </div>
            <LineNumberedEditor value={detail} onChange={setDetail}
              style={{ height: '60vh', resize: 'vertical' }} />
          </Form.Item>
        </Form>
      </Modal>
      <DeleteConfirmModal
        open={deleteTarget !== null}
        targetName={deleteTarget ?? ''}
        entityType="Pipeline Config"
        onConfirm={() => del(deleteTarget!)}
        onCancel={() => setDeleteTarget(null)}
      />
      <Drawer
        title={`History — Pipeline Config "${historyTarget}"`}
        open={historyTarget !== null}
        onClose={() => setHistoryTarget(null)}
        width={640}
      >
        <List
          loading={historyLoading}
          dataSource={historyList}
          locale={{ emptyText: 'No history yet' }}
          renderItem={item => (
            <List.Item
              actions={[
                <Button size="small" key="rb" onClick={() => setDiffEntry(item)}>Rollback</Button>,
              ]}
            >
              <List.Item.Meta
                title={<span><Tag color={item.action === 'delete' ? 'red' : item.action === 'rollback' ? 'purple' : item.action === 'create' ? 'green' : 'blue'}>{item.action}</Tag>{dayjs(item.changed_at).format('YYYY-MM-DD HH:mm:ss')}</span>}
                description={<Typography.Text type="secondary">by {item.changed_by}{item.version ? ` · v${item.version}` : ''}</Typography.Text>}
              />
            </List.Item>
          )}
        />
      </Drawer>
      {diffEntry && historyTarget && (
        <DiffModal
          entry={diffEntry}
          currentDetail={configs.find(c => c.name === historyTarget)?.detail ?? ''}
          configType="pipeline"
          onSuccess={async () => { setDiffEntry(null); setHistoryTarget(null); await refresh() }}
          onCancel={() => setDiffEntry(null)}
        />
      )}      <Drawer
        title="Recycle Bin — Deleted Pipeline Configs"
        open={recycleOpen}
        onClose={() => setRecycleOpen(false)}
        width={500}
      >
        <List
          loading={recycleLoading}
          dataSource={recycleList}
          locale={{ emptyText: 'No recently deleted pipeline configs' }}
          renderItem={item => {
            const existsNow = configs.some(c => c.name === item.resource_name)
            return (
              <List.Item
                actions={[
                  <Button size="small" type="primary" ghost key="restore" onClick={() => setRecycleDiffEntry(item)}>
                    {existsNow ? 'Override & Restore' : 'Restore'}
                  </Button>
                ]}
              >
                <List.Item.Meta
                  title={
                    <Space size={6}>
                      <strong>{item.resource_name}</strong>
                      {existsNow && <Tag color="warning">Overrides existing</Tag>}
                    </Space>
                  }
                  description={
                    <Typography.Text type="secondary">
                      Deleted {dayjs(item.changed_at).format('YYYY-MM-DD HH:mm:ss')} by {item.changed_by}
                    </Typography.Text>
                  }
                />
              </List.Item>
            )
          }}
        />
      </Drawer>
      {recycleDiffEntry && (
        <DiffModal
          entry={recycleDiffEntry}
          currentDetail={configs.find(c => c.name === recycleDiffEntry.resource_name)?.detail ?? ''}
          configType="pipeline"
          onSuccess={async () => { setRecycleDiffEntry(null); setRecycleOpen(false); await refresh() }}
          onCancel={() => setRecycleDiffEntry(null)}
        />
      )}    </>
  )
}

// ── Instance Configs panel ───────────────────────────────────────────────────

function InstancePanel() {
  const [configs, setConfigs] = useState<Config[]>([])
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<Config | null>(null)
  const [detail, setDetail] = useState('')
  const [selectedRow, setSelectedRow] = useState<string | undefined>()
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const [historyTarget, setHistoryTarget] = useState<string | null>(null)
  const [historyList, setHistoryList] = useState<ConfigHistory[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const [diffEntry, setDiffEntry] = useState<ConfigHistory | null>(null)
  const [recycleOpen, setRecycleOpen] = useState(false)
  const [recycleList, setRecycleList] = useState<ConfigHistory[]>([])
  const [recycleLoading, setRecycleLoading] = useState(false)
  const [recycleDiffEntry, setRecycleDiffEntry] = useState<ConfigHistory | null>(null)
  const [form] = Form.useForm()
  const canCreate = usePermission('instance_configs', 'create')
  const canUpdate = usePermission('instance_configs', 'update')
  const canDelete = usePermission('instance_configs', 'delete')

  const refresh = async () => {
    setLoading(true)
    try { setConfigs(await listInstanceConfigs()) } finally { setLoading(false) }
  }
  useEffect(() => { refresh() }, [])

  const openCreate = () => {
    setEditing(null); setDetail(''); form.resetFields(); setOpen(true)
  }
  const openEdit = (cfg: Config) => {
    setSelectedRow(cfg.name)
    setEditing(cfg)
    let formatted = cfg.detail
    try { formatted = JSON.stringify(JSON.parse(cfg.detail), null, 2) } catch { /* keep as-is */ }
    setDetail(formatted)
    form.setFieldsValue({ name: cfg.name }); setOpen(true)
  }
  const openHistory = async (name: string) => {
    setHistoryTarget(name)
    setHistoryLoading(true)
    try { setHistoryList(await listConfigHistory('instance', name)) }
    finally { setHistoryLoading(false) }
  }
  const openRecycleBin = async () => {
    setRecycleOpen(true)
    setRecycleLoading(true)
    try { setRecycleList(await listDeletedConfigs('instance')) }
    finally { setRecycleLoading(false) }
  }
  const fmt = useMemo(() => detectFormat(detail), [detail])

  const save = async () => {
    const vals = await form.validateFields()
    let jsonDetail: string
    try {
      jsonDetail = toJSON(detail)
    } catch (e) {
      message.error('Invalid format: ' + (e instanceof Error ? e.message : String(e)))
      return
    }
    if (editing) { await updateInstanceConfig(editing.name, jsonDetail); message.success(`Instance Config "${editing.name}" updated`) }
    else { await createInstanceConfig(vals.name, jsonDetail); message.success(`Instance Config "${vals.name}" created`) }
    setOpen(false); refresh()
  }
  const del = async (name: string) => {
    await deleteInstanceConfig(name)
    message.success(`Instance Config "${name}" deleted`)
    setDeleteTarget(null)
    refresh()
  }

  const baseCols = [
    { title: 'Name', dataIndex: 'name', key: 'name', width: 280, ellipsis: true,
      sorter: (a: Config, b: Config) => a.name.localeCompare(b.name),
      defaultSortOrder: 'ascend' as const },
    { title: 'Version', dataIndex: 'version', key: 'version', width: 150,
      sorter: (a: Config, b: Config) => a.version - b.version },
    { title: 'Updated At', dataIndex: 'updated_at', key: 'updated_at', width: 170,
      sorter: (a: Config, b: Config) => a.updated_at.localeCompare(b.updated_at),
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm:ss') },
    {
      title: 'Actions', key: 'act', width: 180,
      render: (_: unknown, r: Config) => (
        <Space size={4} onClick={e => e.stopPropagation()}>
          {canUpdate && <Button size="small" onClick={() => openEdit(r)}>Edit</Button>}
          {canDelete && (
            <Button size="small" danger onClick={() => { setSelectedRow(r.name); setDeleteTarget(r.name) }}>Delete</Button>
          )}
          <Button size="small" icon={<HistoryOutlined />} onClick={() => openHistory(r.name)}>History</Button>
        </Space>
      ),
    },
  ]
  const cols = useResizableColumns(baseCols)

  return (
    <>
      <div className="page-header">
        <h2>Instance Configs</h2>
        <Space>
          <Button icon={<RollbackOutlined />} onClick={openRecycleBin}>Recycle Bin</Button>
          {canCreate && <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>Create</Button>}
        </Space>
      </div>
      <Table
        rowKey="name"
        components={tableComponents}
        columns={cols}
        dataSource={configs}
        loading={loading}
        size="small"
        scroll={{ x: 'max-content' }}
        onRow={r => ({
          onClick: () => setSelectedRow(prev => prev === r.name ? undefined : r.name),
          style: { cursor: 'pointer' },
        })}
        rowClassName={r => r.name === selectedRow ? 'row-selected' : ''}
      />
      <Modal title={editing ? <span>Edit Instance Config for <span style={{ display: 'inline-block', background: 'rgba(22,119,255,0.12)', color: '#1677ff', borderRadius: 4, padding: '1px 8px', fontWeight: 600, fontFamily: 'monospace', fontSize: '0.92em' }}>{editing.name}</span></span> : 'Create Instance Config'} open={open}
        onOk={save} onCancel={() => setOpen(false)} width="90vw" style={{ maxWidth: 1200 }}>
        <Form form={form} layout="vertical">
          {!editing && (
            <Form.Item name="name" label="Name" rules={[{ required: true }]}>
              <Input placeholder="instance-config-name" />
            </Form.Item>
          )}
          <Form.Item
            label={
              <span>
                Detail
                {' '}
                {fmt === 'yaml' && <Tag color="blue" style={{ marginLeft: 4 }}>YAML → saved as JSON</Tag>}
                {fmt === 'json' && <Tag color="green" style={{ marginLeft: 4 }}>JSON</Tag>}
                {fmt === 'unknown' && detail.trim() && <Tag color="red" style={{ marginLeft: 4 }}>Invalid format</Tag>}
              </span>
            }
            style={{ marginBottom: 0 }}
          >
            <LineNumberedEditor value={detail} onChange={setDetail}
              style={{ height: '60vh', resize: 'vertical' }} />
          </Form.Item>
        </Form>
      </Modal>
      <DeleteConfirmModal
        open={deleteTarget !== null}
        targetName={deleteTarget ?? ''}
        entityType="Instance Config"
        onConfirm={() => del(deleteTarget!)}
        onCancel={() => setDeleteTarget(null)}
      />
      <Drawer
        title={`History — Instance Config "${historyTarget}"`}
        open={historyTarget !== null}
        onClose={() => setHistoryTarget(null)}
        width={640}
      >
        <List
          loading={historyLoading}
          dataSource={historyList}
          locale={{ emptyText: 'No history yet' }}
          renderItem={item => (
            <List.Item
              actions={[
                <Button size="small" key="rb" onClick={() => setDiffEntry(item)}>Rollback</Button>,
              ]}
            >
              <List.Item.Meta
                title={<span><Tag color={item.action === 'delete' ? 'red' : item.action === 'rollback' ? 'purple' : item.action === 'create' ? 'green' : 'blue'}>{item.action}</Tag>{dayjs(item.changed_at).format('YYYY-MM-DD HH:mm:ss')}</span>}
                description={<Typography.Text type="secondary">by {item.changed_by}{item.version ? ` · v${item.version}` : ''}</Typography.Text>}
              />
            </List.Item>
          )}
        />
      </Drawer>
      {diffEntry && historyTarget && (
        <DiffModal
          entry={diffEntry}
          currentDetail={configs.find(c => c.name === historyTarget)?.detail ?? ''}
          configType="instance"
          onSuccess={async () => { setDiffEntry(null); setHistoryTarget(null); await refresh() }}
          onCancel={() => setDiffEntry(null)}
        />
      )}
      <Drawer
        title="Recycle Bin — Deleted Instance Configs"
        open={recycleOpen}
        onClose={() => setRecycleOpen(false)}
        width={500}
      >
        <List
          loading={recycleLoading}
          dataSource={recycleList}
          locale={{ emptyText: 'No recently deleted instance configs' }}
          renderItem={item => {
            const existsNow = configs.some(c => c.name === item.resource_name)
            return (
              <List.Item
                actions={[
                  <Button size="small" type="primary" ghost key="restore" onClick={() => setRecycleDiffEntry(item)}>
                    {existsNow ? 'Override & Restore' : 'Restore'}
                  </Button>
                ]}
              >
                <List.Item.Meta
                  title={
                    <Space size={6}>
                      <strong>{item.resource_name}</strong>
                      {existsNow && <Tag color="warning">Overrides existing</Tag>}
                    </Space>
                  }
                  description={
                    <Typography.Text type="secondary">
                      Deleted {dayjs(item.changed_at).format('YYYY-MM-DD HH:mm:ss')} by {item.changed_by}
                    </Typography.Text>
                  }
                />
              </List.Item>
            )
          }}
        />
      </Drawer>
      {recycleDiffEntry && (
        <DiffModal
          entry={recycleDiffEntry}
          currentDetail={configs.find(c => c.name === recycleDiffEntry.resource_name)?.detail ?? ''}
          configType="instance"
          onSuccess={async () => { setRecycleDiffEntry(null); setRecycleOpen(false); await refresh() }}
          onCancel={() => setRecycleDiffEntry(null)}
        />
      )}
    </>
  )
}

// ── Onetime Commands panel ─────────────────────────────────────────────────

function OnetimePanel() {
  const [cmds, setCmds] = useState<OnetimeCommand[]>([])
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const [detail, setDetail] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const [selectedRow, setSelectedRow] = useState<string | undefined>()
  const [historyTarget, setHistoryTarget] = useState<string | null>(null)
  const [historyList, setHistoryList] = useState<ConfigHistory[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const [restoreEntry, setRestoreEntry] = useState<ConfigHistory | null>(null)
  const [restoreConfirming, setRestoreConfirming] = useState(false)
  const [recycleOpen, setRecycleOpen] = useState(false)
  const [recycleList, setRecycleList] = useState<ConfigHistory[]>([])
  const [recycleLoading, setRecycleLoading] = useState(false)
  const [form] = Form.useForm()
  const canCreate = usePermission('onetime_commands', 'create')
  const canDelete = usePermission('onetime_commands', 'delete')

  const refresh = async () => {
    setLoading(true)
    try { setCmds(await listOnetimeCommands()) } finally { setLoading(false) }
  }
  useEffect(() => { refresh() }, [])

  const fmt = useMemo(() => detectFormat(detail), [detail])

  const openHistory = async (name: string) => {
    setHistoryTarget(name)
    setHistoryLoading(true)
    try { setHistoryList(await listConfigHistory('onetime', name)) }
    finally { setHistoryLoading(false) }
  }
  const openRecycleBin = async () => {
    setRecycleOpen(true)
    setRecycleLoading(true)
    try { setRecycleList(await listDeletedConfigs('onetime')) }
    finally { setRecycleLoading(false) }
  }

  const save = async () => {
    const vals = await form.validateFields()
    let jsonDetail: string
    try {
      jsonDetail = toJSON(detail)
    } catch (e) {
      message.error('Invalid format: ' + (e instanceof Error ? e.message : String(e)))
      return
    }
    const expireTs = vals.expire_time ? (vals.expire_time as dayjs.Dayjs).unix() : 0
    await createOnetimeCommand(vals.name, jsonDetail, expireTs)
    message.success(`Onetime Command "${vals.name}" created`)
    setOpen(false)
    form.resetFields()
    setDetail('')
    refresh()
  }

  const del = async (name: string) => {
    await deleteOnetimeCommand(name)
    message.success(`Onetime Command "${name}" deleted`)
    setDeleteTarget(null)
    refresh()
  }

  const formatDetail = (v: string) => {
    try { return JSON.stringify(JSON.parse(v), null, 2) } catch { return v }
  }

  const now = Date.now() / 1000
  const baseCols = [
    { title: 'Name', dataIndex: 'name', key: 'name', width: 180, ellipsis: true,
      sorter: (a: OnetimeCommand, b: OnetimeCommand) => a.name.localeCompare(b.name),
      defaultSortOrder: 'ascend' as const },
    {
      title: (
        <span>
          Detail
          <hr style={{ margin: '4px 0 2px', border: 'none', borderTop: '1px solid rgba(128,128,128,0.25)' }} />
          <span style={{ fontWeight: 400, fontSize: 10, opacity: 0.6, fontStyle: 'italic', letterSpacing: '0.02em' }}>command payload</span>
        </span>
      ),
      dataIndex: 'detail', key: 'detail',
      width: 300, ellipsis: true,
      render: (v: string) => {
        if (!v) return <span style={{ color: '#999' }}>-</span>
        const preview = v.length > 80 ? v.slice(0, 80) + '…' : v
        return (
          <Popover
            title="Detail (command payload)"
            content={
              <pre style={{ maxWidth: 560, maxHeight: 400, overflow: 'auto', margin: 0, fontSize: 12 }}>
                {formatDetail(v)}
              </pre>
            }
            trigger="hover"
          >
            <code style={{
              cursor: 'default', fontSize: 11, color: '#1677ff',
              background: 'rgba(22,119,255,0.08)',
              padding: '2px 6px', borderRadius: 3,
              fontFamily: 'monospace', whiteSpace: 'nowrap',
            }}>{preview}</code>
          </Popover>
        )
      },
    },
    {
      title: 'Expires', dataIndex: 'expire_time', key: 'expire_time', width: 170,
      sorter: (a: OnetimeCommand, b: OnetimeCommand) => (a.expire_time ?? 0) - (b.expire_time ?? 0),
      render: (v: number) => {
        if (!v) return <Tag>Never</Tag>
        const expired = v < now
        return (
          <Tag color={expired ? 'red' : 'green'}>
            {dayjs.unix(v).format('YYYY-MM-DD HH:mm')}
            {expired ? ' (expired)' : ''}
          </Tag>
        )
      },
    },
    { title: 'Created At', dataIndex: 'created_at', key: 'created_at', width: 170,
      sorter: (a: OnetimeCommand, b: OnetimeCommand) => a.created_at.localeCompare(b.created_at),
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm:ss') },
    {
      title: 'Actions', key: 'act', width: 140,
      render: (_: unknown, r: OnetimeCommand) => (
        <Space size={4} onClick={e => e.stopPropagation()}>
          {canDelete && (
            <Button size="small" danger onClick={() => { setSelectedRow(r.name); setDeleteTarget(r.name) }}>Delete</Button>
          )}
          <Button size="small" icon={<HistoryOutlined />} onClick={() => openHistory(r.name)}>History</Button>
        </Space>
      ),
    },
  ]
  const cols = useResizableColumns(baseCols)

  return (
    <>
      <div className="page-header">
        <h2>Onetime Commands</h2>
        <Space>
          <Button icon={<RollbackOutlined />} onClick={openRecycleBin}>Recycle Bin</Button>
          {canCreate && <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>Create</Button>}
        </Space>
      </div>
      <Table
        rowKey="name"
        components={tableComponents}
        columns={cols}
        dataSource={cmds}
        loading={loading}
        size="small"
        scroll={{ x: 'max-content' }}
        onRow={r => ({
          onClick: () => setSelectedRow(prev => prev === r.name ? undefined : r.name),
          style: { cursor: 'pointer' },
        })}
        rowClassName={r => r.name === selectedRow ? 'row-selected' : ''}
      />
      <Modal title="Create Onetime Command" open={open}
        onOk={save} onCancel={() => { setOpen(false); form.resetFields(); setDetail('') }}
        width="90vw" style={{ maxWidth: 900 }}>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input placeholder="command-name" />
          </Form.Item>
          <Form.Item name="expire_time" label="Expire Time (optional)">
            <DatePicker showTime format="YYYY-MM-DD HH:mm:ss" style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item
            label={
              <span>
                Detail (command payload)
                {fmt === 'yaml' && <Tag color="blue" style={{ marginLeft: 8 }}>YAML → saved as JSON</Tag>}
                {fmt === 'json' && <Tag color="green" style={{ marginLeft: 8 }}>JSON</Tag>}
                {fmt === 'unknown' && detail.trim() && <Tag color="red" style={{ marginLeft: 8 }}>Invalid format</Tag>}
              </span>
            }
            style={{ marginBottom: 0 }}
          >
            <LineNumberedEditor value={detail} onChange={setDetail}
              style={{ height: '40vh', resize: 'vertical' }} />
          </Form.Item>
        </Form>
      </Modal>
      <DeleteConfirmModal
        open={deleteTarget !== null}
        targetName={deleteTarget ?? ''}
        entityType="Onetime Command"
        onConfirm={() => del(deleteTarget!)}
        onCancel={() => setDeleteTarget(null)}
      />
      <Drawer
        title={`History — Onetime Command "${historyTarget}"`}
        open={historyTarget !== null}
        onClose={() => setHistoryTarget(null)}
        width={640}
      >
        <List
          loading={historyLoading}
          dataSource={historyList}
          locale={{ emptyText: 'No history yet' }}
          renderItem={item => (
            <List.Item
              actions={item.action === 'delete' ? [
                <Button size="small" key="rb" onClick={() => setRestoreEntry(item)}>Restore</Button>
              ] : []}
            >
              <List.Item.Meta
                title={<span><Tag color={item.action === 'delete' ? 'red' : item.action === 'create' ? 'green' : 'blue'}>{item.action}</Tag>{dayjs(item.changed_at).format('YYYY-MM-DD HH:mm:ss')}</span>}
                description={<Typography.Text type="secondary">by {item.changed_by}</Typography.Text>}
              />
            </List.Item>
          )}
        />
      </Drawer>
      <Drawer
        title="Recycle Bin — Deleted Onetime Commands"
        open={recycleOpen}
        onClose={() => setRecycleOpen(false)}
        width={500}
      >
        <List
          loading={recycleLoading}
          dataSource={recycleList}
          locale={{ emptyText: 'No recently deleted onetime commands' }}
          renderItem={item => {
            let snapDetail = item.detail
            let snapExpire = 0
            try { const s = JSON.parse(item.detail); snapDetail = s.detail ?? item.detail; snapExpire = s.expire_time ?? 0 } catch { /* ignore */ }
            const existsNow = cmds.some(c => c.name === item.resource_name)
            return (
              <List.Item
                actions={[
                  <Button size="small" type="primary" ghost key="restore" onClick={() => setRestoreEntry(item)}>
                    {existsNow ? 'Override & Restore' : 'Restore'}
                  </Button>
                ]}
              >
                <List.Item.Meta
                  title={
                    <Space size={6}>
                      <strong>{item.resource_name}</strong>
                      {existsNow && <Tag color="warning">Overrides existing</Tag>}
                    </Space>
                  }
                  description={
                    <Typography.Text type="secondary">
                      Deleted {dayjs(item.changed_at).format('YYYY-MM-DD HH:mm:ss')} by {item.changed_by}
                      {snapExpire ? ` · expires ${dayjs.unix(snapExpire).format('YYYY-MM-DD HH:mm')}` : ''}
                      {snapDetail ? <><br /><code style={{ fontSize: 11 }}>{snapDetail.length > 60 ? snapDetail.slice(0, 60) + '…' : snapDetail}</code></> : null}
                    </Typography.Text>
                  }
                />
              </List.Item>
            )
          }}
        />
      </Drawer>
      {restoreEntry && (() => {
        let snapDetail = restoreEntry.detail
        let snapExpire = 0
        try { const s = JSON.parse(restoreEntry.detail); snapDetail = s.detail ?? restoreEntry.detail; snapExpire = s.expire_time ?? 0 } catch { /* ignore */ }
        const existsNow = cmds.some(c => c.name === restoreEntry.resource_name)
        const doRestore = async () => {
          setRestoreConfirming(true)
          try {
            await rollbackConfig('onetime', restoreEntry.resource_name, restoreEntry.id)
            message.success(`Onetime Command "${restoreEntry.resource_name}" ${existsNow ? 'overridden and restored' : 'restored'}`)
            setRestoreEntry(null)
            setHistoryTarget(null)
            setRecycleOpen(false)
            refresh()
          } catch (e) {
            message.error('Restore failed: ' + (e instanceof Error ? e.message : String(e)))
          } finally { setRestoreConfirming(false) }
        }
        return (
          <Modal
            title={`${existsNow ? 'Override & Restore' : 'Restore'} Onetime Command "${restoreEntry.resource_name}"`}
            open
            onCancel={() => setRestoreEntry(null)}
            onOk={doRestore}
            okText={existsNow ? 'Override & Restore' : 'Restore'}
            okButtonProps={{ danger: existsNow }}
            confirmLoading={restoreConfirming}
          >
            {existsNow && (
              <Alert
                type="warning"
                showIcon
                style={{ marginBottom: 12 }}
                message={<>A command named <strong>{restoreEntry.resource_name}</strong> already exists and will be <strong>replaced</strong> with the deleted snapshot.</>}
              />
            )}
            <Typography.Text type="secondary">
              Deleted {dayjs(restoreEntry.changed_at).format('YYYY-MM-DD HH:mm:ss')} by {restoreEntry.changed_by}
              {snapExpire ? ` · expires ${dayjs.unix(snapExpire).format('YYYY-MM-DD HH:mm')}` : ''}
            </Typography.Text>
            <pre style={{ marginTop: 8, fontFamily: 'monospace', fontSize: 12, maxHeight: 300, overflow: 'auto', background: '#202124', border: '1px solid #3c4043', borderRadius: 6, padding: 10, color: '#e8eaed' }}>
              {snapDetail || '(empty payload)'}
            </pre>
          </Modal>
        )
      })()}
    </>
  )
}

// ── Main export ───────────────────────────────────────────────────────────────

export default function ConfigsPage({ tab }: { tab: 'pipeline' | 'instance' | 'onetime' }) {
  return (
    <div>
      <div style={{ display: tab === 'pipeline' ? 'block' : 'none' }}><PipelinePanel /></div>
      <div style={{ display: tab === 'instance' ? 'block' : 'none' }}><InstancePanel /></div>
      <div style={{ display: tab === 'onetime' ? 'block' : 'none' }}><OnetimePanel /></div>
    </div>
  )
}
