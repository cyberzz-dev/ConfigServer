import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ConfigProvider } from 'antd'
import App from './App'
import 'antd/dist/reset.css'
import './index.css'

const theme = {
  token: {
    colorPrimary: '#0972d3',
    colorLink: '#0972d3',
    colorLinkHover: '#065ea8',
    colorSuccess: '#1d8348',
    colorWarning: '#d68910',
    colorError: '#c0392b',
    colorBgLayout: '#f2f3f3',
    colorBgContainer: '#ffffff',
    colorBorder: '#aab7b8',
    colorBorderSecondary: '#e5e7e8',
    colorText: '#0f1111',
    colorTextSecondary: '#545b64',
    borderRadius: 6,
    borderRadiusLG: 8,
    borderRadiusSM: 4,
    fontSize: 14,
    boxShadow: '0 1px 3px rgba(0,28,36,.1)',
    boxShadowSecondary: '0 4px 12px rgba(0,28,36,.12)',
  },
  components: {
    Layout: {
      headerBg: '#232f3e',
      siderBg: '#ffffff',
      bodyBg: '#f2f3f3',
    },
    Table: {
      headerBg: '#f8f9fa',
      headerColor: '#0f1111',
      borderColor: '#e8eaed',
      rowHoverBg: '#f7f8f8',
      fontSize: 13,
      cellPaddingBlock: 9,
      cellPaddingInline: 16,
      cellPaddingBlockSM: 6,
      cellPaddingInlineSM: 8,
    },
    Button: {
      defaultBorderColor: '#aab7b8',
      primaryShadow: 'none',
      defaultShadow: 'none',
    },
    Modal: {
      titleFontSize: 15,
    },
    Breadcrumb: {
      linkColor: '#0972d3',
      lastItemColor: '#545b64',
      separatorColor: '#aab7b8',
    },
  },
}

const root = createRoot(document.getElementById('root')!)

root.render(
  <StrictMode>
    <ConfigProvider theme={theme}>
      <App />
    </ConfigProvider>
  </StrictMode>,
)

// Splash removal is triggered by 'app:ready' event dispatched from App component
// (fallback timeout is also set in index.html for resilience)
