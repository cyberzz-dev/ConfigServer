import { useEffect, useState, useCallback, useMemo } from 'react'
import yaml from 'js-yaml'
import {
  Table, Button, Modal, Form, Input, InputNumber, Slider, Space, message, notification,
  Tag, Drawer, Descriptions, Statistic, Row, Col, Tooltip, Select, Progress,
  Tabs, Spin, AutoComplete, Card, Alert, Typography,
} from 'antd'
const { Text } = Typography
import {
  PlusOutlined, PauseCircleOutlined, PlayCircleOutlined,
  CheckCircleOutlined, StopOutlined, BarChartOutlined, ReloadOutlined,
  MinusCircleOutlined, EyeOutlined, EditOutlined, SearchOutlined,
} from '@ant-design/icons'
import LineNumberedEditor from '../components/LineNumberedEditor'
import DiffViewer from '../components/DiffViewer'
import type { CanaryRelease, CanaryStats, CanaryAgent, AgentTag, AgentGroup } from '../api'
import {
  listCanaries, createCanary, updateCanary, setCanaryPercent,
  pauseCanary, resumeCanary, promoteCanary, abortCanary, getCanaryStats, getCanaryAgents,
  listPipelineConfigs, listInstanceConfigs,
  listGroups, createPipelineConfig, createInstanceConfig, addGroupConfig,
} from '../api'
import type { Config } from '../api'

// ── YAML ↔ JSON helpers ───────────────────────────────────────────────────────
// Convert JSON string to YAML for human-friendly display/editing.
// If the string is not valid JSON (already YAML or empty), return as-is.
function jsonToYaml(s: string): string {
  if (!s || !s.trim()) return s
  try {
    const parsed = JSON.parse(s)
    return yaml.dump(parsed, { indent: 2, lineWidth: -1 }).trimEnd()
  } catch {
    return s
  }
}

// Convert a YAML or JSON string to JSON for storage.
// If already valid JSON, returns as-is. Otherwise parses as YAML and serialises.
function yamlToJson(s: string): string {
  if (!s || !s.trim()) return s
  try {
    JSON.parse(s)
    return s // already valid JSON
  } catch {
    try {
      const parsed = yaml.load(s)
      return JSON.stringify(parsed)
    } catch {
      return s // unparseable — pass through and let server validate
    }
  }
}

// ── Per-entry form item for batch canary creation ─────────────────────────────
interface CanaryFormItemProps {
  field: { name: number; key: number }
  pipelineConfigs: Config[]
  instanceConfigs: Config[]
  groups: AgentGroup[]
  form: ReturnType<typeof Form.useForm>[0]
  onRemove: () => void
  canRemove: boolean
}
function CanaryFormItem({ field, pipelineConfigs, instanceConfigs, groups, form, onRemove, canRemove }: CanaryFormItemProps) {
  const type = Form.useWatch(['canaries', field.name, 'config_type'], form)
  const nameValue: string | undefined = Form.useWatch(['canaries', field.name, 'config_name'], form)
  const configOptions = (type === 'pipeline' ? pipelineConfigs : instanceConfigs).map(c => ({ value: c.name }))
  const isNewConfig = !!nameValue && !configOptions.some(o => o.value === nameValue)
  return (
    <Card
      size="small"
      style={{ marginBottom: 0, width: 520, flexShrink: 0 }}
      extra={canRemove ? (
        <Button type="text" danger size="small" icon={<MinusCircleOutlined />} onClick={onRemove} />
      ) : null}
    >
      <Form.Item label="Config Type" name={[field.name, 'config_type']} rules={[{ required: true }]}>
        <Select
          options={[
            { label: 'Pipeline Config', value: 'pipeline' },
            { label: 'Instance Config', value: 'instance' },
          ]}
          onChange={() => form.setFieldValue(['canaries', field.name, 'config_name'], undefined)}
        />
      </Form.Item>
      <Form.Item
        label="Config Name"
        name={[field.name, 'config_name']}
        rules={[
          { required: true, message: 'Config name is required' },
          { pattern: /^[a-zA-Z0-9_][a-zA-Z0-9_-]*$/, message: 'Name must start with a letter, digit, or _ and only contain letters, digits, - and _' },
        ]}
        tooltip="Select an existing config or type a new name to create a canary for a new config"
      >
        <AutoComplete
          options={configOptions}
          placeholder="Select or type a config name"
          filterOption={(input, opt) =>
            (opt?.value as string ?? '').toLowerCase().includes(input.toLowerCase())
          }
        />
      </Form.Item>
      {isNewConfig && (
        <>
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 16 }}
            message="New config — Agent Group required"
            description="This config name does not exist yet. Submitting will automatically create an empty stable config and map it to the selected Agent Groups. Agents not covered by the canary will receive an empty config; once the canary rollout is complete, use Promote to publish the final version."
          />
          <Form.Item
            label="Agent Groups"
            name={[field.name, 'agent_groups']}
            rules={[{ required: true, type: 'array', min: 1, message: 'At least one Agent Group is required for a new config' }]}
            tooltip="Map the new config to these Agent Groups so their agents can receive it."
          >
            <Select
              mode="multiple"
              showSearch
              placeholder="Select Agent Groups"
              options={groups.map(g => ({ label: g.Name, value: g.Name }))}
              filterOption={(input, opt) =>
                (opt?.label as string ?? '').toLowerCase().includes(input.toLowerCase())
              }
            />
          </Form.Item>
        </>
      )}
      <Form.Item
        label="Canary Config Detail (YAML / JSON)"
        name={[field.name, 'canary_detail']}
        rules={[{ required: true, message: 'Config content is required' }]}
      >
        <LineNumberedEditor
          value={form.getFieldValue(['canaries', field.name, 'canary_detail']) ?? ''}
          onChange={v => form.setFieldValue(['canaries', field.name, 'canary_detail'], v)}
          style={{ height: 220 }}
        />
      </Form.Item>
      <Form.Item
        label="Initial Rollout %"
        name={[field.name, 'rollout_percent']}
        rules={[{ required: true }, { type: 'number', min: 0, max: 100 }]}
      >
        <InputNumber min={0} max={100} addonAfter="%" style={{ width: 140 }} />
      </Form.Item>
      <Form.Item
        label="Version Constraint"
        name={[field.name, 'version_constraint']}
        tooltip="Semver range (e.g. >=2.0.0). Leave empty to apply to all versions."
      >
        <Input placeholder="e.g. >=2.0.0 (optional)" />
      </Form.Item>
      <Form.Item
        label="IP Selector"
        name={[field.name, 'ip_selector']}
        tooltip="Restrict the canary to matching agent IPs. One rule per line or comma-separated. Supports exact IP, CIDR (10.0.0.0/24), and range (10.0.0.1-10)."
      >
        <Input.TextArea rows={2} placeholder={'e.g.\n10.0.0.0/24\n192.168.1.10-20'} />
      </Form.Item>
      <Form.Item
        label="Tag Selector"
        tooltip="Restrict the canary to agents carrying at least one of these tags (ANY-match)."
      >
        <Form.List name={[field.name, 'tag_selector']}>
          {(tagFields, { add: addTag, remove: removeTag }) => (
            <>
              {tagFields.map(tagField => (
                <Space key={tagField.key} align="baseline" style={{ display: 'flex', marginBottom: 8 }}>
                  <Form.Item
                    {...tagField}
                    name={[tagField.name, 'name']}
                    rules={[{ required: true, message: 'Tag name required' }]}
                    style={{ marginBottom: 0 }}
                  >
                    <Input placeholder="tag name" style={{ width: 160 }} />
                  </Form.Item>
                  <span style={{ color: '#bfbfbf' }}>=</span>
                  <Form.Item {...tagField} name={[tagField.name, 'value']} style={{ marginBottom: 0 }}>
                    <Input placeholder="tag value" style={{ width: 160 }} />
                  </Form.Item>
                  <MinusCircleOutlined onClick={() => removeTag(tagField.name)} style={{ color: '#999' }} />
                </Space>
              ))}
              <Button type="dashed" onClick={() => addTag()} block icon={<PlusOutlined />}>Add Tag</Button>
            </>
          )}
        </Form.List>
      </Form.Item>
    </Card>
  )
}

