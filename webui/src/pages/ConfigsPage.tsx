import { useEffect, useState, useMemo, type CSSProperties } from 'react'
import { Table, Button, Modal, Form, Input, Space, message, DatePicker, Tag, Drawer, List, Typography, Alert, Segmented } from 'antd'
import DeleteConfirmModal from '../components/DeleteConfirmModal'
import LineNumberedEditor from '../components/LineNumberedEditor'
import { CopyOutlined, EyeOutlined, PlusOutlined, HistoryOutlined, RollbackOutlined } from '@ant-design/icons'
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

function formatDetail(v: string) {
  try { return JSON.stringify(JSON.parse(v), null, 2) } catch { return v }
}

function toYaml(input: string): string {
  let parsed: unknown
  try { parsed = JSON.parse(input.trim()) }
  catch { parsed = yaml.load(input.trim()) }
  if (parsed == null) throw new Error('Content is empty')
  return yaml.dump(parsed, { indent: 2, lineWidth: -1 })
}

async function copyText(text: string) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
    } else {
      const textarea = document.createElement('textarea')
      textarea.value = text
      textarea.style.position = 'fixed'
      textarea.style.left = '-9999px'
      document.body.appendChild(textarea)
      textarea.focus()
      textarea.select()
      document.execCommand('copy')
      document.body.removeChild(textarea)
    }
    message.success('Copied')
  } catch (e) {
    message.error('Copy failed: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function parseExpireDuration(input?: string): number {
  const trimmed = input?.trim()
  if (!trimmed) return 0
  const match = trimmed.match(/^(\d+)\s*(min|h|d|w|m)$/i)
  if (!match) throw new Error('Use duration like 60min, 6h, 6d, 6w, or 6m')
  const amount = Number(match[1])
  if (!Number.isSafeInteger(amount) || amount <= 0) throw new Error('Duration must be a positive integer')
  const unit = match[2].toLowerCase()
  switch (unit) {
    case 'min': return dayjs().add(amount, 'minute').unix()
    case 'h': return dayjs().add(amount, 'hour').unix()
    case 'd': return dayjs().add(amount, 'day').unix()
    case 'w': return dayjs().add(amount, 'week').unix()
    case 'm': return dayjs().add(amount, 'month').unix()
    default: throw new Error('Unsupported duration unit')
  }
}

function ReadonlyPayloadViewer({ value, style }: { value: string; style?: CSSProperties }) {
  const lines = (value || '(empty payload)').split('\n')
  const lineNoWidth = Math.max(String(lines.length).length * 9, 24) + 18
  return (
    <div style={{
      maxHeight: '60vh', overflow: 'auto', margin: 0, fontSize: 12,
      background: 'var(--input-bg)', border: '1px solid var(--input-border)', borderRadius: 6,
      color: 'var(--text-primary)', fontFamily: 'monospace', lineHeight: '20px',
      ...style,
    }}>
      {lines.map((line, index) => (
        <div key={index} style={{ display: 'flex', minWidth: 'max-content' }}>
          <span style={{
            width: lineNoWidth, flexShrink: 0, boxSizing: 'border-box',
            padding: '0 8px 0 6px', textAlign: 'right', userSelect: 'none',
            color: 'var(--text-tertiary)', background: 'var(--bg-elevated)', borderRight: '1px solid var(--border-color)',
          }}>{index + 1}</span>
          <span style={{ whiteSpace: 'pre', padding: '0 8px' }}>{line || ' '}</span>
        </div>
      ))}
    </div>
  )
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
  const [viewTarget, setViewTarget] = useState<Config | null>(null)
  const [viewDetail, setViewDetail] = useState('')
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
  const openView = (cfg: Config) => {
    setSelectedRow(cfg.name)
    setViewTarget(cfg)
    setViewDetail(formatDetail(cfg.detail))
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
  const viewFmt = useMemo(() => detectFormat(viewDetail), [viewDetail])

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
  const handleViewToYaml = () => {
    try { setViewDetail(toYaml(viewDetail)) }
    catch (e) { message.error('Conversion failed: ' + (e instanceof Error ? e.message : String(e))) }
  }
  const handleViewToJson = () => {
    try { setViewDetail(toJSON(viewDetail)) }
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
      title: 'Actions', key: 'act', width: 250,
      render: (_: unknown, r: Config) => (
        <Space size={4} onClick={e => e.stopPropagation()}>
          <Button size="small" icon={<EyeOutlined />} onClick={() => openView(r)}>View</Button>
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
      <Modal title={editing ? `Edit Pipeline Config — ${editing.name}` : 'Create Pipeline Config'} open={open}
        onOk={save} onCancel={() => setOpen(false)} width="90vw" style={{ maxWidth: 1200 }}>
        <Form form={form} layout="vertical">
          {!editing && (
            <Form.Item name="name" label="Name" rules={[
              { required: true },
              { pattern: /^[a-zA-Z0-9_][a-zA-Z0-9_-]*$/, message: 'Name must start with a letter, digit, or _ and only contain letters, digits, - and _' },
            ]}>
              <Input placeholder="config-name" />
            </Form.Item>
          )}
          <Form.Item style={{ marginBottom: 0 }}>
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
              <Space wrap>
                {editing && <Tag color="blue">{editing.name}</Tag>}
                {editing && <Tag>v{editing.version}</Tag>}
                {(fmt === 'yaml' || fmt === 'json') && <Tag color="green">JSON</Tag>}
                {fmt === 'unknown' && detail.trim() && <Tag color="red">Invalid format</Tag>}
                <Button size="small" onClick={handleToYaml} disabled={fmt !== 'json'}>JSON to YAML</Button>
                <Button size="small" onClick={handleToJson} disabled={fmt !== 'yaml'}>YAML to JSON</Button>
              </Space>
              <LineNumberedEditor value={detail} onChange={setDetail}
                style={{ height: '60vh', resize: 'vertical' }} />
            </Space>
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
      <Modal
        title={`Pipeline Config — ${viewTarget?.name ?? ''}`}
        open={viewTarget !== null}
        onCancel={() => setViewTarget(null)}
        width="90vw"
        style={{ maxWidth: 1200 }}
        footer={[
          <Button key="copy" icon={<CopyOutlined />} onClick={() => copyText(viewDetail)}>COPY</Button>,
          <Button key="close" type="primary" onClick={() => setViewTarget(null)}>Close</Button>,
        ]}
      >
        {viewTarget && (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Space wrap>
              <Tag color="blue">{viewTarget.name}</Tag>
              <Tag>v{viewTarget.version}</Tag>
              <Tag color={viewFmt === 'yaml' ? 'blue' : viewFmt === 'json' ? 'green' : 'red'}>
                {viewFmt === 'yaml' ? 'YAML' : viewFmt === 'json' ? 'JSON' : 'Invalid format'}
              </Tag>
              <Button size="small" onClick={handleViewToYaml} disabled={viewFmt !== 'json'}>JSON to YAML</Button>
              <Button size="small" onClick={handleViewToJson} disabled={viewFmt !== 'yaml'}>YAML to JSON</Button>
            </Space>
            <ReadonlyPayloadViewer value={viewDetail} />
          </Space>
        )}
      </Modal>
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
  const [viewTarget, setViewTarget] = useState<Config | null>(null)
  const [viewDetail, setViewDetail] = useState('')
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
  const openView = (cfg: Config) => {
    setSelectedRow(cfg.name)
    setViewTarget(cfg)
    setViewDetail(formatDetail(cfg.detail))
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
  const viewFmt = useMemo(() => detectFormat(viewDetail), [viewDetail])

  const handleViewToYaml = () => {
    try { setViewDetail(toYaml(viewDetail)) }
    catch (e) { message.error('Conversion failed: ' + (e instanceof Error ? e.message : String(e))) }
  }
  const handleViewToJson = () => {
    try { setViewDetail(toJSON(viewDetail)) }
    catch (e) { message.error('Conversion failed: ' + (e instanceof Error ? e.message : String(e))) }
  }
  const handleToYaml = () => {
    try { setDetail(toYaml(detail)) }
    catch (e) { message.error('Conversion failed: ' + (e instanceof Error ? e.message : String(e))) }
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
      title: 'Actions', key: 'act', width: 250,
      render: (_: unknown, r: Config) => (
        <Space size={4} onClick={e => e.stopPropagation()}>
          <Button size="small" icon={<EyeOutlined />} onClick={() => openView(r)}>View</Button>
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
      <Modal title={editing ? `Edit Instance Config — ${editing.name}` : 'Create Instance Config'} open={open}
        onOk={save} onCancel={() => setOpen(false)} width="90vw" style={{ maxWidth: 1200 }}>
        <Form form={form} layout="vertical">
          {!editing && (
            <Form.Item name="name" label="Name" rules={[
              { required: true },
              { pattern: /^[a-zA-Z0-9_][a-zA-Z0-9_-]*$/, message: 'Name must start with a letter, digit, or _ and only contain letters, digits, - and _' },
            ]}>
              <Input placeholder="instance-config-name" />
            </Form.Item>
          )}
          <Form.Item style={{ marginBottom: 0 }}>
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
              <Space wrap>
                {editing && <Tag color="blue">{editing.name}</Tag>}
                {editing && <Tag>v{editing.version}</Tag>}
                {(fmt === 'yaml' || fmt === 'json') && <Tag color="green">JSON</Tag>}
                {fmt === 'unknown' && detail.trim() && <Tag color="red">Invalid format</Tag>}
                <Button size="small" onClick={handleToYaml} disabled={fmt !== 'json'}>JSON to YAML</Button>
                <Button size="small" onClick={handleToJson} disabled={fmt !== 'yaml'}>YAML to JSON</Button>
              </Space>
              <LineNumberedEditor value={detail} onChange={setDetail}
                style={{ height: '60vh', resize: 'vertical' }} />
            </Space>
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
      <Modal
        title={`Instance Config — ${viewTarget?.name ?? ''}`}
        open={viewTarget !== null}
        onCancel={() => setViewTarget(null)}
        width="90vw"
        style={{ maxWidth: 1200 }}
        footer={[
          <Button key="copy" icon={<CopyOutlined />} onClick={() => copyText(viewDetail)}>COPY</Button>,
          <Button key="close" type="primary" onClick={() => setViewTarget(null)}>Close</Button>,
        ]}
      >
        {viewTarget && (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Space wrap>
              <Tag color="blue">{viewTarget.name}</Tag>
              <Tag>v{viewTarget.version}</Tag>
              <Tag color={viewFmt === 'yaml' ? 'blue' : viewFmt === 'json' ? 'green' : 'red'}>
                {viewFmt === 'yaml' ? 'YAML' : viewFmt === 'json' ? 'JSON' : 'Invalid format'}
              </Tag>
              <Button size="small" onClick={handleViewToYaml} disabled={viewFmt !== 'json'}>JSON to YAML</Button>
              <Button size="small" onClick={handleViewToJson} disabled={viewFmt !== 'yaml'}>YAML to JSON</Button>
            </Space>
            <ReadonlyPayloadViewer value={viewDetail} />
          </Space>
        )}
      </Modal>
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
  const [viewTarget, setViewTarget] = useState<OnetimeCommand | null>(null)
  const [viewDetail, setViewDetail] = useState('')
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
  const expireMode = Form.useWatch('expire_mode', form) ?? 'absolute'
  const canCreate = usePermission('onetime_commands', 'create')
  const canDelete = usePermission('onetime_commands', 'delete')

  const refresh = async () => {
    setLoading(true)
    try { setCmds(await listOnetimeCommands()) } finally { setLoading(false) }
  }
  useEffect(() => { refresh() }, [])

  const fmt = useMemo(() => detectFormat(detail), [detail])
  const viewFmt = useMemo(() => detectFormat(viewDetail), [viewDetail])

  const openView = (cmd: OnetimeCommand) => {
    setSelectedRow(cmd.name)
    setViewTarget(cmd)
    setViewDetail(formatDetail(cmd.detail))
  }

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
    let expireTs = 0
    try {
      expireTs = vals.expire_mode === 'duration'
        ? parseExpireDuration(vals.expire_duration)
        : vals.expire_time ? (vals.expire_time as dayjs.Dayjs).unix() : 0
    } catch (e) {
      message.error('Invalid expire duration: ' + (e instanceof Error ? e.message : String(e)))
      return
    }
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

  const handleViewToYaml = () => {
    try { setViewDetail(toYaml(viewDetail)) }
    catch (e) { message.error('Conversion failed: ' + (e instanceof Error ? e.message : String(e))) }
  }
  const handleViewToJson = () => {
    try { setViewDetail(toJSON(viewDetail)) }
    catch (e) { message.error('Conversion failed: ' + (e instanceof Error ? e.message : String(e))) }
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
          <code style={{
            cursor: 'default', fontSize: 11, color: '#1677ff',
            background: 'rgba(22,119,255,0.08)',
            padding: '2px 6px', borderRadius: 3,
            fontFamily: 'monospace', whiteSpace: 'nowrap',
          }}>{preview}</code>
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
            {dayjs.unix(v).format('YYYY-MM-DD HH:mm:ss')}
            {expired ? ' (expired)' : ''}
          </Tag>
        )
      },
    },
    { title: 'Created At', dataIndex: 'created_at', key: 'created_at', width: 170,
      sorter: (a: OnetimeCommand, b: OnetimeCommand) => a.created_at.localeCompare(b.created_at),
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm:ss') },
    {
      title: 'Actions', key: 'act', width: 220,
      render: (_: unknown, r: OnetimeCommand) => (
        <Space size={4} onClick={e => e.stopPropagation()}>
          <Button size="small" icon={<EyeOutlined />} onClick={() => openView(r)}>View</Button>
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
        <Form form={form} layout="vertical" initialValues={{ expire_mode: 'absolute' }}>
          <Form.Item name="name" label="Name" rules={[
            { required: true },
            { pattern: /^[a-zA-Z0-9_][a-zA-Z0-9_-]*$/, message: 'Name must start with a letter, digit, or _ and only contain letters, digits, - and _' },
          ]}>
            <Input placeholder="command-name" />
          </Form.Item>
          <Form.Item label="Expire Time (optional)">
            <Space direction="vertical" style={{ width: '100%' }} size={8}>
              <Form.Item name="expire_mode" noStyle>
                <Segmented
                  options={[
                    { label: 'Date Time', value: 'absolute' },
                    { label: 'Duration', value: 'duration' },
                  ]}
                />
              </Form.Item>
              {expireMode === 'duration' ? (
                <Space.Compact style={{ width: '100%' }}>
                  <Form.Item name="expire_duration" noStyle>
                    <Input placeholder="60min / 6h / 6d / 6w / 6m" />
                  </Form.Item>
                  {['60min', '6h', '6d', '6w', '6m'].map(v => (
                    <Button key={v} onClick={() => form.setFieldValue('expire_duration', v)}>{v}</Button>
                  ))}
                </Space.Compact>
              ) : (
                <Form.Item name="expire_time" noStyle>
                  <DatePicker showTime format="YYYY-MM-DD HH:mm:ss" style={{ width: '100%' }} />
                </Form.Item>
              )}
            </Space>
          </Form.Item>
          <Form.Item
            label={
              <span>
                Detail (command payload)
                {(fmt === 'yaml' || fmt === 'json') && <Tag color="green" style={{ marginLeft: 8 }}>JSON</Tag>}
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
      <Modal
        title={`Onetime Command — ${viewTarget?.name ?? ''}`}
        open={viewTarget !== null}
        onCancel={() => setViewTarget(null)}
        width="80vw"
        style={{ maxWidth: 900 }}
        footer={[
          <Button key="copy" icon={<CopyOutlined />} onClick={() => copyText(viewDetail)}>COPY</Button>,
          <Button key="close" type="primary" onClick={() => setViewTarget(null)}>Close</Button>,
        ]}
      >
        {viewTarget && (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Space wrap>
              <Tag color="blue">{viewTarget.name}</Tag>
              {viewTarget.expire_time ? (
                <Tag color={viewTarget.expire_time < now ? 'red' : 'green'}>
                  Expires {dayjs.unix(viewTarget.expire_time).format('YYYY-MM-DD HH:mm:ss')}
                </Tag>
              ) : <Tag>Never expires</Tag>}
              <Tag color={viewFmt === 'yaml' ? 'blue' : viewFmt === 'json' ? 'green' : 'red'}>
                {viewFmt === 'yaml' ? 'YAML' : viewFmt === 'json' ? 'JSON' : 'Invalid format'}
              </Tag>
              <Button size="small" onClick={handleViewToYaml} disabled={viewFmt !== 'json'}>JSON to YAML</Button>
              <Button size="small" onClick={handleViewToJson} disabled={viewFmt !== 'yaml'}>YAML to JSON</Button>
            </Space>
            <ReadonlyPayloadViewer value={viewDetail} />
          </Space>
        )}
      </Modal>
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
                      {snapExpire ? ` · expires ${dayjs.unix(snapExpire).format('YYYY-MM-DD HH:mm:ss')}` : ''}
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
              {snapExpire ? ` · expires ${dayjs.unix(snapExpire).format('YYYY-MM-DD HH:mm:ss')}` : ''}
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
