import { useState, useEffect, lazy, Suspense } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom'
import { Layout, Breadcrumb, Tooltip, Modal, Form, Input, Button, message, Dropdown, Avatar, Space, ConfigProvider, theme as antdTheme, Spin, Steps, Alert, Typography } from 'antd'
import type { MenuProps } from 'antd'
import {
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  DatabaseOutlined,
  TeamOutlined,
  ClusterOutlined,
  ThunderboltOutlined,
  ToolOutlined,
  KeyOutlined,
  LogoutOutlined,
    QuestionCircleOutlined,
  UserOutlined,
  SafetyOutlined,
  AuditOutlined,
  SunOutlined,
  MoonOutlined,
  DesktopOutlined,
  ExperimentOutlined,
  SafetyCertificateOutlined,
  QrcodeOutlined,
} from '@ant-design/icons'
const ConfigsPage = lazy(() => import('./pages/ConfigsPage'))
const GroupsPage  = lazy(() => import('./pages/GroupsPage'))
const AgentsPage  = lazy(() => import('./pages/AgentsPage'))
const UsersPage   = lazy(() => import('./pages/UsersPage'))
const RolesPage   = lazy(() => import('./pages/RolesPage'))
const AuditPage   = lazy(() => import('./pages/AuditPage'))
const LoginPage   = lazy(() => import('./pages/LoginPage'))
const CanaryPage  = lazy(() => import('./pages/CanaryPage'))
import { getAuthStatus, logout, changePassword, getMe, otpSetup, otpEnable, otpDisable, type OTPSetupResponse } from './api'
import { PermissionProvider, useCurrentUser, useLoadMe } from './PermissionContext'

const { Header, Sider, Content } = Layout

const NAV_GROUPS = [
  {
    key: 'collect',
    label: 'Collection',
    items: [
      { path: '/pipeline-configs',  label: 'Pipeline Configs',  icon: <DatabaseOutlined /> },
      { path: '/instance-configs',  label: 'Instance Configs',  icon: <ToolOutlined /> },
      { path: '/onetime-configs',    label: 'Onetime Commands',    icon: <ThunderboltOutlined /> },
      { path: '/canary',            label: 'Canary Releases',   icon: <ExperimentOutlined /> },
    ],
  },
  {
    key: 'manage',
    label: 'Management',
    items: [
      { path: '/agent-groups', label: 'Agent Groups', icon: <TeamOutlined />,  adminOnly: false },
      { path: '/agents',       label: 'Agents',        icon: <ClusterOutlined />, adminOnly: false },
    ],
  },
  {
    key: 'system',
    label: 'System',
    items: [
      { path: '/users',  label: 'User Management', icon: <UserOutlined />,  adminOnly: true },
      { path: '/roles',  label: 'Roles',            icon: <SafetyOutlined />, adminOnly: true },
      { path: '/audit',  label: 'Audit Log',        icon: <AuditOutlined />,  adminOnly: true },
    ],
  },
]

const BREADCRUMB_MAP: Record<string, string[]> = {
  '/pipeline-configs':  ['Collection', 'Pipeline Configs'],
  '/instance-configs':  ['Collection', 'Instance Configs'],
  '/onetime-configs':   ['Collection', 'Onetime Commands'],
  '/canary':            ['Collection', 'Canary Releases'],
  '/agent-groups':      ['Management', 'Agent Groups'],
  '/agents':            ['Management', 'Agents'],
  '/users':             ['System', 'User Management'],
  '/roles':             ['System', 'Roles'],
  '/audit':             ['System', 'Audit Log'],
}

