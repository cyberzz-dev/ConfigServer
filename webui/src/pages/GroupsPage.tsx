import { useEffect, useState } from 'react'
import {
  Table, Button, Modal, Form, Input, Space, Popconfirm, message, Tabs, Tag, Drawer, List, Typography, Alert,
} from 'antd'
import LineNumberedEditor from '../components/LineNumberedEditor'
import { PlusOutlined, HistoryOutlined, RollbackOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import type { AgentGroup, GroupTag, GroupConfigMapping, Config, OnetimeCommand, ConfigHistory } from '../api'
import {
  listGroups, createGroup, updateGroup, deleteGroup,
  getGroupTags, setGroupTags,
  getGroupIPSelector, setGroupIPSelector,
  setGroupVersionConstraint,
  getGroupConfigs, addGroupConfig, removeGroupConfig,
  listPipelineConfigs, listInstanceConfigs, listOnetimeCommands,
  listConfigHistory, rollbackConfig, listDeletedConfigs, getGroup,
} from '../api'
import { useResizableColumns, tableComponents } from '../components/ResizableColumns'
import { usePermission } from '../PermissionContext'
import DeleteConfirmModal from '../components/DeleteConfirmModal'

// ── Diff utilities ────────────────────────────────────────────────────────────

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

function tagsToText(detail: string): string {
  try {
    const arr = JSON.parse(detail) as { TagName: string; TagValue: string }[]
    return arr.map(t => `${t.TagName}=${t.TagValue}`).join('\n')
  } catch {
    return detail.trim()
  }
}

function ipSelectorToText(detail: string): string {
  try {
    const selector = JSON.parse(detail) as { ips?: string[] }
    return (selector.ips ?? []).join('\n')
  } catch {
    return detail.trim()
  }
}

function parseIPSelectorLines(detail?: string): string[] {
  if (!detail) return []
  return ipSelectorToText(detail).split('\n').map(line => line.trim()).filter(Boolean)
}

function deleteSnapshotToText(detail: string): string {
  try {
    const snap = JSON.parse(detail) as {
      description?: string
      ip_selector_json?: string
      tags?: { TagName: string; TagValue: string }[]
      configs?: { ConfigType: string; ConfigName: string }[]
    }
    const lines: string[] = []
    if (snap.description) lines.push(`description: ${snap.description}`)
    if (snap.ip_selector_json) {
      lines.push('[ip selector]')
      ipSelectorToText(snap.ip_selector_json).split('\n').filter(Boolean).forEach(ip => lines.push(`  ${ip}`))
    }
    if (snap.tags?.length) {
      lines.push('[tags]')
      snap.tags.forEach(t => lines.push(`  ${t.TagName}=${t.TagValue}`))
    }
    if (snap.configs?.length) {
      lines.push('[configs]')
      snap.configs.forEach(c => lines.push(`  ${c.ConfigType}/${c.ConfigName}`))
    }
    return lines.join('\n')
  } catch {
    return detail.trim()
  }
}

export default function GroupsPage() {
  const [groups, setGroups] = useState<AgentGroup[]>([])
  const [sortField, setSortField] = useState<string>('Name')
  const [sortOrder, setSortOrder] = useState<'ascend' | 'descend' | null>('ascend')
  const [loading, setLoading] = useState(true)
  const [createOpen, setCreateOpen] = useState(false)
  const [editTarget, setEditTarget] = useState<AgentGroup | null>(null)
  const [tagsGroup, setTagsGroup] = useState<AgentGroup | null>(null)
  const [tags, setTags] = useState<GroupTag[]>([])
  const [ipSelectorGroup, setIPSelectorGroup] = useState<AgentGroup | null>(null)
  const [ipSelectorLines, setIPSelectorLines] = useState<string[]>([])
  const [versionConstraintGroup, setVersionConstraintGroup] = useState<AgentGroup | null>(null)
  const [configsGroup, setConfigsGroup] = useState<AgentGroup | null>(null)
  const [configs, setConfigs] = useState<GroupConfigMapping[]>([])
  const [addConfigOpen, setAddConfigOpen] = useState(false)
  const [addConfigType, setAddConfigType] = useState<string>('pipeline')
  const [availableConfigs, setAvailableConfigs] = useState<(Config | OnetimeCommand)[]>([])
  const [selectedConfigNames, setSelectedConfigNames] = useState<string[]>([])
  const [loadingAvail, setLoadingAvail] = useState(false)
  const [activeTab, setActiveTab] = useState<string>('pipeline')
  const [configSearch, setConfigSearch] = useState('')
  const [selectedRow, setSelectedRow] = useState<string | undefined>()
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const [historyTarget, setHistoryTarget] = useState<string | null>(null)
  const [historyList, setHistoryList] = useState<ConfigHistory[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const [diffEntry, setDiffEntry] = useState<ConfigHistory | null>(null)
  const [diffCurrentStr, setDiffCurrentStr] = useState('')
  const [diffLoading, setDiffLoading] = useState(false)
  const [diffConfirming, setDiffConfirming] = useState(false)
  const [recycleOpen, setRecycleOpen] = useState(false)
  const [recycleList, setRecycleList] = useState<ConfigHistory[]>([])
  const [recycleLoading, setRecycleLoading] = useState(false)
  const [recycleConfirmItem, setRecycleConfirmItem] = useState<ConfigHistory | null>(null)
  const [recycleDiffCurrent, setRecycleDiffCurrent] = useState('')
  const [recycleDiffLoading, setRecycleDiffLoading] = useState(false)
  const [groupMeta, setGroupMeta] = useState<Record<string, {
    tagCount?: number; ipSelectorCount?: number;
    pipelineCount?: number; instanceCount?: number; onetimeCount?: number;
  }>>({})
  const [form] = Form.useForm()
  const [tagsForm] = Form.useForm()
  const [ipSelectorForm] = Form.useForm()
  const [vcForm] = Form.useForm()
  const canCreate = usePermission('agent_groups', 'create')
  const canUpdate = usePermission('agent_groups', 'update')
  const canDelete = usePermission('agent_groups', 'delete')

  const refreshMeta = async (grps: AgentGroup[]) => {
    const results = await Promise.all(
      grps.map(async g => {
        const [tagsResult, ipSelectorResult, configsResult] = await Promise.allSettled([
          getGroupTags(g.Name),
          getGroupIPSelector(g.Name),
          getGroupConfigs(g.Name),
        ])
        const cfgs = configsResult.status === 'fulfilled' ? configsResult.value : []
        return {
          name: g.Name,
          tagCount: tagsResult.status === 'fulfilled' ? tagsResult.value.length : 0,
          ipSelectorCount: ipSelectorResult.status === 'fulfilled' ? ipSelectorResult.value.ips.length : parseIPSelectorLines(g.IPSelectorJSON).length,
          pipelineCount: cfgs.filter(c => c.ConfigType === 'pipeline').length,
          instanceCount: cfgs.filter(c => c.ConfigType === 'instance').length,
          onetimeCount: cfgs.filter(c => c.ConfigType === 'onetime').length,
        }
      })
    )
    const meta: Record<string, {
      tagCount?: number; ipSelectorCount?: number;
      pipelineCount?: number; instanceCount?: number; onetimeCount?: number;
    }> = {}
    results.forEach(r => { meta[r.name] = r })
    setGroupMeta(meta)
  }

  const refresh = async () => {
    setLoading(true)
    try {
      const grps = await listGroups()
      setGroups(grps)
      refreshMeta(grps)
    } finally { setLoading(false) }
  }

  useEffect(() => { refresh() }, [])

  const handleCreate = async () => {
    const { name, description } = await form.validateFields()
    await createGroup(name, description ?? '')
    message.success(`Agent Group "${name}" created`)
    setCreateOpen(false)
    form.resetFields()
    refresh()
  }

  const handleUpdate = async () => {
    const { description } = await form.validateFields()
    await updateGroup(editTarget!.Name, description ?? '')
    message.success(`Agent Group "${editTarget!.Name}" updated`)
    setEditTarget(null)
    form.resetFields()
    refresh()
  }

  const handleDelete = async (name: string) => {
    await deleteGroup(name)
    message.success(`Agent Group "${name}" deleted`)
    setDeleteTarget(null)
    refresh()
  }

  const openHistory = async (name: string) => {
    setHistoryTarget(name)
    setHistoryLoading(true)
    try { setHistoryList(await listConfigHistory('group', name)) }
    finally { setHistoryLoading(false) }
  }

  const openRecycleBin = async () => {
    setRecycleOpen(true)
    setRecycleLoading(true)
    try { setRecycleList(await listDeletedConfigs('group')) }
    finally { setRecycleLoading(false) }
  }

  const restoreFromRecycleBin = async (item: ConfigHistory) => {
    try {
      await rollbackConfig('group', item.resource_name, item.id)
      message.success(`Agent Group "${item.resource_name}" restored`)
      setRecycleOpen(false)
      refresh()
    } catch (e) {
      message.error('Restore failed: ' + (e instanceof Error ? e.message : String(e)))
    }
  }

  const prepareRecycleOverride = async (item: ConfigHistory) => {
    setRecycleConfirmItem(item)
    setRecycleDiffCurrent('')
    setRecycleDiffLoading(true)
    try {
      // Fetch current group state
      const currentGroup = groups.find(g => g.Name === item.resource_name)
      const currentTags = await getGroupTags(item.resource_name)
      const currentIPSelector = await getGroupIPSelector(item.resource_name)
      const currentConfigs = await getGroupConfigs(item.resource_name)
      
      const lines: string[] = []
      if (currentGroup?.Description) lines.push(`description: ${currentGroup.Description}`)
      if (currentIPSelector.ips.length) {
        lines.push('[ip selector]')
        currentIPSelector.ips.forEach(ip => lines.push(`  ${ip}`))
      }
      if (currentTags.length) {
        lines.push('[tags]')
        currentTags.forEach(t => lines.push(`  ${t.TagName}=${t.TagValue}`))
      }
      if (currentConfigs.length) {
        lines.push('[configs]')
        currentConfigs.forEach(c => lines.push(`  ${c.ConfigType}/${c.ConfigName}`))
      }
      setRecycleDiffCurrent(lines.join('\n'))
    } catch (e) {
      message.error('Failed to load current state: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setRecycleDiffLoading(false)
    }
  }

  const openDiffRollback = async (item: ConfigHistory) => {
    setDiffEntry(item)
    setDiffCurrentStr('')
    if (item.action === 'delete') {
      // Snapshot is already in item.detail; no API call needed.
      return
    }
    setDiffLoading(true)
    try {
      if (item.action === 'set_tags') {
        const current = await getGroupTags(historyTarget!)
        setDiffCurrentStr(current.map((t: GroupTag) => `${t.TagName}=${t.TagValue}`).join('\n'))
      } else if (item.action === 'set_ip_selector') {
        const current = await getGroupIPSelector(historyTarget!)
        setDiffCurrentStr(current.ips.join('\n'))
      } else if (item.action === 'set_version_constraint') {
        const current = await getGroup(historyTarget!)
        setDiffCurrentStr(current.VersionConstraint ?? '')
      } else {
        // add_config / remove_config — fetch current config list
        const current = await getGroupConfigs(historyTarget!)
        setDiffCurrentStr(
          current.map((c: GroupConfigMapping) => `${c.ConfigType}/${c.ConfigName}`).sort().join('\n')
        )
      }
    } finally {
      setDiffLoading(false)
    }
  }

  const confirmRollback = async () => {
    if (!diffEntry) return
    setDiffConfirming(true)
    try {
      await rollbackConfig('group', diffEntry.resource_name, diffEntry.id)
      message.success('Rolled back successfully')
      openHistory(diffEntry.resource_name)
      setDiffEntry(null)
    } catch (e) {
      message.error('Rollback failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setDiffConfirming(false)
    }
  }

  const openTags = async (g: AgentGroup) => {
    const t = await getGroupTags(g.Name)
    setTags(t)
    setTagsGroup(g)
    tagsForm.setFieldsValue({ tags: t.map(x => `${x.TagName}=${x.TagValue}`).join('\n') })
  }

  const openVersionConstraint = (g: AgentGroup) => {
    setVersionConstraintGroup(g)
    vcForm.setFieldsValue({ versionConstraint: g.VersionConstraint })
  }

  const handleSaveVersionConstraint = async () => {
    const { versionConstraint } = await vcForm.validateFields()
    await setGroupVersionConstraint(versionConstraintGroup!.Name, versionConstraint ?? '')
    message.success('Version constraint saved')
    const name = versionConstraintGroup!.Name
    setVersionConstraintGroup(null)
    refresh()
  }

  const openIPSelector = async (g: AgentGroup) => {    let ips = parseIPSelectorLines(g.IPSelectorJSON)
    try {
      const selector = await getGroupIPSelector(g.Name)
      ips = selector.ips
    } catch (e) {
      message.warning('Failed to load IP selector from API; showing cached group data')
    }
    setIPSelectorLines(ips)
    setIPSelectorGroup(g)
    ipSelectorForm.setFieldsValue({ ips: ips.join('\n') })
  }

  const handleSaveIPSelector = async () => {
    const { ips: raw } = await ipSelectorForm.validateFields()
    const ips = (raw as string).split('\n').map(line => line.trim()).filter(Boolean)
    await setGroupIPSelector(ipSelectorGroup!.Name, { ips })
    message.success('IP selector saved')
    const name = ipSelectorGroup!.Name
    setIPSelectorGroup(null)
    setGroupMeta(prev => ({ ...prev, [name]: { ...prev[name], ipSelectorCount: ips.length } }))
  }

  const handleSaveTags = async () => {
    const { tags: raw } = await tagsForm.validateFields()
    const newTags = (raw as string).split('\n')
      .map(line => line.trim())
      .filter(Boolean)
      .map(line => {
        const idx = line.indexOf('=')
        return { TagName: line.slice(0, idx), TagValue: line.slice(idx + 1) }
      })
    await setGroupTags(tagsGroup!.Name, newTags)
    message.success('Tags saved')
    const name = tagsGroup!.Name
    setTagsGroup(null)
    getGroupTags(name).then(t =>
      setGroupMeta(prev => ({ ...prev, [name]: { ...prev[name], tagCount: t.length } }))
    )
  }

  const openConfigs = async (g: AgentGroup) => {
    const c = await getGroupConfigs(g.Name)
    setConfigs(c)
    setConfigsGroup(g)
  }

  const handleRemoveConfig = async (m: GroupConfigMapping) => {
    await removeGroupConfig(m.GroupName, m.ConfigType, m.ConfigName)
    message.success('Removed')
    const c = await getGroupConfigs(configsGroup!.Name)
    setConfigs(c)
    const name = configsGroup!.Name
    setGroupMeta(prev => ({ ...prev, [name]: {
      ...prev[name],
      pipelineCount: c.filter(m => m.ConfigType === 'pipeline').length,
      instanceCount: c.filter(m => m.ConfigType === 'instance').length,
      onetimeCount: c.filter(m => m.ConfigType === 'onetime').length,
    } }))
  }

  const loadAvailableConfigs = async (type: string) => {
    setLoadingAvail(true)
    try {
      if (type === 'pipeline') setAvailableConfigs(await listPipelineConfigs())
      else if (type === 'instance') setAvailableConfigs(await listInstanceConfigs())
      else if (type === 'onetime') setAvailableConfigs(await listOnetimeCommands())
      else setAvailableConfigs([])
    } finally {
      setLoadingAvail(false)
    }
  }

  const handleAddConfig = async () => {
    if (selectedConfigNames.length === 0) {
      message.warning('Please select at least one config')
      return
    }
    for (const name of selectedConfigNames) {
      await addGroupConfig(configsGroup!.Name, addConfigType, name)
    }
    message.success(`Added ${selectedConfigNames.length} config(s)`)
    setAddConfigOpen(false)
    setSelectedConfigNames([])
    const c = await getGroupConfigs(configsGroup!.Name)
    setConfigs(c)
    const name = configsGroup!.Name
    setGroupMeta(prev => ({ ...prev, [name]: {
      ...prev[name],
      pipelineCount: c.filter(m => m.ConfigType === 'pipeline').length,
      instanceCount: c.filter(m => m.ConfigType === 'instance').length,
      onetimeCount: c.filter(m => m.ConfigType === 'onetime').length,
    } }))
  }

  const baseColumns = [
    { title: 'Name', dataIndex: 'Name', key: 'Name', width: 200, ellipsis: true,
      sorter: true,
      sortOrder: sortField === 'Name' ? (sortOrder || undefined) : undefined,
      render: (name: string) => (
        <Space size={6}>
          {name}
          {name === 'default' && <Tag color="blue">Default</Tag>}
        </Space>
      ),
    },
    { title: 'Description', dataIndex: 'Description', key: 'Description', width: 260, ellipsis: true },
    { title: 'Updated At', dataIndex: 'UpdatedAt', key: 'UpdatedAt', width: 170,
      sorter: true,
      sortOrder: sortField === 'UpdatedAt' ? (sortOrder || undefined) : undefined,
      render: (v: string) => v ? dayjs(v).format('YYYY-MM-DD HH:mm:ss') : '—' },
    {
      title: 'Actions', key: 'actions', width: 330,
      render: (_: unknown, record: AgentGroup) => (
        <Space size={4} onClick={e => e.stopPropagation()}>
          {canUpdate && <Button size="small" onClick={() => { setSelectedRow(record.Name); setEditTarget(record); form.setFieldsValue({ description: record.Description }) }}>Edit</Button>}
          <Button size="small" onClick={() => openTags(record)}
            style={groupMeta[record.Name]?.tagCount
              ? { borderColor: '#52c41a', color: '#52c41a' }
              : { color: '#bfbfbf', borderColor: '#d9d9d9' }}>
            {`Tags (${String(groupMeta[record.Name]?.tagCount ?? '\u2026').padEnd(2, '\u00A0')})`}
          </Button>
          <Button size="small" onClick={() => openIPSelector(record)}
            style={groupMeta[record.Name]?.ipSelectorCount
              ? { borderColor: '#fa8c16', color: '#d46b08', background: 'rgba(250, 140, 22, 0.14)' }
              : { color: '#bfbfbf', borderColor: '#d9d9d9' }}>
            {`IPs (${String(groupMeta[record.Name]?.ipSelectorCount ?? '\u2026').padEnd(2, '\u00A0')})`}
          </Button>
          <Button size="small" onClick={() => openVersionConstraint(record)}
            style={record.VersionConstraint
              ? { borderColor: '#722ed1', color: '#722ed1' }
              : { color: '#bfbfbf', borderColor: '#d9d9d9' }}>
            {record.VersionConstraint ? `Ver: ${record.VersionConstraint}` : 'Version'}
          </Button>
          <Button size="small" onClick={() => openConfigs(record)}
            style={(() => {
              const m = groupMeta[record.Name]
              const total = (m?.pipelineCount ?? 0) + (m?.instanceCount ?? 0) + (m?.onetimeCount ?? 0)
              return total > 0
                ? { borderColor: '#1677ff', color: '#1677ff' }
                : { color: '#bfbfbf', borderColor: '#d9d9d9' }
            })()}>
            {(() => {
              const m = groupMeta[record.Name]
              if (!m || m.pipelineCount === undefined) return 'Configs (\u2026)'
              return `pipeline(${m.pipelineCount}) instance(${m.instanceCount ?? 0}) onetime(${m.onetimeCount ?? 0})`
            })()}
          </Button>
          {canDelete && record.Name !== 'default' && (
            <Button size="small" danger onClick={() => { setSelectedRow(record.Name); setDeleteTarget(record.Name) }}>Delete</Button>
          )}
          <Button size="small" icon={<HistoryOutlined />} onClick={() => openHistory(record.Name)}>History</Button>
        </Space>
      ),
    },
  ]
  const columns = useResizableColumns(baseColumns)

  // Compute sorted groups: 'default' always first, others sorted by user selection
  const sortedGroups = (() => {
    const defaultGroup = groups.find(g => g.Name === 'default')
    let otherGroups = groups.filter(g => g.Name !== 'default')
    
    // Apply sorting only when user has selected a sort order
    if (sortOrder && sortField) {
      otherGroups = [...otherGroups].sort((a, b) => {
        let result = 0
        if (sortField === 'Name') {
          result = a.Name.localeCompare(b.Name)
        } else if (sortField === 'UpdatedAt') {
          result = (a.UpdatedAt ?? '').localeCompare(b.UpdatedAt ?? '')
        }
        return sortOrder === 'descend' ? -result : result
      })
    }
    
    return defaultGroup ? [defaultGroup, ...otherGroups] : otherGroups
  })()

  const handleTableChange = (_pagination: any, _filters: any, sorter: any) => {
    // Handle both single column sort and multi-column sort (sorter could be an array)
    const currentSorter = Array.isArray(sorter) ? sorter[0] : sorter
    if (currentSorter && currentSorter.column) {
      setSortField(currentSorter.columnKey || currentSorter.field)
      setSortOrder(currentSorter.order || null)
    } else {
      // No active sorting
      setSortOrder(null)
    }
  }

  return (
    <>
      <div className="page-header">
        <h2>Agent Groups</h2>
        <Space>
          <Button icon={<RollbackOutlined />} onClick={openRecycleBin}>Recycle Bin</Button>
          {canCreate && <Button type="primary" icon={<PlusOutlined />} onClick={() => { setCreateOpen(true); form.resetFields() }}>
            Create Group
          </Button>}
        </Space>
      </div>
      <Table
        rowKey="Name"
        components={tableComponents}
        columns={columns}
        dataSource={sortedGroups}
        loading={loading}
        showSorterTooltip={false}
        size="small"
        scroll={{ x: 'max-content' }}
        onChange={handleTableChange}
        onRow={r => ({
          onClick: () => setSelectedRow(prev => prev === r.Name ? undefined : r.Name),
          style: { cursor: 'pointer' },
        })}
        rowClassName={r => r.Name === selectedRow ? 'row-selected' : ''}
      />

      {/* Create modal */}
      <Modal title="Create Group" open={createOpen} onOk={handleCreate} onCancel={() => setCreateOpen(false)}>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="description" label="Description"><Input /></Form.Item>
        </Form>
      </Modal>

      {/* Edit modal */}
      <Modal title={<span>Edit Group for <span style={{ display: 'inline-block', background: 'rgba(22,119,255,0.12)', color: '#1677ff', borderRadius: 4, padding: '1px 8px', fontWeight: 600, fontFamily: 'monospace', fontSize: '0.92em' }}>{editTarget?.Name ?? ''}</span></span>} open={!!editTarget} onOk={handleUpdate} onCancel={() => setEditTarget(null)}>
        <Form form={form} layout="vertical">
          <Form.Item name="description" label="Description"><Input /></Form.Item>
        </Form>
      </Modal>

      {/* Tags modal */}
      <Modal
        title={`Tags for "${tagsGroup?.Name}"`}
        open={!!tagsGroup}
        onOk={handleSaveTags}
        onCancel={() => setTagsGroup(null)}
      >
        <p style={{ marginTop: 0 }}>One tag per line, format: <code>key=value</code></p>
        <Form form={tagsForm} layout="vertical">
          <Form.Item name="tags">
            <LineNumberedEditor rows={8} />
          </Form.Item>
        </Form>
        <div>
          {tags.map(t => <Tag key={`${t.TagName}=${t.TagValue}`}>{t.TagName}={t.TagValue}</Tag>)}
        </div>
      </Modal>

      {/* IP selector modal */}
      <Modal
        title={`IP Selector for "${ipSelectorGroup?.Name}"`}
        open={!!ipSelectorGroup}
        onOk={handleSaveIPSelector}
        onCancel={() => setIPSelectorGroup(null)}
        footer={[
          <Button key="cancel" onClick={() => setIPSelectorGroup(null)}>Cancel</Button>,
          <Button key="save" type="primary" onClick={handleSaveIPSelector}>Save</Button>,
        ]}
      >
        <p style={{ marginTop: 0 }}>
          One rule per line, for example: <code>192.168.1.2</code>, <code>192.168.1.200-230</code>, <code>192.168.1.0/24</code>
        </p>
        <Form form={ipSelectorForm} layout="vertical">
          <Form.Item name="ips">
            <LineNumberedEditor rows={8} />
          </Form.Item>
        </Form>
        <div>
          {ipSelectorLines.map(ip => <Tag key={ip}>{ip}</Tag>)}
        </div>
      </Modal>

      {/* Version constraint modal */}
      <Modal
        title={`Version Constraint for "${versionConstraintGroup?.Name}"`}
        open={!!versionConstraintGroup}
        onOk={handleSaveVersionConstraint}
        onCancel={() => setVersionConstraintGroup(null)}
      >
        <p style={{ marginTop: 0 }}>
          Format: <code>&gt;= 1.8.0, &lt; 2.0.0</code> (AND), or use <code>||</code> for OR groups: <code>&gt;= 1.0.0, &lt; 2.0.0 || &gt;= 3.0.0</code>. Leave empty to match all versions.
        </p>
        <Form form={vcForm} layout="vertical">
          <Form.Item name="versionConstraint">
            <Input placeholder="e.g. >= 1.8.0, < 2.0.0" allowClear />
          </Form.Item>
        </Form>
      </Modal>

      {/* Group configs modal */}      <Modal
        title={`Configs for "${configsGroup?.Name}"`}
        open={!!configsGroup}
        footer={null}
        onCancel={() => setConfigsGroup(null)}
        width={680}
      >
        <Button type="primary" size="small" icon={<PlusOutlined />} onClick={() => {
          setAddConfigType(activeTab)
          setSelectedConfigNames([])
          setConfigSearch('')
          loadAvailableConfigs(activeTab)
          setAddConfigOpen(true)
        }} style={{ marginBottom: 12 }}>
          Add Config
        </Button>
        <Tabs
          activeKey={activeTab}
          onChange={setActiveTab}
          items={['pipeline', 'instance', 'onetime'].map(type => ({
            key: type,
            label: `${type} (${configs.filter(c => c.ConfigType === type).length})`,
            children: (
              <Table
                rowKey={r => `${r.ConfigType}/${r.ConfigName}`}
                size="small"
                dataSource={configs.filter(c => c.ConfigType === type)}
                columns={[
                  { title: 'Config Name', dataIndex: 'ConfigName', key: 'ConfigName' },
                  {
                    title: '',
                    key: 'del',
                    render: (_: unknown, m: GroupConfigMapping) => (
                      <Popconfirm title="Remove?" onConfirm={() => handleRemoveConfig(m)}>
                        <Button size="small" danger>Remove</Button>
                      </Popconfirm>
                    ),
                  },
                ]}
              />
            ),
          }))}
        />
      </Modal>

      <DeleteConfirmModal
        open={deleteTarget !== null}
        targetName={deleteTarget ?? ''}
        entityType="Agent Group"
        onConfirm={() => handleDelete(deleteTarget!)}
        onCancel={() => setDeleteTarget(null)}
      />

      {/* Add config modal */}
      <Modal
        title={`Add ${addConfigType} config to "${configsGroup?.Name}"`}
        open={addConfigOpen}
        onOk={handleAddConfig}
        onCancel={() => { setAddConfigOpen(false); setSelectedConfigNames([]); setConfigSearch('') }}
        okText={selectedConfigNames.length > 0 ? `Add ${selectedConfigNames.length} Selected` : 'Add'}
        width={600}
      >
        <Input.Search
          placeholder="Search by name..."
          value={configSearch}
          onChange={e => setConfigSearch(e.target.value)}
          style={{ marginBottom: 8 }}
          allowClear
        />
        <p style={{ fontSize: 12, color: '#999', margin: '0 0 8px' }}>
          Already-added configs are not shown.
        </p>
        <Table
          rowKey="name"
          size="small"
          loading={loadingAvail}
          dataSource={availableConfigs.filter(
            c => !configs.some(gc => gc.ConfigType === addConfigType && gc.ConfigName === c.name)
              && c.name.toLowerCase().includes(configSearch.toLowerCase())
          )}
          columns={[{ title: 'Config Name', dataIndex: 'name', key: 'name' }]}
          rowSelection={{
            selectedRowKeys: selectedConfigNames,
            onChange: keys => setSelectedConfigNames(keys as string[]),
          }}
          pagination={{ pageSize: 8 }}
        />
      </Modal>

      {/* History drawer */}
      <Drawer
        title={`History — Agent Group "${historyTarget}"`}
        open={historyTarget !== null}
        onClose={() => setHistoryTarget(null)}
        width={580}
      >
        <List
          loading={historyLoading}
          dataSource={historyList}
          locale={{ emptyText: 'No history yet' }}
          renderItem={item => {
            const canRollback = item.action === 'set_tags' || item.action === 'set_ip_selector' || item.action === 'set_version_constraint' || item.action === 'add_config' || item.action === 'remove_config' || item.action === 'delete'
            const tagColor = item.action === 'delete' ? 'red'
              : item.action === 'create' ? 'green'
              : item.action === 'rollback' ? 'purple'
              : item.action === 'set_tags' ? 'geekblue'
              : item.action === 'set_ip_selector' ? 'teal'
              : item.action === 'set_version_constraint' ? 'volcano'
              : item.action === 'add_config' ? 'cyan'
              : item.action === 'remove_config' ? 'orange'
              : 'blue'
            return (
              <List.Item
                actions={canRollback ? [
                  <Button key="rb" size="small" onClick={() => openDiffRollback(item)}>Rollback</Button>,
                ] : []}
              >
                <List.Item.Meta
                  title={<span><Tag color={tagColor}>{item.action}</Tag>{dayjs(item.changed_at).format('YYYY-MM-DD HH:mm:ss')}</span>}
                  description={<Typography.Text type="secondary">by {item.changed_by}{item.detail ? ` · ${item.detail}` : ''}</Typography.Text>}
                />
              </List.Item>
            )
          }}
        />
      </Drawer>

      {/* Diff/rollback modal — set_tags / set_ip_selector / add_config / remove_config / delete */}
      {diffEntry && (() => {
        // Build before/after strings depending on action
        let beforeStr: string
        let afterStr: string
        let diffLines: ReturnType<typeof computeDiff>
        if (diffEntry.action === 'set_tags') {
          beforeStr = diffCurrentStr
          afterStr = tagsToText(diffEntry.detail)
          diffLines = computeDiff(beforeStr, afterStr)
        } else if (diffEntry.action === 'set_ip_selector') {
          beforeStr = diffCurrentStr
          afterStr = ipSelectorToText(diffEntry.detail)
          diffLines = computeDiff(beforeStr, afterStr)
        } else if (diffEntry.action === 'add_config') {
          // rollback = remove this config
          beforeStr = diffCurrentStr
          afterStr = diffCurrentStr.split('\n').filter(l => l && l !== diffEntry.detail).join('\n')
          diffLines = computeDiff(beforeStr, afterStr)
        } else if (diffEntry.action === 'remove_config') {
          // remove_config rollback = add this config back
          beforeStr = diffCurrentStr
          const lines = diffCurrentStr.split('\n').filter(Boolean)
          afterStr = [...lines, diffEntry.detail].sort().join('\n')
          diffLines = computeDiff(beforeStr, afterStr)
        } else if (diffEntry.action === 'set_version_constraint') {
          beforeStr = diffCurrentStr
          afterStr = diffEntry.detail ?? ''
          diffLines = computeDiff(beforeStr, afterStr)
        } else {
          // delete rollback = recreate group; show snapshot as all-additions
          beforeStr = ''
          afterStr = deleteSnapshotToText(diffEntry.detail)
          diffLines = afterStr.split('\n').filter(Boolean).map(l => ({ type: 'add' as const, value: l }))
        }
        const hasChanges = !diffLoading && (diffEntry.action === 'delete' ? diffLines.length > 0 : diffLines.some(l => l.type !== 'equal'))
        const modalTitle = diffEntry.action === 'set_tags'
          ? 'Rollback Preview — Tags'
          : diffEntry.action === 'set_ip_selector'
            ? 'Rollback Preview — IP Selector'
          : diffEntry.action === 'set_version_constraint'
            ? 'Rollback Preview — Version Constraint'
          : diffEntry.action === 'add_config'
            ? `Rollback Preview — Remove config "${diffEntry.detail}"`
            : diffEntry.action === 'remove_config'
              ? `Rollback Preview — Re-add config "${diffEntry.detail}"`
              : `Rollback Preview — Recreate deleted group "${diffEntry.resource_name}"`
        return (
          <Modal
            title={modalTitle}
            open
            width={680}
            onCancel={() => setDiffEntry(null)}
            footer={[
              <Button key="cancel" onClick={() => setDiffEntry(null)}>Cancel</Button>,
              <Button key="confirm" type="primary" danger disabled={!hasChanges} loading={diffConfirming} onClick={confirmRollback}>
                Confirm Rollback
              </Button>,
            ]}
          >
            <div style={{ marginBottom: 10, fontSize: 13, color: '#555' }}>
              {diffEntry.action === 'delete'
                ? <>Recreating group <strong>{diffEntry.resource_name}</strong> (deleted at{' '}
                    <strong>{dayjs(diffEntry.changed_at).format('YYYY-MM-DD HH:mm:ss')}</strong>{' '}
                    by <strong>{diffEntry.changed_by}</strong>)</>
                : <>Rolling back <strong>{diffEntry.action}</strong> for group <strong>{diffEntry.resource_name}</strong>,{' '}
                    snapshot from <strong>{dayjs(diffEntry.changed_at).format('YYYY-MM-DD HH:mm:ss')}</strong>{' '}
                    by <strong>{diffEntry.changed_by}</strong></>
              }
            </div>
            {diffLoading
              ? <div style={{ textAlign: 'center', padding: 20, color: '#888' }}>Loading current state…</div>
              : !hasChanges
                ? <Alert message="No changes — already matches the target state." type="info" showIcon />
                : (
                  <>
                    <div style={{ marginBottom: 6, fontSize: 12, color: '#888' }}>
                      <span style={{ background: '#ffebe9', padding: '1px 8px', borderRadius: 3, marginRight: 10 }}>− will be removed</span>
                      <span style={{ background: '#e6ffed', padding: '1px 8px', borderRadius: 3 }}>+ will be added</span>
                    </div>
                    <div style={{ fontFamily: 'monospace', fontSize: 12.5, border: '1px solid #3c4043', borderRadius: 6, overflow: 'auto', maxHeight: 400, background: '#202124' }}>
                      {diffLines.map((line, idx) => (
                        <div key={idx} style={{ display: 'flex', alignItems: 'baseline', background: line.type === 'add' ? 'rgba(38, 166, 91, 0.15)' : line.type === 'remove' ? 'rgba(242, 139, 130, 0.15)' : 'transparent', padding: '1px 10px', lineHeight: '1.65' }}>
                          <span style={{ color: line.type === 'add' ? '#81c995' : line.type === 'remove' ? '#f28b82' : '#9aa0a6', marginRight: 10, userSelect: 'none', flexShrink: 0, width: '0.9em' }}>
                            {line.type === 'add' ? '+' : line.type === 'remove' ? '−' : ' '}
                          </span>
                          <pre style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all', color: line.type === 'add' ? '#81c995' : line.type === 'remove' ? '#f28b82' : '#e8eaed' }}>{line.value}</pre>
                        </div>
                      ))}
                    </div>
                  </>
                )
            }
          </Modal>
        )
      })()}
      <Drawer
        title="Recycle Bin — Deleted Agent Groups"
        open={recycleOpen}
        onClose={() => setRecycleOpen(false)}
        width={500}
      >
        <List
          loading={recycleLoading}
          dataSource={recycleList}
          locale={{ emptyText: 'No recently deleted groups' }}
          renderItem={item => {
            let snapText = ''
            try {
              const snap = JSON.parse(item.detail) as { description?: string; tags?: unknown[]; configs?: unknown[] }
              const parts: string[] = []
              if (snap.description) parts.push(snap.description)
              if (snap.tags?.length) parts.push(`${snap.tags.length} tag(s)`)
              if (snap.configs?.length) parts.push(`${snap.configs.length} config(s)`)
              snapText = parts.join(' · ')
            } catch { snapText = '' }
            const existsNow = groups.some(g => g.Name === item.resource_name)
            return (
              <List.Item
                actions={[
                  <Button size="small" type="primary" ghost key="restore" onClick={() => {
                    if (existsNow) {
                      prepareRecycleOverride(item)
                    } else {
                      restoreFromRecycleBin(item)
                    }
                  }}>
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
                      {snapText ? ` · ${snapText}` : ''}
                    </Typography.Text>
                  }
                />
              </List.Item>
            )
          }}
        />
      </Drawer>
      {recycleConfirmItem && (() => {
        let snapText = ''
        try {
          const snap = JSON.parse(recycleConfirmItem.detail) as { description?: string; tags?: unknown[]; configs?: unknown[] }
          const parts: string[] = []
          if (snap.description) parts.push(snap.description)
          if (snap.tags?.length) parts.push(`${snap.tags.length} tag(s)`)
          if (snap.configs?.length) parts.push(`${snap.configs.length} config(s)`)
          snapText = parts.join(' · ')
        } catch { snapText = '' }
        
        const snapshotText = deleteSnapshotToText(recycleConfirmItem.detail)
        const diff = (() => {
          // 两者都为空 - 无变化
          if (!recycleDiffCurrent && !snapshotText) return []
          // 当前为空，快照有内容 - 所有行为新增
          if (!recycleDiffCurrent && snapshotText) {
            return snapshotText.split('\n').map(line => ({ type: 'add' as const, value: line }))
          }
          // 当前有内容，快照为空 - 所有行将被删除
          if (recycleDiffCurrent && !snapshotText) {
            return recycleDiffCurrent.split('\n').map(line => ({ type: 'remove' as const, value: line }))
          }
          // 两者都有内容 - 正常 diff
          return computeDiff(recycleDiffCurrent, snapshotText)
        })()
        
        return (
          <Modal
            title={`Override & Restore Agent Group "${recycleConfirmItem.resource_name}"`}
            open
            onCancel={() => {
              setRecycleConfirmItem(null)
              setRecycleDiffCurrent('')
            }}
            onOk={() => {
              restoreFromRecycleBin(recycleConfirmItem)
              setRecycleConfirmItem(null)
              setRecycleDiffCurrent('')
            }}
            okText="Override & Restore"
            okButtonProps={{ danger: true, loading: recycleDiffLoading }}
            width={700}
          >
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 12 }}
              message={<>A group named <strong>{recycleConfirmItem.resource_name}</strong> already exists and will be <strong>replaced</strong> with the deleted snapshot.</>}
            />
            <Typography.Text type="secondary" style={{ display: 'block', marginBottom: 8 }}>
              Deleted {dayjs(recycleConfirmItem.changed_at).format('YYYY-MM-DD HH:mm:ss')} by {recycleConfirmItem.changed_by}
              {snapText ? ` · ${snapText}` : ''}
            </Typography.Text>
            
            {recycleDiffLoading ? (
              <div style={{ textAlign: 'center', padding: '20px 0' }}>Loading diff...</div>
            ) : diff.length === 0 ? (
              <div style={{ 
                textAlign: 'center', 
                padding: '40px 20px', 
                color: '#9aa0a6',
                background: '#202124',
                border: '1px solid #3c4043',
                borderRadius: 6,
              }}>
                No changes · 无变化
              </div>
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
                  {diff.map((line, idx) => (
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
      })()}
    </>
  )
}
