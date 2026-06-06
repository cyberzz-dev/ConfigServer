import { useState } from 'react'
import { LockOutlined, SafetyCertificateOutlined, UserOutlined } from '@ant-design/icons'
import { Alert, Button, Form, Input, Typography } from 'antd'
import { getAuthStatus, initAdmin, login, loginOTP } from '../api'

const { Title, Text } = Typography

interface Props {
  onLoggedIn: () => void
}

/**
 * LoginPage handles three flows:
 *  1. First-time setup 鈥?admin account not yet initialized 鈫?password init form.
 *  2. Normal login 鈥?show username + password form.
 *  3. OTP step 鈥?server returned otp_required=true; show 6-digit code input.
 *
 * After successful auth the `onLoggedIn` callback is invoked so App.tsx can
 * render the main layout.
 */
export default function LoginPage({ onLoggedIn }: Props) {
  const [mode, setMode] = useState<'checking' | 'init' | 'login' | 'otp'>('checking')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [pendingToken, setPendingToken] = useState('')

  // Determine which form to show on mount.
  useState(() => {
    getAuthStatus()
      .then(s => setMode(s.initialized ? 'login' : 'init'))
      .catch(() => setMode('login'))
  })

  // 鈹€鈹€ Init form 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

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

  // 鈹€鈹€ Login form 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

  const handleLogin = async (values: { username: string; password: string }) => {
    setError('')
    setLoading(true)
    try {
      const res = await login(values.username, values.password)
      const data = res.data.data
      if (data.otp_required && data.pending_token) {
        setPendingToken(data.pending_token)
        setMode('otp')
      } else {
        onLoggedIn()
      }
    } catch (e: any) {
      setError(e?.response?.data?.message ?? 'Login failed, please check your password')
    } finally {
      setLoading(false)
    }
  }

  // 鈹€鈹€ OTP verification step 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

  const handleOTP = async (values: { otp_code: string }) => {
    setError('')
    setLoading(true)
    try {
      await loginOTP(pendingToken, values.otp_code)
      onLoggedIn()
    } catch (e: any) {
      setError(e?.response?.data?.message ?? 'Invalid code, please try again')
    } finally {
      setLoading(false)
    }
  }

  // 鈹€鈹€ Render 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

  const subtitle =
    mode === 'init' ? 'First-time setup: set admin password' :
    mode === 'otp'  ? 'Two-factor authentication' :
                      'Sign in'

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
            background: mode === 'otp' ? '#52c41a' : '#0972d3',
            marginBottom: 12,
          }}>
            {mode === 'otp'
              ? <SafetyCertificateOutlined style={{ fontSize: 24, color: '#fff' }} />
              : <LockOutlined style={{ fontSize: 24, color: '#fff' }} />}
          </div>
          <Title level={4} style={{ margin: 0 }}>
            <span className="brand-title" aria-label="LoongCollector Config Server">
              <span className="brand-title-word-1">LoongCollector</span>
              <span className="brand-title-word-2">Config</span>
              <span className="brand-title-word-3">Server</span>
            </span>
          </Title>
          <Text type="secondary" style={{ fontSize: 13 }}>{subtitle}</Text>
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
              <Input prefix={<UserOutlined />} placeholder="Username" autoFocus disabled={loading} />
            </Form.Item>

            <Form.Item
              label="Password"
              name="password"
              rules={[{ required: true, message: 'Please enter your password' }]}
            >
              <Input.Password prefix={<LockOutlined />} placeholder="Password" disabled={loading} />
            </Form.Item>

            <Form.Item style={{ marginBottom: 0, marginTop: 8 }}>
              <Button
                type="primary"
                htmlType="submit"
                loading={loading}
                block
                style={{ height: 38 }}
              >
                {loading ? 'Signing in...' : 'Sign In'}
              </Button>
            </Form.Item>
          </Form>
        )}

        {/* OTP step */}
        {mode === 'otp' && (
          <Form layout="vertical" onFinish={handleOTP} requiredMark={false}>
            <div style={{ textAlign: 'center', color: '#595959', fontSize: 13, marginBottom: 20 }}>
              Open your authenticator app and enter the 6-digit code.
            </div>
            <Form.Item
              label="Verification Code"
              name="otp_code"
              rules={[
                { required: true, message: 'Please enter the 6-digit code' },
                { pattern: /^\d{6}$/, message: 'Must be exactly 6 digits' },
              ]}
            >
              <Input
                prefix={<SafetyCertificateOutlined />}
                placeholder="6-digit code"
                maxLength={6}
                autoFocus
                style={{ letterSpacing: '0.25em', fontSize: 18, textAlign: 'center' }}
              />
            </Form.Item>

            <Form.Item style={{ marginBottom: 0, marginTop: 8 }}>
              <Button
                type="primary"
                htmlType="submit"
                loading={loading}
                block
                style={{ height: 38 }}
              >
                Verify
              </Button>
            </Form.Item>
            <div style={{ textAlign: 'center', marginTop: 12 }}>
              <Button type="link" size="small" onClick={() => { setMode('login'); setError('') }}>
                Back to login
              </Button>
            </div>
          </Form>
        )}
      </div>
    </div>
  )
}


/**
 * LoginPage handles two flows:
 *  1. First-time setup — admin account not yet initialized — show password init
 *     form with two confirmation fields.
 *  2. Normal login — account already initialized — show password login form.
 *
 * After successful auth the `onLoggedIn` callback is invoked so App.tsx can
 * render the main layout.
 */
