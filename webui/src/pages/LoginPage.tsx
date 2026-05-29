import { useState } from 'react'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { Alert, Button, Form, Input, Typography } from 'antd'
import { getAuthStatus, initAdmin, login } from '../api'

const { Title, Text } = Typography

interface Props {
  onLoggedIn: () => void
}

/**
 * LoginPage handles two flows:
 *  1. First-time setup – admin account not yet initialized → show password init
 *     form with two confirmation fields.
 *  2. Normal login – account already initialized → show password login form.
 *
 * After successful auth the `onLoggedIn` callback is invoked so App.tsx can
 * render the main layout.
 */
export default function LoginPage({ onLoggedIn }: Props) {
  const [mode, setMode] = useState<'checking' | 'init' | 'login'>('checking')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  // Determine which form to show on mount.
  useState(() => {
    getAuthStatus()
      .then(s => setMode(s.initialized ? 'login' : 'init'))
      .catch(() => setMode('login'))
  })

  // ── Init form ──────────────────────────────────────────────────────────────

  const [initSuccess, setInitSuccess] = useState(false)

  const handleInit = async (values: { password: string; confirm: string }) => {
    setError('')
    if (values.password !== values.confirm) {
      setError('Passwords do not match')
      return
    }
    setLoading(true)
    try {
      await initAdmin(values.password, values.confirm)
      setInitSuccess(true)
      setMode('login')
    } catch (e: any) {
      setError(e?.response?.data?.message ?? 'Initialization failed, please try again')
    } finally {
      setLoading(false)
    }
  }

  // ── Login form ─────────────────────────────────────────────────────────────

  const handleLogin = async (values: { username: string; password: string }) => {
    setError('')
    setLoading(true)
    try {
      await login(values.username, values.password)
      onLoggedIn()
    } catch (e: any) {
      setError(e?.response?.data?.message ?? 'Login failed, please check your password')
    } finally {
      setLoading(false)
    }
  }

  // ── Render ─────────────────────────────────────────────────────────────────

  return (
    <div className="login-page-container">
      <div className="login-page-card">
        {/* Logo / Title */}
        <div style={{ textAlign: 'center', marginBottom: 28 }}>
          <div style={{
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            width: 52,
            height: 52,
            borderRadius: '50%',
            background: '#0972d3',
            marginBottom: 12,
          }}>
            <LockOutlined style={{ fontSize: 24, color: '#fff' }} />
          </div>
          <Title level={4} style={{ margin: 0 }}>
            LoongCollector Config Server
          </Title>
          <Text type="secondary" style={{ fontSize: 13 }}>
            {mode === 'init' ? 'First-time setup: set admin password' : 'Sign in'}
          </Text>
        </div>

        {initSuccess && (
          <Alert
            type="success"
            message="Password set successfully. Please sign in."
            showIcon
            style={{ marginBottom: 16 }}
          />
        )}

        {error && (
          <Alert
            type="error"
            message={error}
            showIcon
            style={{ marginBottom: 16 }}
            closable
            onClose={() => setError('')}
          />
        )}

        {/* Init form */}
        {mode === 'init' && (
          <Form layout="vertical" onFinish={handleInit} requiredMark={false}>
            <Form.Item
              label="New Password"
              name="password"
              rules={[
                { required: true, message: 'Please enter a password' },
                { min: 8, message: 'Password must be at least 8 characters' },
              ]}
            >
              <Input.Password placeholder="At least 8 characters" autoFocus />
            </Form.Item>

            <Form.Item
              label="Confirm Password"
              name="confirm"
              dependencies={['password']}
              rules={[
                { required: true, message: 'Please confirm your password' },
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
              <Input.Password placeholder="Repeat password" />
            </Form.Item>

            <Form.Item style={{ marginBottom: 0, marginTop: 8 }}>
              <Button
                type="primary"
                htmlType="submit"
                loading={loading}
                block
                style={{ height: 38 }}
              >
                Set Password &amp; Sign In
              </Button>
            </Form.Item>
          </Form>
        )}

        {/* Login form */}
        {mode === 'login' && (
          <Form layout="vertical" onFinish={handleLogin} requiredMark={false}
            initialValues={{ username: 'admin' }}
          >
            <Form.Item
              label="Username"
              name="username"
              rules={[{ required: true, message: 'Please enter your username' }]}
            >
              <Input prefix={<UserOutlined />} placeholder="Username" autoFocus />
            </Form.Item>

            <Form.Item
              label="Password"
              name="password"
              rules={[{ required: true, message: 'Please enter your password' }]}
            >
              <Input.Password prefix={<LockOutlined />} placeholder="Password" />
            </Form.Item>

            <Form.Item style={{ marginBottom: 0, marginTop: 8 }}>
              <Button
                type="primary"
                htmlType="submit"
                loading={loading}
                block
                style={{ height: 38 }}
              >
                Sign In
              </Button>
            </Form.Item>
          </Form>
        )}
      </div>
    </div>
  )
}
