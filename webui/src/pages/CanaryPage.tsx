import { useEffect, useState, useCallback } from 'react'
import {
  Table, Button, Modal, Form, Input, InputNumber, Slider, Space, message,
  Tag, Drawer, Descriptions, Statistic, Row, Col, Tooltip, Select, Progress,
  Popconfirm, Tabs, Spin, AutoComplete, Card, Alert,
} from 'antd'
import {
  PlusOutlined, PauseCircleOutlined, PlayCircleOutlined,
  CheckCircleOutlined, StopOutlined, BarChartOutlined, ReloadOutlined,
  MinusCircleOutlined, EyeOutlined, EditOutlined,
} from '@ant-design/icons'
import LineNumberedEditor from '../components/LineNumberedEditor'
import DiffViewer from '../components/DiffViewer'
import type { CanaryRelease, CanaryStats, AgentGroup } from '../api'
import {
  listCanaries, createCanary, updateCanary, setCanaryPercent,
  pauseCanary, resumeCanary, promoteCanary, abortCanary, getCanaryStats,
  listPipelineConfigs, listInstanceConfigs,
  listGroups, createPipelineConfig, createInstanceConfig, addGroupConfig,
} from '../api'
import type { Config } from '../api'

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
  const [loading, setLoading] = useState(false)

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

  // Detail drawer
  const [detailTarget, setDetailTarget] = useState<CanaryRelease | null>(null)
  const [detailTab, setDetailTab] = useState<'content' | 'diff'>('content')
  const [stableDetail, setStableDetail] = useState<string>('')
  const [stableLoading, setStableLoading] = useState(false)

  // Percent edit modal
  const [percentTarget, setPercentTarget] = useState<CanaryRelease | null>(null)
  const [percentValue, setPercentValue] = useState(0)
  const [percentLoading, setPercentLoading] = useState(false)

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
    const errors: string[] = []
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
          canary_detail: entry.canary_detail,
          rollout_percent: entry.rollout_percent ?? 0,
          version_constraint: entry.version_constraint ?? '',
          ip_selector: ipList.length ? ipList : undefined,
          tag_selector: tagList.length ? tagList : undefined,
        })
        successCount++
      } catch (e: any) {
        errors.push(`${entry.config_name}: ${e?.response?.data?.message ?? 'failed'}`)
      }
    }
    setCreateLoading(false)
    if (errors.length === 0) {
      message.success(successCount === 1 ? 'Canary release created' : `${successCount} canary releases created`)
      setCreateOpen(false)
      form.resetFields()
      fetchCanaries()
    } else if (successCount > 0) {
      message.warning(`${successCount} created, ${errors.length} failed: ${errors.join('; ')}`)
      fetchCanaries()
    } else {
      message.error(`Failed: ${errors.join('; ')}`)
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
      canary_detail: cr.canary_detail ?? '',
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
        canary_detail: values.canary_detail,
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

  const columns = [
    {
      title: 'Config Name',
      dataIndex: 'config_name',
      key: 'config_name',
      ellipsis: true,
    },
    {
      title: 'Type',
      dataIndex: 'config_type',
      key: 'config_type',
      width: 100,
      render: (v: string) => <Tag>{v}</Tag>,
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
      render: (v: number, cr: CanaryRelease) => (
        <Space>
          <Progress
            percent={v}
            size="small"
            style={{ width: 80, marginBottom: 0 }}
            status={cr.status === 'paused' ? 'exception' : cr.status === 'promoted' ? 'success' : 'active'}
          />
          <span style={{ fontSize: 12, color: '#8c8c8c' }}>{v}%</span>
        </Space>
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
      render: (v: string) => new Date(v).toLocaleString(),
    },
    {
      title: 'Actions',
      key: 'actions',
      width: 260,
      render: (_: unknown, cr: CanaryRelease) => {
        const isActive = cr.status === 'rolling' || cr.status === 'paused'
        return (
          <Space size={4}>
            {/* Adjust percent — only when rolling or paused */}
            {isActive && (
              <Tooltip title="Adjust rollout %">
                <Button
                  size="small"
                  onClick={() => { setPercentValue(cr.rollout_percent); setPercentTarget(cr) }}
                >
                  {cr.rollout_percent}%
                </Button>
              </Tooltip>
            )}

            {/* Pause / Resume */}
            {cr.status === 'rolling' && (
              <Tooltip title="Pause rollout">
                <Button size="small" icon={<PauseCircleOutlined />} onClick={() => handleAction('pause', cr)} />
              </Tooltip>
            )}
            {cr.status === 'paused' && (
              <Tooltip title="Resume rollout">
                <Button size="small" icon={<PlayCircleOutlined />} onClick={() => handleAction('resume', cr)} />
              </Tooltip>
            )}

            {/* Promote */}
            {isActive && (
              <Popconfirm
                title="Promote canary to stable?"
                description="The canary config will replace the stable version for all agents."
                onConfirm={() => handleAction('promote', cr)}
                okText="Promote"
                okButtonProps={{ danger: false }}
              >
                <Tooltip title="Promote to stable">
                  <Button size="small" icon={<CheckCircleOutlined />} />
                </Tooltip>
              </Popconfirm>
            )}

            {/* Abort */}
            {isActive && (
              <Popconfirm
                title="Abort canary release?"
                description="Agents will fall back to the stable config version."
                onConfirm={() => handleAction('abort', cr)}
                okText="Abort"
                okButtonProps={{ danger: true }}
              >
                <Tooltip title="Abort">
                  <Button size="small" icon={<StopOutlined />} danger />
                </Tooltip>
              </Popconfirm>
            )}

            {/* Detail */}
            <Tooltip title="View config detail">
              <Button size="small" icon={<EyeOutlined />} onClick={() => openDetail(cr)} />
            </Tooltip>

            {/* Edit — only for active canaries */}
            {isActive && (
              <Tooltip title="Edit canary">
                <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(cr)} />
              </Tooltip>
            )}

            {/* Stats */}
            <Tooltip title="View stats">
              <Button size="small" icon={<BarChartOutlined />} onClick={() => openStats(cr)} />
            </Tooltip>
          </Space>
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
        <div style={{ padding: '24px 0' }}>
          <Row align="middle" gutter={16}>
            <Col flex="auto">
              <Slider
                min={0} max={100}
                value={percentValue}
                onChange={setPercentValue}
                marks={{ 0: '0%', 25: '25%', 50: '50%', 75: '75%', 100: '100%' }}
              />
            </Col>
            <Col>
              <InputNumber
                min={0} max={100}
                value={percentValue}
                onChange={v => setPercentValue(v ?? 0)}
                addonAfter="%"
                style={{ width: 90 }}
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
              <Tag>{detailTarget.config_type}</Tag>
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
              <Descriptions.Item label="Updated">{new Date(detailTarget.updated_at).toLocaleString()}</Descriptions.Item>
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
                      value={detailTarget.canary_detail ?? ''}
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
                      oldText={stableDetail}
                      newText={detailTarget.canary_detail ?? ''}
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
        width={480}
        extra={
          <Button
            icon={<ReloadOutlined />}
            size="small"
            onClick={() => statsTarget && openStats(statsTarget)}
            loading={statsLoading}
          >
            Refresh
          </Button>
        }
      >
        {stats ? (
          <>
            <Descriptions bordered size="small" column={1} style={{ marginBottom: 24 }}>
              <Descriptions.Item label="Config">{stats.config_name}</Descriptions.Item>
              <Descriptions.Item label="Type"><Tag>{stats.config_type}</Tag></Descriptions.Item>
              <Descriptions.Item label="Status"><StatusTag status={stats.status} /></Descriptions.Item>
              <Descriptions.Item label="Rollout Target">
                <Progress percent={stats.rollout_percent} size="small" style={{ width: 160, marginBottom: 0 }} />
              </Descriptions.Item>
            </Descriptions>

            <Row gutter={16}>
              <Col span={8}>
                <Statistic
                  title="Canary Hosts"
                  value={stats.canary_hosts}
                  valueStyle={{ color: '#1677ff' }}
                />
              </Col>
              <Col span={8}>
                <Statistic
                  title="Stable Hosts"
                  value={stats.stable_hosts}
                  valueStyle={{ color: '#52c41a' }}
                />
              </Col>
              <Col span={8}>
                <Statistic
                  title="Unknown"
                  value={stats.unknown_hosts}
                  valueStyle={{ color: '#8c8c8c' }}
                  suffix={<Tooltip title="Hosts without a stable identifier (Hostid/Hostname) — cannot be bucketed"><span style={{ fontSize: 12 }}>ℹ</span></Tooltip>}
                />
              </Col>
            </Row>

            <div style={{ marginTop: 24 }}>
              <Progress
                percent={stats.total_hosts > 0
                  ? Math.round((stats.canary_hosts / stats.total_hosts) * 100)
                  : 0}
                success={{ percent: stats.total_hosts > 0 ? Math.round((stats.canary_hosts / stats.total_hosts) * 100) : 0 }}
                format={() => `${stats.canary_hosts} / ${stats.total_hosts} hosts`}
                strokeColor="#1677ff"
              />
              <div style={{ marginTop: 8, fontSize: 12, color: '#8c8c8c' }}>
                Actual canary distribution based on Hostid bucketing
              </div>
            </div>
          </>
        ) : (
          <div style={{ textAlign: 'center', padding: 40, color: '#8c8c8c' }}>
            {statsLoading ? 'Loading…' : 'No data'}
          </div>
        )}
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