const STATUS_COLOR: Record<string, string> = {
  rolling:  'processing',
  paused:   'warning',
  promoted: 'success',
  aborted:  'default',
}

const TYPE_COLORS: Record<string, string> = {
  pipeline: 'geekblue',
  instance: 'cyan',
  onetime:  'orange',
  group:    'gold',
}

// Text colors matching the Tag preset colors above
const TYPE_TEXT_COLORS: Record<string, string> = {
  geekblue: '#2f54eb',
  cyan:     '#08979c',
  orange:   '#d46b08',
  gold:     '#d48806',
}

const STATUS_LABEL: Record<string, string> = {
  rolling:  'Rolling',
  paused:   'Paused',
  promoted: 'Promoted',
  aborted:  'Aborted',
}

function StatusTag({ status }: { status: string }) {
  return <Tag color={STATUS_COLOR[status] ?? 'default'}>{STATUS_LABEL[status] ?? status}</Tag>
}

export default function CanaryPage() {
  const [canaries, setCanaries] = useState<CanaryRelease[]>([])
  const [loading, setLoading] = useState(true)

  // Create drawer
  const [createOpen, setCreateOpen] = useState(false)
  const [createLoading, setCreateLoading] = useState(false)
  const [form] = Form.useForm()
  const [pipelineConfigs, setPipelineConfigs] = useState<Config[]>([])
  const [instanceConfigs, setInstanceConfigs] = useState<Config[]>([])
  const [groups, setGroups] = useState<AgentGroup[]>([])

  // Stats drawer
  const [statsOpen, setStatsOpen] = useState(false)
  const [statsLoading, setStatsLoading] = useState(false)
  const [stats, setStats] = useState<CanaryStats | null>(null)
  const [statsTarget, setStatsTarget] = useState<CanaryRelease | null>(null)
  const [statsTab, setStatsTab] = useState<'summary' | 'agents'>('summary')
  const [agentsData, setAgentsData] = useState<CanaryAgent[]>([])
  const [agentsLoading, setAgentsLoading] = useState(false)
  const [agentsSearch, setAgentsSearch] = useState('')
  const [agentsBucketFilter, setAgentsBucketFilter] = useState<'all' | 'canary' | 'stable' | 'unknown'>('all')
  const [agentsPage, setAgentsPage] = useState(1)
  const [agentsPageSize, setAgentsPageSize] = useState(50)

  const filteredAgents = useMemo(() => {
    let data = agentsData
    if (agentsBucketFilter !== 'all') {
      data = data.filter(a => a.bucket === agentsBucketFilter)
    }
    const q = agentsSearch.trim().toLowerCase()
    if (q) {
      data = data.filter(a =>
        a.instance_id.toLowerCase().includes(q) ||
        a.ip.toLowerCase().includes(q) ||
        a.hostname.toLowerCase().includes(q) ||
        a.agent_type.toLowerCase().includes(q)
      )
    }
    return data
  }, [agentsData, agentsSearch, agentsBucketFilter])

  // Detail drawer
  const [detailTarget, setDetailTarget] = useState<CanaryRelease | null>(null)
  const [detailTab, setDetailTab] = useState<'content' | 'diff'>('content')
  const [stableDetail, setStableDetail] = useState<string>('')
  const [stableLoading, setStableLoading] = useState(false)

  // Percent edit modal
  const [percentTarget, setPercentTarget] = useState<CanaryRelease | null>(null)
  const [percentValue, setPercentValue] = useState(0)
  const [percentLoading, setPercentLoading] = useState(false)

  // Promote / Abort confirm modal
  const [confirmAction, setConfirmAction] = useState<{ action: 'promote' | 'abort'; cr: CanaryRelease } | null>(null)
  const [confirmInput, setConfirmInput] = useState('')
  const [confirmLoading, setConfirmLoading] = useState(false)

  // Edit drawer
  const [editOpen, setEditOpen] = useState(false)
  const [editLoading, setEditLoading] = useState(false)
  const [editTarget, setEditTarget] = useState<CanaryRelease | null>(null)
  const [editForm] = Form.useForm()

  const fetchCanaries = useCallback(async () => {
    setLoading(true)
    try {
      setCanaries(await listCanaries())
    } catch {
      message.error('Failed to load canary releases')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { fetchCanaries() }, [fetchCanaries])

  // Load config lists and groups when create drawer opens
  useEffect(() => {
    if (!createOpen) return
    Promise.all([listPipelineConfigs(), listInstanceConfigs(), listGroups()]).then(([p, i, g]) => {
      setPipelineConfigs(p)
      setInstanceConfigs(i)
      setGroups(g)
    })
  }, [createOpen])

  const handleCreate = async (values: {
    canaries: {
      config_type: string
      config_name: string
      canary_detail: string
      rollout_percent: number
      version_constraint?: string
      ip_selector?: string
      tag_selector?: { name: string; value: string }[]
      agent_groups?: string[]
    }[]
  }) => {
    setCreateLoading(true)
    const existingPipeline = new Set(pipelineConfigs.map(c => c.name))
    const existingInstance = new Set(instanceConfigs.map(c => c.name))
    let successCount = 0
    const errors: { name: string; msg: string; isConflict: boolean }[] = []
    for (const entry of values.canaries) {
      try {
        const isNew = entry.config_type === 'pipeline'
          ? !existingPipeline.has(entry.config_name)
          : !existingInstance.has(entry.config_name)
        if (isNew) {
          if (entry.config_type === 'pipeline') {
            await createPipelineConfig(entry.config_name, '')
          } else {
            await createInstanceConfig(entry.config_name, '')
          }
          for (const groupName of (entry.agent_groups ?? [])) {
            await addGroupConfig(groupName, entry.config_type, entry.config_name)
          }
        }
        const ipList = (entry.ip_selector ?? '')
          .split(/[\n,]/)
          .map(s => s.trim())
          .filter(Boolean)
        const tagList = (entry.tag_selector ?? [])
          .filter(t => t && t.name)
          .map(t => ({ name: t.name.trim(), value: (t.value ?? '').trim() }))
        await createCanary(entry.config_type, entry.config_name, {
          canary_detail: yamlToJson(entry.canary_detail),
          rollout_percent: entry.rollout_percent ?? 0,
          version_constraint: entry.version_constraint ?? '',
          ip_selector: ipList.length ? ipList : undefined,
          tag_selector: tagList.length ? tagList : undefined,
        })
        successCount++
      } catch (e: any) {
        const status: number | undefined = e?.response?.status
        const msg: string = e?.response?.data?.message ?? e?.message ?? 'failed'
        // 409 Conflict = canary already exists; surface this specially
        errors.push({ name: entry.config_name, msg, isConflict: status === 409 })
      }
    }
    setCreateLoading(false)
    if (errors.length === 0) {
      message.success(successCount === 1 ? 'Canary release created' : `${successCount} canary releases created`)
      setCreateOpen(false)
      form.resetFields()
      fetchCanaries()
    } else if (successCount > 0) {
      notification.warning({
        message: `${successCount} created, ${errors.length} failed`,
        description: (
          <ul style={{ margin: 0, paddingLeft: 16 }}>
            {errors.map((e, i) => (
              <li key={i}><Text code>{e.name}</Text> — {e.msg}</li>
            ))}
          </ul>
        ),
        duration: 0,
      })
      fetchCanaries()
    } else {
      notification.error({
        message: errors.length === 1 && errors[0].isConflict
          ? 'Canary release already exists'
          : 'Failed to create canary release',
        description: (
          <ul style={{ margin: 0, paddingLeft: 16 }}>
            {errors.map((e, i) => (
              <li key={i}>
                {errors.length > 1 && <><Text code>{e.name}</Text> — </>}
                {e.msg}
              </li>
            ))}
          </ul>
        ),
        duration: 0,
      })
    }
  }

  const handleSetPercent = async () => {
    if (!percentTarget) return
    setPercentLoading(true)
    try {
      await setCanaryPercent(percentTarget.config_type, percentTarget.config_name, percentValue)
      message.success('Rollout percent updated')
      setPercentTarget(null)
      fetchCanaries()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to update percent')
    } finally {
      setPercentLoading(false)
    }
  }

  const handleAction = async (
    action: 'pause' | 'resume' | 'promote' | 'abort',
    cr: CanaryRelease,
  ) => {
    const fns = { pause: pauseCanary, resume: resumeCanary, promote: promoteCanary, abort: abortCanary }
    const labels = { pause: 'Paused', resume: 'Resumed', promote: 'Promoted', abort: 'Aborted' }
    try {
      await fns[action](cr.config_type, cr.config_name)
      message.success(`${labels[action]} successfully`)
      fetchCanaries()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? `Failed to ${action}`)
    }
  }

  const openEdit = (cr: CanaryRelease) => {
    setEditTarget(cr)
    editForm.setFieldsValue({
      canary_detail: jsonToYaml(cr.canary_detail ?? ''),
      rollout_percent: cr.rollout_percent,
      version_constraint: cr.version_constraint ?? '',
      ip_selector: (cr.ip_selector ?? []).join('\n'),
      tag_selector: (cr.tag_selector ?? []).map(t => ({ name: t.name, value: t.value })),
    })
    setEditOpen(true)
  }

  const handleEdit = async (values: {
    canary_detail: string
    rollout_percent: number
    version_constraint?: string
    ip_selector?: string
    tag_selector?: { name: string; value: string }[]
  }) => {
    if (!editTarget) return
    setEditLoading(true)
    try {
      const ipList = (values.ip_selector ?? '')
        .split(/[\n,]/)
        .map(s => s.trim())
        .filter(Boolean)
      const tagList = (values.tag_selector ?? [])
        .filter(t => t && t.name)
        .map(t => ({ name: t.name.trim(), value: (t.value ?? '').trim() }))
      await updateCanary(editTarget.config_type, editTarget.config_name, {
        canary_detail: yamlToJson(values.canary_detail),
        rollout_percent: values.rollout_percent ?? 0,
        version_constraint: values.version_constraint ?? '',
        ip_selector: ipList.length ? ipList : undefined,
        tag_selector: tagList.length ? tagList : undefined,
      })
      message.success('Canary release updated')
      setEditOpen(false)
      setEditTarget(null)
      editForm.resetFields()
      fetchCanaries()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to update canary release')
    } finally {
      setEditLoading(false)
    }
  }

  const openDetail = async (cr: CanaryRelease) => {
    setDetailTarget(cr)
    setDetailTab('content')
    setStableDetail('')
    setStableLoading(true)
    try {
      const list = await (cr.config_type === 'pipeline' ? listPipelineConfigs() : listInstanceConfigs())
      const found = list.find(c => c.name === cr.config_name)
      setStableDetail(found?.detail ?? '')
    } catch {
      setStableDetail('')
    } finally {
      setStableLoading(false)
    }
  }

  const openStats = async (cr: CanaryRelease) => {
    setStatsTarget(cr)
    setStats(null)
    setAgentsData([])
    setAgentsSearch('')
    setAgentsBucketFilter('all')
    setAgentsPage(1)
    setStatsTab('summary')
    setStatsOpen(true)
    setStatsLoading(true)
    try {
      setStats(await getCanaryStats(cr.config_type, cr.config_name))
    } catch {
      message.error('Failed to load stats')
    } finally {
      setStatsLoading(false)
    }
  }

  const loadCanaryAgents = async (cr: CanaryRelease) => {
    setAgentsLoading(true)
    try {
      setAgentsData(await getCanaryAgents(cr.config_type, cr.config_name))
    } catch {
      message.error('Failed to load canary agents')
    } finally {
      setAgentsLoading(false)
    }
  }

  const handleStatsTabChange = (key: string) => {
    setStatsTab(key as 'summary' | 'agents')
    setAgentsPage(1)
    if (key === 'agents' && statsTarget && agentsData.length === 0 && !agentsLoading) {
      loadCanaryAgents(statsTarget)
    }
  }

  const columns = [
    {
      title: 'Config Name',
      dataIndex: 'config_name',
      key: 'config_name',
      ellipsis: true,
      render: (name: string, record: { config_type: string }) => (
        <Text style={{ color: TYPE_TEXT_COLORS[TYPE_COLORS[record.config_type] ?? ''] ?? 'inherit', fontWeight: 500 }}>
          {name}
        </Text>
      ),
    },
    {
      title: 'Type',
      dataIndex: 'config_type',
      key: 'config_type',
      width: 100,
      render: (v: string) => <Tag color={TYPE_COLORS[v] ?? 'default'}>{v}</Tag>,
    },
    {
      title: 'Status',
      dataIndex: 'status',
      key: 'status',
      width: 110,
      render: (v: string) => <StatusTag status={v} />,
    },
    {
      title: 'Rollout',
      dataIndex: 'rollout_percent',
      key: 'rollout_percent',
      width: 150,
      align: 'left' as const,
      render: (v: number, cr: CanaryRelease) => (
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, justifyContent: 'flex-start' }}>
          <Progress
            percent={v}
            size="small"
            showInfo={false}
            style={{ width: 80, marginBottom: 0 }}
            status={cr.status === 'paused' ? 'exception' : cr.status === 'promoted' ? 'success' : 'active'}
          />
          <span style={{ fontSize: 12, color: '#8c8c8c', minWidth: 32 }}>{v}%</span>
        </div>
      ),
    },
    {
      title: 'Version Constraint',
      dataIndex: 'version_constraint',
      key: 'version_constraint',
      ellipsis: true,
      render: (v: string) => v || <span style={{ color: '#bfbfbf' }}>—</span>,
    },
    {
      title: 'Targeting',
      key: 'targeting',
      width: 200,
      render: (_: unknown, cr: CanaryRelease) => {
        const ips = cr.ip_selector ?? []
        const tags = cr.tag_selector ?? []
        if (ips.length === 0 && tags.length === 0) {
          return <span style={{ color: '#bfbfbf' }}>All hosts</span>
        }
        return (
          <Space size={2} wrap>
            {ips.map((ip, i) => (
              <Tag key={`ip-${i}`} color="geekblue" style={{ marginInlineEnd: 0 }}>{ip}</Tag>
            ))}
            {tags.map((t, i) => (
              <Tag key={`tag-${i}`} color="cyan" style={{ marginInlineEnd: 0 }}>
                {t.name}={t.value}
              </Tag>
            ))}
          </Space>
        )
      },
    },
    {
      title: 'Created By',
      dataIndex: 'created_by',
      key: 'created_by',
      width: 110,
      render: (v: string) => v || <span style={{ color: '#bfbfbf' }}>—</span>,
    },
    {
      title: 'Updated',
      dataIndex: 'updated_at',
      key: 'updated_at',
      width: 170,
      render: (v: string) => {
        const d = new Date(v)
        return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')} ${String(d.getHours()).padStart(2,'0')}:${String(d.getMinutes()).padStart(2,'0')}:${String(d.getSeconds()).padStart(2,'0')}`
      },
    },
    {
      title: 'Actions',
      key: 'actions',
      width: 290,
      render: (_: unknown, cr: CanaryRelease) => {
        const isActive = cr.status === 'rolling' || cr.status === 'paused'
        return (
          <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
            {/* ── Operational area (fixed width) ─────────────────── */}
            <div style={{ width: 124, display: 'flex', alignItems: 'center', gap: 2 }}>
              {/* Rollout percent badge */}
              {isActive ? (
                <Tooltip title="Adjust rollout %">
                  <Tag
                    color="blue"
                    style={{
                      cursor: 'pointer',
                      fontWeight: 600,
                      fontSize: 12,
                      padding: '0 4px',
                      borderRadius: 10,
                      userSelect: 'none',
                      width: 46,
                      textAlign: 'left',
                      marginInlineEnd: 0,
                      display: 'inline-block',
                    }}
                    onClick={() => { setPercentValue(cr.rollout_percent); setPercentTarget(cr) }}
                  >
                    {cr.rollout_percent}%
                  </Tag>
                </Tooltip>
              ) : (
                <span style={{ display: 'inline-block', width: 46 }} />
              )}

              {/* Pause / Resume */}
              {cr.status === 'rolling' ? (
                <Tooltip title="Pause rollout">
                  <Button
                    size="small"
                    icon={<PauseCircleOutlined />}
                    style={{ color: '#fa8c16', borderColor: '#fa8c16' }}
                    onClick={() => handleAction('pause', cr)}
                  />
                </Tooltip>
              ) : cr.status === 'paused' ? (
                <Tooltip title="Resume rollout">
                  <Button
                    size="small"
                    type="primary"
                    icon={<PlayCircleOutlined />}
                    style={{ backgroundColor: '#13c2c2', borderColor: '#13c2c2' }}
                    onClick={() => handleAction('resume', cr)}
                  />
                </Tooltip>
              ) : (
                <span style={{ display: 'inline-block', width: 24 }} />
              )}

              {/* Promote */}
              {isActive ? (
                <Tooltip title="Promote to stable">
                  <Button
                    size="small"
                    type="primary"
                    icon={<CheckCircleOutlined />}
                    style={{ backgroundColor: '#52c41a', borderColor: '#52c41a' }}
                    onClick={() => { setConfirmInput(''); setConfirmAction({ action: 'promote', cr }) }}
                  />
                </Tooltip>
              ) : (
                <span style={{ display: 'inline-block', width: 24 }} />
              )}

              {/* Abort */}
              {isActive ? (
                <Tooltip title="Abort">
                  <Button
                    size="small"
                    type="primary"
                    danger
                    icon={<StopOutlined />}
                    onClick={() => { setConfirmInput(''); setConfirmAction({ action: 'abort', cr }) }}
                  />
                </Tooltip>
              ) : (
                <span style={{ display: 'inline-block', width: 24 }} />
              )}
            </div>

            {/* ── Divider ────────────────────────────────────────── */}
            <span style={{ borderLeft: '1px solid #d9d9d9', height: 16, margin: '0 1px', flexShrink: 0 }} />

            {/* ── Utility area (always visible, always aligned) ──── */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
              {/* Edit — only for active canaries */}
              {isActive ? (
                <Tooltip title="Edit canary">
                  <Button
                    size="small"
                    type="text"
                    icon={<EditOutlined style={{ color: '#1677ff' }} />}
                    onClick={() => openEdit(cr)}
                  />
                </Tooltip>
              ) : (
                <span style={{ display: 'inline-block', width: 24 }} />
              )}

              {/* Detail */}
              <Tooltip title="View config detail">
                <Button
                  size="small"
                  type="text"
                  icon={<EyeOutlined style={{ color: '#595959' }} />}
                  onClick={() => openDetail(cr)}
                />
              </Tooltip>

              {/* Stats */}
              <Tooltip title="View stats">
                <Button
                  size="small"
                  type="text"
                  icon={<BarChartOutlined style={{ color: '#722ed1' }} />}
                  onClick={() => openStats(cr)}
                />
              </Tooltip>
            </div>
          </div>
        )
      },
    },
  ]

  const canariesCount = ((Form.useWatch('canaries', form) as unknown[]) ?? []).length || 1
  const drawerWidth = Math.min(80 + canariesCount * 560, 1800)

  return (
    <div>
      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <div>
          <span style={{ fontSize: 16, fontWeight: 600 }}>Canary Releases</span>
          <span style={{ marginLeft: 8, fontSize: 12, color: '#8c8c8c' }}>
            Gradually roll out config changes to a subset of agents
          </span>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={fetchCanaries} loading={loading}>Refresh</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
            New Canary
          </Button>
        </Space>
      </div>

      <Table
        dataSource={canaries}
        columns={columns}
        rowKey={r => `${r.config_type}/${r.config_name}`}
        loading={loading}
        size="small"
        pagination={{ pageSize: 20, showSizeChanger: false }}
      />

      {/* ── Create drawer ── */}
      <Drawer
        title="New Canary Release"
        open={createOpen}
        onClose={() => { setCreateOpen(false); form.resetFields() }}
        width={drawerWidth}
        styles={{ body: { padding: 16 } }}
        footer={
          <Space style={{ float: 'right' }}>
            <Button onClick={() => { setCreateOpen(false); form.resetFields() }}>Cancel</Button>
            <Button type="primary" loading={createLoading} onClick={() => form.submit()}>
              Create
            </Button>
          </Space>
        }
      >
        <Form
          form={form}
          layout="vertical"
          onFinish={handleCreate}
          initialValues={{ canaries: [{ config_type: 'pipeline', rollout_percent: 10 }] }}
        >
          <Form.List name="canaries">
            {(fields, { add, remove }) => (
              <div style={{ display: 'flex', flexDirection: 'row', gap: 12, alignItems: 'flex-start', overflowX: 'auto', paddingBottom: 4 }}>
                {fields.map(field => (
                  <CanaryFormItem
                    key={field.key}
                    field={field}
                    pipelineConfigs={pipelineConfigs}
                    instanceConfigs={instanceConfigs}
                    groups={groups}
                    form={form}
                    onRemove={() => remove(field.name)}
                    canRemove={fields.length > 1}
                  />
                ))}
                <Button
                  type="dashed"
                  onClick={() => add({ config_type: 'pipeline', rollout_percent: 10 })}
                  icon={<PlusOutlined />}
                  style={{ height: '100%', minHeight: 120, width: 48, flexShrink: 0, writingMode: 'vertical-rl', letterSpacing: 2 }}
                >
                  Add
                </Button>
              </div>
            )}
          </Form.List>
        </Form>
      </Drawer>

      {/* ── Promote / Abort confirm modal ── */}
      {(() => {
        if (!confirmAction) return null
        const isAbort = confirmAction.action === 'abort'
        const configName = confirmAction.cr.config_name
        const matched = confirmInput === configName
        const handleOk = async () => {
          if (!matched) return
          setConfirmLoading(true)
          try {
            await handleAction(confirmAction.action, confirmAction.cr)
            setConfirmAction(null)
          } finally {
            setConfirmLoading(false)
          }
        }
        return (
          <Modal
            title={
              <span style={{ color: isAbort ? '#ff4d4f' : '#52c41a' }}>
                {isAbort ? '⚠ Confirm Abort' : '✔ Confirm Promote'}
              </span>
            }
            open
            onOk={handleOk}
            onCancel={() => setConfirmAction(null)}
            okText={isAbort ? 'Abort' : 'Promote'}
            cancelText="Cancel"
            okButtonProps={{ danger: isAbort, disabled: !matched, loading: confirmLoading, ...(isAbort ? {} : { style: { backgroundColor: '#52c41a', borderColor: '#52c41a' } }) }}
            closable={!confirmLoading}
            maskClosable={false}
            destroyOnClose
          >
            <Alert
              type={isAbort ? 'error' : 'warning'}
              showIcon
              message={isAbort
                ? <>Agents will <strong>fall back</strong> to the stable config. This cannot be undone.</>
                : <>The canary config will <strong>replace the stable version</strong> for all agents. This cannot be undone.</>
              }
              style={{ marginBottom: 16 }}
            />
            <p style={{ marginBottom: 8 }}>
              Type <Text code>{configName}</Text> to confirm:
            </p>
            <Input
              value={confirmInput}
              onChange={e => setConfirmInput(e.target.value)}
              placeholder={configName}
              onPressEnter={handleOk}
              autoFocus
              status={confirmInput && !matched ? 'error' : undefined}
            />
            {confirmInput && !matched && (
              <div style={{ color: '#ff4d4f', fontSize: 12, marginTop: 4 }}>
                Input does not match the config name
              </div>
            )}
          </Modal>
        )
      })()}

      {/* ── Percent edit modal ── */}
      <Modal
        title={`Adjust Rollout — ${percentTarget?.config_name}`}
        open={!!percentTarget}
        onCancel={() => setPercentTarget(null)}
        onOk={handleSetPercent}
        okText="Update"
        confirmLoading={percentLoading}
        destroyOnClose
      >
        <div style={{ padding: '24px 0 8px' }}>
          {/* Current value display */}
          <div style={{ textAlign: 'center', marginBottom: 20 }}>
            <span style={{
              fontSize: 48,
              fontWeight: 700,
              fontVariantNumeric: 'tabular-nums',
              lineHeight: 1,
              background: 'linear-gradient(135deg, #1677ff 0%, #69b1ff 100%)',
              WebkitBackgroundClip: 'text',
              WebkitTextFillColor: 'transparent',
            }}>
              {percentValue}
            </span>
            <span style={{ fontSize: 22, fontWeight: 600, color: '#1677ff', marginLeft: 2 }}>%</span>
          </div>

          <Row align="middle" gutter={16}>
            <Col flex="auto">
              <Slider
                min={0} max={100}
                value={percentValue}
                onChange={setPercentValue}
                marks={{ 0: '0%', 25: '25%', 50: '50%', 75: '75%', 100: '100%' }}
                tooltip={{ formatter: (v) => `${v}%` }}
                styles={{
                  track: { background: 'linear-gradient(90deg, #1677ff, #69b1ff)' },
                  handle: { borderColor: '#1677ff', width: 18, height: 18, marginTop: -7 },
                }}
              />
            </Col>
            <Col>
              <InputNumber
                min={0} max={100}
                value={percentValue}
                onChange={v => setPercentValue(v ?? 0)}
                addonAfter="%"
                style={{ width: 90 }}
                size="large"
              />
            </Col>
          </Row>
        </div>
      </Modal>

      {/* ── Detail drawer ── */}
      <Drawer
        title={`Config Detail — ${detailTarget?.config_name}`}
        open={!!detailTarget}
        onClose={() => setDetailTarget(null)}
        width={900}
        extra={
          detailTarget && (
            <Space>
              <Tag color={TYPE_COLORS[detailTarget.config_type] ?? 'default'}>{detailTarget.config_type}</Tag>
              <StatusTag status={detailTarget.status} />
            </Space>
          )
        }
      >
        {detailTarget && (
          <>
            <Descriptions bordered size="small" column={1} style={{ marginBottom: 16 }}>
              <Descriptions.Item label="Version">{detailTarget.canary_version || <span style={{ color: '#bfbfbf' }}>—</span>}</Descriptions.Item>
              <Descriptions.Item label="Rollout">
                <Progress
                  percent={detailTarget.rollout_percent}
                  size="small"
                  style={{ width: 160, marginBottom: 0 }}
                  status={detailTarget.status === 'paused' ? 'exception' : detailTarget.status === 'promoted' ? 'success' : 'active'}
                />
              </Descriptions.Item>
              {detailTarget.version_constraint && (
                <Descriptions.Item label="Version Constraint">{detailTarget.version_constraint}</Descriptions.Item>
              )}
              {(detailTarget.ip_selector ?? []).length > 0 && (
                <Descriptions.Item label="IP Selector">
                  <Space wrap size={4}>
                    {detailTarget.ip_selector!.map((ip, i) => (
                      <Tag key={i} color="geekblue" style={{ marginInlineEnd: 0 }}>{ip}</Tag>
                    ))}
                  </Space>
                </Descriptions.Item>
              )}
              {(detailTarget.tag_selector ?? []).length > 0 && (
                <Descriptions.Item label="Tag Selector">
                  <Space wrap size={4}>
                    {detailTarget.tag_selector!.map((t, i) => (
                      <Tag key={i} color="cyan" style={{ marginInlineEnd: 0 }}>{t.name}={t.value}</Tag>
                    ))}
                  </Space>
                </Descriptions.Item>
              )}
              <Descriptions.Item label="Created By">{detailTarget.created_by || <span style={{ color: '#bfbfbf' }}>—</span>}</Descriptions.Item>
              <Descriptions.Item label="Updated">{(() => { const d = new Date(detailTarget.updated_at); return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')} ${String(d.getHours()).padStart(2,'0')}:${String(d.getMinutes()).padStart(2,'0')}:${String(d.getSeconds()).padStart(2,'0')}` })()}</Descriptions.Item>
            </Descriptions>
            <Tabs
              activeKey={detailTab}
              onChange={k => setDetailTab(k as 'content' | 'diff')}
              size="small"
              style={{ marginBottom: 0 }}
              items={[
                {
                  key: 'content',
                  label: 'Canary Content',
                  children: (
                    <LineNumberedEditor
                      value={jsonToYaml(detailTarget.canary_detail ?? '')}
                      onChange={() => {}}
                      style={{ height: 560 }}
                      readOnly
                    />
                  ),
                },
                {
                  key: 'diff',
                  label: (
                    <span>
                      Diff with Stable
                      {stableLoading && <Spin size="small" style={{ marginLeft: 6 }} />}
                    </span>
                  ),
                  children: stableLoading ? (
                    <div style={{ textAlign: 'center', padding: 40 }}><Spin /></div>
                  ) : (
                    <DiffViewer
                      oldText={jsonToYaml(stableDetail)}
                      newText={jsonToYaml(detailTarget.canary_detail ?? '')}
                      oldLabel={`Stable (${detailTarget.config_name})`}
                      newLabel={`Canary v${detailTarget.canary_version}`}
                      style={{ height: 560 }}
                    />
                  ),
                },
              ]}
            />
          </>
        )}
      </Drawer>

      {/* ── Stats drawer ── */}
      <Drawer
        title={`Canary Stats — ${statsTarget?.config_name}`}
        open={statsOpen}
        onClose={() => setStatsOpen(false)}
        width={1100}
        extra={
          <Button
            icon={<ReloadOutlined />}
            size="small"
            onClick={() => {
              if (!statsTarget) return
              if (statsTab === 'summary') openStats(statsTarget)
              else loadCanaryAgents(statsTarget)
            }}
            loading={statsTab === 'summary' ? statsLoading : agentsLoading}
          >
            Refresh
          </Button>
        }
      >
        <Tabs
          activeKey={statsTab}
          onChange={handleStatsTabChange}
          items={[
            {
              key: 'summary',
              label: 'Summary',
              children: stats ? (
                <>
                  <Descriptions bordered size="small" column={1} style={{ marginBottom: 24 }}>
                    <Descriptions.Item label="Config">{stats.config_name}</Descriptions.Item>
                    <Descriptions.Item label="Type"><Tag color={TYPE_COLORS[stats.config_type] ?? 'default'}>{stats.config_type}</Tag></Descriptions.Item>
                    <Descriptions.Item label="Status"><StatusTag status={stats.status} /></Descriptions.Item>
                    <Descriptions.Item label="Rollout Target">
                      <Progress percent={stats.rollout_percent} size="small" style={{ width: 160, marginBottom: 0 }} />
                    </Descriptions.Item>
                  </Descriptions>

                  <Row gutter={16}>
                    <Col span={6}>
                      <Statistic
                        title="Canary Agents"
                        value={stats.canary_agents}
                        valueStyle={{ color: '#1677ff' }}
                      />
                    </Col>
                    <Col span={6}>
                      <Statistic
                        title="Stable Agents"
                        value={stats.stable_agents}
                        valueStyle={{ color: '#52c41a' }}
                      />
                    </Col>
                    <Col span={6}>
                      <Statistic
                        title="Not Targeted"
                        value={stats.not_targeted}
                        valueStyle={{ color: '#fa8c16' }}
                        suffix={<Tooltip title="Agents outside the canary's IP / tag / version scope — they always receive the stable config regardless of rollout %"><span style={{ fontSize: 12 }}>ℹ</span></Tooltip>}
                      />
                    </Col>
                    <Col span={6}>
                      <Statistic
                        title="Unknown"
                        value={stats.unknown_agents}
                        valueStyle={{ color: '#8c8c8c' }}
                        suffix={<Tooltip title="Agents without a stable identifier (InstanceID/Hostid/Hostname) — cannot be bucketed"><span style={{ fontSize: 12 }}>ℹ</span></Tooltip>}
                      />
                    </Col>
                  </Row>

                  <div style={{ marginTop: 24 }}>
                    {/* Progress is over targeted agents (canary + stable) to reflect the meaningful population */}
                    {(() => {
                      const targeted = stats.canary_agents + stats.stable_agents
                      const pct = targeted > 0 ? Math.round((stats.canary_agents / targeted) * 100) : 0
                      return (
                        <>
                          <Progress
                            percent={pct}
                            format={() => `${stats.canary_agents} / ${targeted} targeted agents`}
                            strokeColor="#1677ff"
                          />
                          <div style={{ marginTop: 8, fontSize: 12, color: '#8c8c8c' }}>
                            Canary distribution among targeted agents (InstanceID bucketing).
                            {stats.not_targeted > 0 && (
                              <span style={{ color: '#fa8c16' }}> {stats.not_targeted} agent{stats.not_targeted !== 1 ? 's' : ''} outside IP/tag/version scope.</span>
                            )}
                          </div>
                        </>
                      )
                    })()}
                  </div>
                </>
              ) : (
                <div style={{ textAlign: 'center', padding: 40, color: '#8c8c8c' }}>
                  {statsLoading ? <Spin /> : 'No data'}
                </div>
              ),
            },
            {
              key: 'agents',
              label: 'Agents',
              children: (
                <>
                  {/* Toolbar */}
                  <Space style={{ marginBottom: 10, display: 'flex', flexWrap: 'wrap' }}>
                    <Input
                      prefix={<SearchOutlined style={{ color: '#bfbfbf' }} />}
                      placeholder="Search Instance ID / IP / Hostname / Type…"
                      value={agentsSearch}
                      onChange={e => { setAgentsSearch(e.target.value); setAgentsPage(1) }}
                      allowClear
                      style={{ width: 380 }}
                    />
                    <Select
                      value={agentsBucketFilter}
                      onChange={v => { setAgentsBucketFilter(v); setAgentsPage(1) }}
                      style={{ width: 160 }}
                      options={[
                        { label: 'All Buckets', value: 'all' },
                        { label: 'Canary', value: 'canary' },
                        { label: 'Stable', value: 'stable' },
                        { label: 'Not Targeted', value: 'not_targeted' },
                        { label: 'Unknown', value: 'unknown' },
                      ]}
                    />
                    <span style={{ fontSize: 12, color: '#8c8c8c' }}>
                      {filteredAgents.length !== agentsData.length
                        ? `${filteredAgents.length} / ${agentsData.length} agents`
                        : `${agentsData.length} agents total`}
                    </span>
                  </Space>
                  <Table<CanaryAgent>
                    dataSource={filteredAgents}
                    rowKey="instance_id"
                    loading={agentsLoading}
                    size="small"
                    scroll={{ x: 1060 }}
                    pagination={{
                      current: agentsPage,
                      pageSize: agentsPageSize,
                      showSizeChanger: true,
                      pageSizeOptions: ['20', '50', '100', '500'],
                      showTotal: (total, range) => `${range[0]}-${range[1]} / ${total}`,
                      onChange: (page, size) => { setAgentsPage(page); setAgentsPageSize(size) },
                    }}
                    columns={[
                      {
                        title: 'Instance ID',
                        dataIndex: 'instance_id',
                        key: 'instance_id',
                        width: 280,
                        ellipsis: true,
                        render: (id: string) => <Text copyable code style={{ fontSize: 11 }}>{id}</Text>,
                      },
                      {
                        title: 'Type',
                        dataIndex: 'agent_type',
                        key: 'agent_type',
                        width: 110,
                        ellipsis: true,
                      },
                      {
                        title: 'IP',
                        dataIndex: 'ip',
                        key: 'ip',
                        width: 140,
                      },
                      {
                        title: 'Hostname',
                        dataIndex: 'hostname',
                        key: 'hostname',
                        ellipsis: true,
                        width: 200,
                      },
                      {
                        title: 'Version',
                        dataIndex: 'version',
                        key: 'version',
                        width: 100,
                      },
                      {
                        title: 'Tags',
                        key: 'tags',
                        width: 220,
                        render: (_: unknown, record: CanaryAgent) => {
                          const tags: AgentTag[] = record.tags ?? []
                          if (tags.length === 0) return <span style={{ color: '#bfbfbf', fontSize: 12 }}>—</span>
                          return (
                            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 2 }}>
                              {tags.map((t, i) => (
                                <Tag key={i} style={{ fontSize: 11, margin: 0 }}>{t.name} = {t.value}</Tag>
                              ))}
                            </div>
                          )
                        },
                      },
                      {
                        title: 'Bucket',
                        dataIndex: 'bucket',
                        key: 'bucket',
                        width: 90,
                        fixed: 'right' as const,
                        filters: [
                          { text: 'Canary', value: 'canary' },
                          { text: 'Stable', value: 'stable' },
                          { text: 'Not Targeted', value: 'not_targeted' },
                          { text: 'Unknown', value: 'unknown' },
                        ],
                        onFilter: (value, record) => record.bucket === value,
                        render: (bucket: string) => {
                          const colorMap: Record<string, string> = {
                            canary: 'blue',
                            stable: 'green',
                            not_targeted: 'orange',
                            unknown: 'default',
                          }
                          const labelMap: Record<string, string> = {
                            canary: 'Canary',
                            stable: 'Stable',
                            not_targeted: 'Not Targeted',
                            unknown: 'Unknown',
                          }
                          return <Tag color={colorMap[bucket] ?? 'default'}>{labelMap[bucket] ?? bucket}</Tag>
                        },
                      },
                    ]}
                  />
                </>
              ),
            },
          ]}
        />
      </Drawer>

      {/* ── Edit drawer ── */}
      <Drawer
        title={`Edit Canary — ${editTarget?.config_name}`}
        open={editOpen}
        onClose={() => { setEditOpen(false); setEditTarget(null); editForm.resetFields() }}
        width={600}
        footer={
          <Space style={{ float: 'right' }}>
            <Button onClick={() => { setEditOpen(false); setEditTarget(null); editForm.resetFields() }}>Cancel</Button>
            <Button type="primary" loading={editLoading} onClick={() => editForm.submit()}>
              Save
            </Button>
          </Space>
        }
      >
        <Form
          form={editForm}
          layout="vertical"
          onFinish={handleEdit}
        >
          <Form.Item
            label="Canary Config Detail (YAML / JSON)"
            name="canary_detail"
            rules={[{ required: true, message: 'Config content is required' }]}
          >
            <LineNumberedEditor
              value={editForm.getFieldValue('canary_detail') ?? ''}
              onChange={v => editForm.setFieldValue('canary_detail', v)}
              style={{ height: 280 }}
            />
          </Form.Item>
          <Form.Item
            label="Rollout %"
            name="rollout_percent"
            rules={[{ required: true }, { type: 'number', min: 0, max: 100 }]}
          >
            <InputNumber min={0} max={100} addonAfter="%" style={{ width: 140 }} />
          </Form.Item>
          <Form.Item
            label="Version Constraint"
            name="version_constraint"
            tooltip="Semver range (e.g. >=2.0.0). Leave empty to apply to all versions."
          >
            <Input placeholder="e.g. >=2.0.0 (optional)" />
          </Form.Item>
          <Form.Item
            label="IP Selector"
            name="ip_selector"
            tooltip="Restrict the canary to matching agent IPs. One rule per line or comma-separated."
          >
            <Input.TextArea
              rows={3}
              placeholder={'e.g.\n10.0.0.0/24\n192.168.1.10-20\n10.1.2.3'}
            />
          </Form.Item>
          <Form.Item
            label="Tag Selector"
            tooltip="Restrict the canary to agents carrying at least one of these tags (ANY-match)."
          >
            <Form.List name="tag_selector">
              {(fields, { add, remove }) => (
                <>
                  {fields.map(field => (
                    <Space key={field.key} align="baseline" style={{ display: 'flex', marginBottom: 8 }}>
                      <Form.Item
                        {...field}
                        name={[field.name, 'name']}
                        rules={[{ required: true, message: 'Tag name required' }]}
                        style={{ marginBottom: 0 }}
                      >
                        <Input placeholder="tag name" style={{ width: 160 }} />
                      </Form.Item>
                      <span style={{ color: '#bfbfbf' }}>=</span>
                      <Form.Item
                        {...field}
                        name={[field.name, 'value']}
                        style={{ marginBottom: 0 }}
                      >
                        <Input placeholder="tag value" style={{ width: 160 }} />
                      </Form.Item>
                      <MinusCircleOutlined onClick={() => remove(field.name)} style={{ color: '#999' }} />
                    </Space>
                  ))}
                  <Button type="dashed" onClick={() => add()} block icon={<PlusOutlined />}>
                    Add Tag
                  </Button>
                </>
              )}
            </Form.List>
          </Form.Item>
        </Form>
      </Drawer>
    </div>
  )
}