function SideNav({ collapsed, theme }: { collapsed: boolean; theme: 'light' | 'dark' }) {
  const navigate = useNavigate()
  const location = useLocation()
  const currentUser = useCurrentUser()
  const [openGroups, setOpenGroups] = useState(() => new Set(['collect', 'manage', 'system']))
  const navGroups = NAV_GROUPS.map(g => ({
    ...g,
    items: g.items.filter(item => !(item as any).adminOnly || currentUser?.is_admin),
  })).filter(g => g.items.length > 0)

  const toggleGroup = (key: string) =>
    setOpenGroups(prev => {
      const next = new Set(prev)
      next.has(key) ? next.delete(key) : next.add(key)
      return next
    })

  if (collapsed) {
    return (
      <div style={{ padding: '8px 0' }}>
        {navGroups.flatMap(g => g.items).map(item => {
          const isActive = item.path === location.pathname
          return (
            <Tooltip key={item.path} title={item.label} placement="right">
              <div
                onClick={() => navigate(item.path)}
                style={{
                  height: 36,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  cursor: 'pointer',
                  color: isActive ? (theme === 'dark' ? '#8ab4f8' : '#1677ff') : (theme === 'dark' ? '#9aa0a6' : '#595959'),
                  background: isActive ? (theme === 'dark' ? 'rgba(138,180,248,0.12)' : 'rgba(22,119,255,0.1)') : 'transparent',
                  borderRadius: 4,
                  margin: '1px 4px',
                  fontSize: 16,
                }}
              >
                {item.icon}
              </div>
            </Tooltip>
          )
        })}
      </div>
    )
  }

  return (
    <div style={{ padding: '8px 0' }}>
      {navGroups.map(group => {
        const isOpen = openGroups.has(group.key)
        return (
          <div key={group.key}>
            <div
              onClick={() => toggleGroup(group.key)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 5,
                padding: '12px 16px 5px',
                cursor: 'pointer',
                userSelect: 'none',
              }}
            >
              <span style={{
                fontSize: 9,
                color: theme === 'dark' ? '#9aa0a6' : '#bfbfbf',
                display: 'inline-block',
                transition: 'transform 0.2s',
                transform: isOpen ? 'rotate(0deg)' : 'rotate(-90deg)',
              }}>▼</span>
              <span style={{ fontSize: 11, fontWeight: 700, color: theme === 'dark' ? '#9aa0a6' : '#8c8c8c', letterSpacing: '0.06em', textTransform: 'uppercase' }}>
                {group.label}
              </span>
            </div>
            {isOpen && group.items.map(item => {
              const isActive = item.path === location.pathname
              return (
                <div
                  key={item.path}
                  onClick={() => navigate(item.path)}
                  className={isActive ? 'sidenav-item active' : 'sidenav-item'}
                >
                  <span style={{ marginRight: 8, fontSize: 13 }}>{item.icon}</span>
                  {item.label}
                </div>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}

function AppLayout({ onLogout, theme, themeMode, onCycleTheme }: { 
  onLogout: () => void
  theme: 'light' | 'dark'
  themeMode: 'light' | 'dark' | 'system'
  onCycleTheme: () => void
}) {
  const [collapsed, setCollapsed] = useState(false)
  const [changePwdOpen, setChangePwdOpen] = useState(false)
  const [changePwdLoading, setChangePwdLoading] = useState(false)
  const [form] = Form.useForm()
  // TOTP modal state
  const [totpOpen, setTotpOpen] = useState(false)
  const [totpStep, setTotpStep] = useState<'menu' | 'setup' | 'disable'>('menu')
  const [totpSetupData, setTotpSetupData] = useState<OTPSetupResponse | null>(null)
  const [totpLoading, setTotpLoading] = useState(false)
  const [totpError, setTotpError] = useState('')
  const [totpForm] = Form.useForm()
  const [totpDisableForm] = Form.useForm()
  const location = useLocation()
  const crumbs = BREADCRUMB_MAP[location.pathname] ?? []
  const currentUser = useCurrentUser()
  const loadMe = useLoadMe()

  useEffect(() => { loadMe() }, [loadMe])

  const handleChangePwd = async (values: { current: string; newPwd: string; confirm: string }) => {
    setChangePwdLoading(true)
    try {
      await changePassword(values.current, values.newPwd, values.confirm)
      message.success('Password changed successfully')
      setChangePwdOpen(false)
      form.resetFields()
    } catch (e: any) {
      message.error(e?.response?.data?.message ?? 'Failed to change password')
    } finally {
      setChangePwdLoading(false)
    }
  }

  const openTotpModal = () => { setTotpOpen(true); setTotpStep('menu'); setTotpError(''); setTotpSetupData(null) }
  const closeTotpModal = () => { setTotpOpen(false); totpForm.resetFields(); totpDisableForm.resetFields(); setTotpSetupData(null); setTotpError('') }

  const startTotpSetup = async () => {
    setTotpLoading(true); setTotpError('')
    try {
      const data = await otpSetup()
      setTotpSetupData(data)
      setTotpStep('setup')
    } catch (e: any) {
      setTotpError(e?.response?.data?.message ?? 'Failed to generate QR code')
    } finally {
      setTotpLoading(false)
    }
  }

  const confirmTotpEnable = async (values: { otp_code: string }) => {
    setTotpLoading(true); setTotpError('')
    try {
      await otpEnable(values.otp_code)
      message.success('Two-factor authentication enabled')
      closeTotpModal()
    } catch (e: any) {
      setTotpError(e?.response?.data?.message ?? 'Invalid code, please try again')
    } finally {
      setTotpLoading(false)
    }
  }

  const confirmTotpDisable = async (values: { password: string }) => {
    setTotpLoading(true); setTotpError('')
    try {
      await otpDisable(values.password)
      message.success('Two-factor authentication disabled')
      closeTotpModal()
    } catch (e: any) {
      setTotpError(e?.response?.data?.message ?? 'Failed to disable 2FA')
    } finally {
      setTotpLoading(false)
    }
  }

  return (
    <>
    <Layout style={{ height: '100vh', overflow: 'hidden' }}>
      {/* Top navigation bar */}
      <Header style={{
        display: 'flex',
        alignItems: 'center',
        padding: '0 16px',
        background: theme === 'dark' ? '#202124' : '#ffffff',
        borderBottom: theme === 'dark' ? '1px solid #3c4043' : '1px solid #f0f0f0',
        height: 48,
        position: 'sticky',
        top: 0,
        zIndex: 1000,
      }}>
        <div
          onClick={() => setCollapsed(c => !c)}
          style={{
            width: 34, height: 34,
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            cursor: 'pointer',
            color: theme === 'dark' ? '#9aa0a6' : '#595959',
            borderRadius: 4,
            marginRight: 10,
          }}
        >
          {collapsed
            ? <MenuUnfoldOutlined style={{ fontSize: 16 }} />
            : <MenuFoldOutlined  style={{ fontSize: 16 }} />}
        </div>
        <span style={{ fontSize: 15, fontWeight: 700, letterSpacing: '0.02em', userSelect: 'none' }}>
          <span className="brand-title" aria-label="LoongCollector Config Server">
            <span className="brand-title-word-1">LoongCollector</span>
            <span className="brand-title-word-2">Config</span>
            <span className="brand-title-word-3">Server</span>
          </span>
        </span>
        <div style={{ marginLeft: 'auto' }}>
          <Space size={2}>
            <Tooltip title={
                themeMode === 'light'  ? 'Light — click for Dark' :
                themeMode === 'dark'   ? 'Dark — click for System' :
                                        'Follow System — click for Light'
              }>
              <div 
                onClick={onCycleTheme}
                style={{ width: 34, height: 34, display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer', color: theme === 'dark' ? '#9aa0a6' : '#595959', borderRadius: 4 }}
              >
                {themeMode === 'light'  ? <SunOutlined     style={{ fontSize: 16 }} /> :
                 themeMode === 'dark'   ? <MoonOutlined    style={{ fontSize: 16 }} /> :
                                         <DesktopOutlined style={{ fontSize: 16 }} />}
              </div>
            </Tooltip>
            <Tooltip title="Documentation">
              <div
                onClick={() => window.open('https://github.com/alibaba/loongcollector', '_blank', 'noopener,noreferrer')}
                style={{ width: 34, height: 34, display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer', color: theme === 'dark' ? '#9aa0a6' : '#595959', borderRadius: 4 }}
              >
                <QuestionCircleOutlined style={{ fontSize: 16 }} />
              </div>
            </Tooltip>
            <div style={{ width: 1, height: 22, background: theme === 'dark' ? '#5f6368' : '#d9d9d9', margin: '0 8px' }} />
            <ConfigProvider theme={{ algorithm: theme === 'dark' ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm }}>
            <Dropdown
              menu={{
                items: [
                  {
                    key: 'change-password',
                    icon: <KeyOutlined />,
                    label: 'Change Password',
                    onClick: () => setChangePwdOpen(true),
                  },
                  {
                    key: 'two-factor',
                    icon: <SafetyCertificateOutlined />,
                    label: 'Two-Factor Auth',
                    onClick: openTotpModal,
                  },
                  { type: 'divider' },
                  {
                    key: 'logout',
                    icon: <LogoutOutlined style={theme === 'dark' ? { color: '#ff7875' } : undefined} />,
                    label: <span style={theme === 'dark' ? { color: '#ff7875' } : undefined}>Sign Out</span>,
                    onClick: onLogout,
                    danger: true,
                  },
                ] as MenuProps['items'],
              }}
              trigger={['click']}
              placement="bottomRight"
              align={{ offset: [0, 4] }}
              overlayStyle={{ minWidth: 180 }}
            >
              <Space style={{ cursor: 'pointer', color: theme === 'dark' ? '#e8eaed' : '#262626', fontSize: 13 }}>
                <Avatar size={26} icon={<UserOutlined />} style={{ background: theme === 'dark' ? '#5f6368' : '#1677ff' }} />
                <span style={{ fontWeight: 500 }}>{currentUser?.username ?? '—'}</span>
                {currentUser?.is_admin && (
                  <span style={{
                    fontSize: 10, color: '#fff', background: '#fa8c16',
                    padding: '1px 6px', borderRadius: 10, fontWeight: 700, lineHeight: '16px',
                  }}>ADMIN</span>
                )}
              </Space>
            </Dropdown>
            </ConfigProvider>
          </Space>
        </div>
      </Header>

      <Layout style={{ flex: 1, minHeight: 0, overflow: 'hidden' }}>
        <Sider
          collapsed={collapsed}
          width={220}
          collapsedWidth={52}
          style={{
            background: theme === 'dark' ? '#292a2d' : '#fafafa',
            borderRight: theme === 'dark' ? '1px solid #3c4043' : '1px solid #f0f0f0',
            height: '100%',
            overflowY: 'auto',
          }}
        >
          <SideNav collapsed={collapsed} theme={theme} />
        </Sider>

        <Layout style={{ background: theme === 'dark' ? '#202124' : '#f0f0f0', flexDirection: 'column', flex: 1, minHeight: 0, overflow: 'hidden' }}>
          {/* Breadcrumb bar */}
          <div style={{
            padding: '8px 24px',
            background: theme === 'dark' ? '#292a2d' : '#ffffff',
            borderBottom: theme === 'dark' ? '1px solid #3c4043' : '1px solid #f0f0f0',
            flexShrink: 0,
          }}>
            <Breadcrumb
              items={[{ title: 'Console' }, ...crumbs.map(c => ({ title: c }))]}
              style={{ fontSize: 12, color: theme === 'dark' ? '#9aa0a6' : '#595959' }}
            />
          </div>
          {/* Page content */}
          <Content style={{
            padding: '20px 24px',
            flex: 1,
            overflowY: 'auto',
            overflowX: 'hidden',
          }}>
            <div style={{
              background: theme === 'dark' ? '#292a2d' : '#ffffff',
              borderRadius: 6,
              padding: '20px 24px',
              border: theme === 'dark' ? '1px solid #3c4043' : '1px solid #f0f0f0',
              boxShadow: theme === 'dark' ? '0 1px 6px rgba(0,0,0,.35)' : '0 1px 4px rgba(0,0,0,.08)',
              minHeight: 0,
            }}>
              <Suspense fallback={<div style={{ textAlign: 'center', padding: 60 }}><Spin size="large" /></div>}>
              <div key={location.pathname} className="page-transition">
              <Routes>
                <Route path="/"                  element={<Navigate to="/pipeline-configs" replace />} />
                <Route path="/pipeline-configs"  element={<ConfigsPage tab="pipeline" />} />
                <Route path="/instance-configs"  element={<ConfigsPage tab="instance" />} />
                <Route path="/onetime-configs"   element={<ConfigsPage tab="onetime" />} />
                <Route path="/agent-groups"       element={<GroupsPage />} />
                <Route path="/agents"            element={<AgentsPage />} />
                <Route path="/canary"            element={<CanaryPage />} />
                <Route path="/users"             element={<UsersPage />} />
                <Route path="/roles"             element={<RolesPage />} />
                <Route path="/audit"             element={<AuditPage />} />
              </Routes>
              </div>
              </Suspense>
            </div>
          </Content>
        </Layout>
      </Layout>
    </Layout>

    {/* Change Password Modal */}
    <Modal
      title="Change Password"
      open={changePwdOpen}
      onCancel={() => { setChangePwdOpen(false); form.resetFields() }}
      footer={null}
      destroyOnClose
    >
      <Form form={form} layout="vertical" onFinish={handleChangePwd} requiredMark={false} style={{ marginTop: 16 }}>
        <Form.Item
          label="Current Password"
          name="current"
          rules={[{ required: true, message: 'Required' }]}
        >
          <Input.Password autoFocus />
        </Form.Item>
        <Form.Item
          label="New Password"
          name="newPwd"
          rules={[
            { required: true, message: 'Required' },
            { min: 8, message: 'At least 8 characters' },
          ]}
        >
          <Input.Password />
        </Form.Item>
        <Form.Item
          label="Confirm New Password"
          name="confirm"
          dependencies={['newPwd']}
          rules={[
            { required: true, message: 'Required' },
            ({ getFieldValue }) => ({
              validator(_, value) {
                if (!value || getFieldValue('newPwd') === value) return Promise.resolve()
                return Promise.reject(new Error('Passwords do not match'))
              },
            }),
          ]}
        >
          <Input.Password />
        </Form.Item>
        <Form.Item style={{ marginBottom: 0, marginTop: 8, textAlign: 'right' }}>
          <Button onClick={() => { setChangePwdOpen(false); form.resetFields() }} style={{ marginRight: 8 }}>Cancel</Button>
          <Button type="primary" htmlType="submit" loading={changePwdLoading}>Change Password</Button>
        </Form.Item>
      </Form>
    </Modal>

    {/* TOTP / Two-Factor Auth Modal */}
    <Modal
      title={<Space><SafetyCertificateOutlined />Two-Factor Authentication</Space>}
      open={totpOpen}
      onCancel={closeTotpModal}
      footer={null}
      destroyOnClose
      width={480}
    >
      {totpError && (
        <Alert type="error" message={totpError} showIcon closable onClose={() => setTotpError('')}
          style={{ marginBottom: 16 }} />
      )}

      {/* Menu: choose setup or disable */}
      {totpStep === 'menu' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12, padding: '8px 0' }}>
          <Button
            type="primary"
            icon={<QrcodeOutlined />}
            block
            size="large"
            loading={totpLoading}
            onClick={startTotpSetup}
          >
            Set Up / Reset Two-Factor Auth
          </Button>
          <Button
            danger
            block
            size="large"
            onClick={() => { setTotpStep('disable'); setTotpError('') }}
          >
            Disable Two-Factor Auth
          </Button>
          <Button block onClick={closeTotpModal}>Cancel</Button>
        </div>
      )}

      {/* Setup: show QR code then ask for confirmation code */}
      {totpStep === 'setup' && totpSetupData && (
        <div>
          <Steps
            size="small"
            current={1}
            items={[
              { title: 'Scan QR Code' },
              { title: 'Confirm Code' },
            ]}
            style={{ marginBottom: 20 }}
          />
          <Typography.Paragraph type="secondary" style={{ fontSize: 13, marginBottom: 12 }}>
            Scan the QR code below with Google Authenticator, Authy, or any TOTP app.
            Then enter the 6-digit code to confirm.
          </Typography.Paragraph>
          {totpSetupData.qr_code ? (
            <div className="totp-qr-panel">
              <img
                src={totpSetupData.qr_code}
                alt="TOTP QR Code"
                className="totp-qr-image"
              />
            </div>
          ) : null}
          <Typography.Paragraph
            copyable
            style={{ fontFamily: 'monospace', fontSize: 13, textAlign: 'center', background: theme === 'dark' ? '#3c4043' : '#f5f5f5', color: theme === 'dark' ? '#e8eaed' : '#262626', padding: '6px 10px', borderRadius: 6 }}
          >
            {totpSetupData.secret}
          </Typography.Paragraph>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            Can't scan? Enter the code above manually in your authenticator app.
          </Typography.Text>
          <Form form={totpForm} layout="vertical" onFinish={confirmTotpEnable}
            requiredMark={false} style={{ marginTop: 16 }}>
            <Form.Item
              label="Verification Code"
              name="otp_code"
              rules={[
                { required: true, message: 'Enter the 6-digit code from your app' },
                { pattern: /^\d{6}$/, message: 'Must be exactly 6 digits' },
              ]}
            >
              <Input
                placeholder="123456"
                maxLength={6}
                autoFocus
                style={{ letterSpacing: '0.3em', fontSize: 18, textAlign: 'center' }}
              />
            </Form.Item>
            <Form.Item style={{ marginBottom: 0, textAlign: 'right' }}>
              <Button onClick={() => { setTotpStep('menu'); totpForm.resetFields(); setTotpError('') }} style={{ marginRight: 8 }}>Back</Button>
              <Button type="primary" htmlType="submit" loading={totpLoading}>Enable 2FA</Button>
            </Form.Item>
          </Form>
        </div>
      )}

      {/* Disable: require current password */}
      {totpStep === 'disable' && (
        <Form form={totpDisableForm} layout="vertical" onFinish={confirmTotpDisable}
          requiredMark={false}>
          <Typography.Paragraph type="secondary" style={{ fontSize: 13 }}>
            Enter your current password to disable two-factor authentication.
          </Typography.Paragraph>
          <Form.Item
            label="Current Password"
            name="password"
            rules={[{ required: true, message: 'Password is required' }]}
          >
            <Input.Password autoFocus />
          </Form.Item>
          <Form.Item style={{ marginBottom: 0, textAlign: 'right' }}>
            <Button onClick={() => { setTotpStep('menu'); totpDisableForm.resetFields(); setTotpError('') }} style={{ marginRight: 8 }}>Back</Button>
            <Button danger htmlType="submit" loading={totpLoading}>Disable 2FA</Button>
          </Form.Item>
        </Form>
      )}
    </Modal>
    </>
  )
}

type ThemeMode = 'light' | 'dark' | 'system'

function getSystemTheme(): 'light' | 'dark' {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export default function App() {
  const [authed, setAuthed] = useState<boolean | null>(null) // null = checking

  const [themeMode, setThemeMode] = useState<ThemeMode>(() => {
    const saved = localStorage.getItem('cs-theme')
    if (saved === 'dark' || saved === 'light' || saved === 'system') return saved
    return 'system'
  })

  const [resolvedTheme, setResolvedTheme] = useState<'light' | 'dark'>(() => {
    const saved = localStorage.getItem('cs-theme')
    if (saved === 'dark') return 'dark'
    if (saved === 'light') return 'light'
    return getSystemTheme()
  })

  const cycleTheme = () => {
    setThemeMode(prev => {
      const next: ThemeMode = prev === 'light' ? 'dark' : prev === 'dark' ? 'system' : 'light'
      localStorage.setItem('cs-theme', next)
      setResolvedTheme(next === 'system' ? getSystemTheme() : next)
      return next
    })
  }

  // Re-resolve theme when OS preference changes (only matters when mode is 'system')
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const handler = (e: MediaQueryListEvent) => {
      setThemeMode(prev => {
        if (prev === 'system') setResolvedTheme(e.matches ? 'dark' : 'light')
        return prev
      })
    }
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [])

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', resolvedTheme)
  }, [resolvedTheme])

  const checkAuth = () => {
    getAuthStatus()
      .then(s => setAuthed(s.logged_in))
      .catch(() => setAuthed(false))
  }

  useEffect(() => {
    checkAuth()
    // Listen for 401 events emitted by the axios interceptor.
    const handler = () => setAuthed(false)
    window.addEventListener('cs:unauthorized', handler)
    return () => window.removeEventListener('cs:unauthorized', handler)
  }, [])

  // Dispatch app:ready once auth state is resolved (removes splash screen)
  useEffect(() => {
    if (authed !== null) {
      window.dispatchEvent(new Event('app:ready'))
    }
  }, [authed])

  if (authed === null) return null // brief loading state

  if (!authed) {
    return <Suspense fallback={null}><LoginPage onLoggedIn={() => setAuthed(true)} /></Suspense>
  }

  return (
    <ConfigProvider theme={{
      algorithm: resolvedTheme === 'dark' ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
      token: resolvedTheme === 'dark' ? {
        colorPrimary: '#8ab4f8',
        colorBgContainer: '#292a2d',
        colorBgElevated: '#35363a',
        colorBgLayout: '#202124',
        colorBorder: '#3c4043',
        colorBorderSecondary: '#3c4043',
        colorText: '#e8eaed',
        colorTextSecondary: '#9aa0a6',
        colorTextTertiary: '#9aa0a6',
        colorLink: '#8ab4f8',
        colorLinkHover: '#aecbfa',
        borderRadius: 6,
      } : {
        colorPrimary: '#1677ff',
        borderRadius: 6,
      },
    }}>
      <BrowserRouter>
        <PermissionProvider getMe={getMe}>
          <AppLayout 
            onLogout={async () => { await logout(); setAuthed(false) }}
            theme={resolvedTheme}
            themeMode={themeMode}
            onCycleTheme={cycleTheme}
          />
        </PermissionProvider>
      </BrowserRouter>
    </ConfigProvider>
  )
}
