import React, { useRef, useCallback } from 'react'
import { theme } from 'antd'

interface Props {
  value?: string
  onChange?: (val: string) => void
  style?: React.CSSProperties
  rows?: number
  readOnly?: boolean
}

const FS = 13
const LH = 20

export default function LineNumberedEditor({ value = '', onChange, style, rows = 12, readOnly }: Props) {
  const { token } = theme.useToken()
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const numRef = useRef<HTMLDivElement>(null)

  const { height, resize, ...restStyle } = style ?? {}
  const hasHeight = !!height

  const lineCount = value.split('\n').length
  // 渲染行数取内容行数和 rows 的较大值，避免 gutter 背景只占顶部一小段
  const gutterLines = hasHeight ? lineCount : Math.max(lineCount, rows)
  const numWidth = Math.max(String(gutterLines).length * 9, 24) + 18

  const onScroll = useCallback(() => {
    if (numRef.current && textareaRef.current) {
      numRef.current.scrollTop = textareaRef.current.scrollTop
    }
  }, [])

  return (
    <div style={{
      display: 'flex',
      border: `1px solid ${token.colorBorder}`,
      borderRadius: token.borderRadius,
      overflow: hasHeight ? 'auto' : 'hidden',
      fontFamily: 'monospace',
      fontSize: FS,
      lineHeight: `${LH}px`,
      background: token.colorBgContainer,
      ...(hasHeight ? { height, resize: resize ?? 'vertical' } : {}),
      ...restStyle,
    }}>
      {/* line numbers */}
      <div
        ref={numRef}
        style={{
          width: numWidth,
          padding: '4px 8px 4px 6px',
          textAlign: 'right',
          color: token.colorTextQuaternary,
          background: 'rgba(0,0,0,0.18)',
          userSelect: 'none',
          overflowY: 'hidden',
          borderRight: `1px solid ${token.colorBorderSecondary}`,
          flexShrink: 0,
          boxSizing: 'border-box',
        }}
      >
        {Array.from({ length: gutterLines }, (_, i) => (
          <div key={i} style={{ height: LH, color: i < lineCount ? undefined : 'transparent' }}>{i + 1}</div>
        ))}
      </div>

      {/* editor */}
      <textarea
        ref={textareaRef}
        value={value}
        readOnly={readOnly}
        onChange={e => onChange?.(e.target.value)}
        onScroll={onScroll}
        rows={hasHeight ? undefined : rows}
        style={{
          flex: 1,
          padding: '4px 8px',
          border: 'none',
          outline: 'none',
          resize: 'none',
          fontFamily: 'monospace',
          fontSize: FS,
          lineHeight: `${LH}px`,
          background: 'transparent',
          color: token.colorText,
          width: 0,
          boxSizing: 'border-box',
          ...(hasHeight ? { height: '100%' } : {}),
        }}
      />
    </div>
  )
}
