import { useMemo, useRef, useState } from 'react'
import { diffLines } from 'diff'
import { theme, Tooltip } from 'antd'
import { CopyOutlined, CheckOutlined } from '@ant-design/icons'
import yaml from 'js-yaml'

/**
 * Normalize text to YAML for comparison.
 * Both sides are always converted to YAML so JSON vs YAML format differences
 * don't produce false diffs.
 */
function normalizeToYaml(text: string): string {
  const t = text.trim()
  if (!t) return text
  // Try JSON → YAML
  try {
    const parsed = JSON.parse(t)
    return yaml.dump(parsed, { indent: 2, lineWidth: -1, noRefs: true })
  } catch {/* not JSON */}
  // Try YAML → YAML (re-serialize to normalize whitespace/quoting)
  try {
    const parsed = yaml.load(t)
    if (parsed !== null && typeof parsed === 'object') {
      return yaml.dump(parsed, { indent: 2, lineWidth: -1, noRefs: true })
    }
  } catch {/* not YAML */}
  return text
}

interface Props {
  oldText: string
  newText: string
  oldLabel?: string
  newLabel?: string
  style?: React.CSSProperties
}

// A paired row: left (stable) side and right (canary) side shown side-by-side
type SideCell = { lineNo: number; content: string; type: 'added' | 'removed' | 'normal' } | null
type PairedRow = { left: SideCell; right: SideCell }

export default function DiffViewer({
  oldText,
  newText,
  oldLabel = 'Stable',
  newLabel = 'Canary',
  style,
}: Props) {
  const { token } = theme.useToken()
  const scrollRef = useRef<HTMLDivElement>(null)
  const [copiedLeft, setCopiedLeft] = useState(false)
  const [copiedRight, setCopiedRight] = useState(false)

  const copyText = (text: string, side: 'left' | 'right') => {
    navigator.clipboard.writeText(normalizeToYaml(text)).then(() => {
      if (side === 'left') {
        setCopiedLeft(true)
        setTimeout(() => setCopiedLeft(false), 1500)
      } else {
        setCopiedRight(true)
        setTimeout(() => setCopiedRight(false), 1500)
      }
    })
  }

  const pairedRows = useMemo<PairedRow[]>(() => {
    const formattedOld = normalizeToYaml(oldText)
    const formattedNew = normalizeToYaml(newText)
    const hunks = diffLines(formattedOld, formattedNew)
    const rows: PairedRow[] = []
    let oldLine = 1
    let newLine = 1

    // Buffer consecutive removed/added lines so they pair up side-by-side
    const flush = (removed: string[], added: string[]) => {
      const len = Math.max(removed.length, added.length)
      for (let i = 0; i < len; i++) {
        const leftContent  = removed[i]
        const rightContent = added[i]
        rows.push({
          left:  leftContent  !== undefined ? { lineNo: oldLine++, content: leftContent,  type: 'removed' } : null,
          right: rightContent !== undefined ? { lineNo: newLine++, content: rightContent, type: 'added'   } : null,
        })
      }
    }

    let pendingRemoved: string[] = []
    let pendingAdded:   string[] = []

    for (const part of hunks) {
      const lines = part.value.split('\n')
      if (lines[lines.length - 1] === '') lines.pop()

      if (part.removed) {
        pendingRemoved.push(...lines)
      } else if (part.added) {
        pendingAdded.push(...lines)
      } else {
        if (pendingRemoved.length || pendingAdded.length) {
          flush(pendingRemoved, pendingAdded)
          pendingRemoved = []
          pendingAdded   = []
        }
        for (const line of lines) {
          rows.push({
            left:  { lineNo: oldLine++, content: line, type: 'normal' },
            right: { lineNo: newLine++, content: line, type: 'normal' },
          })
        }
      }
    }
    if (pendingRemoved.length || pendingAdded.length) {
      flush(pendingRemoved, pendingAdded)
    }
    return rows
  }, [oldText, newText])

  const noChange = pairedRows.every(r => r.left?.type === 'normal' && r.right?.type === 'normal')

  // Detect dark mode by computing luminance of the container background.
  // Ant Design dark tokens use short hex like #000; expand 3-char hex to 6 before parsing.
  const isDark = (() => {
    let hex = (token.colorBgBase || token.colorBgContainer || '#ffffff').replace('#', '').toLowerCase()
    // Expand 3-char shorthand: "abc" → "aabbcc"
    if (hex.length === 3) hex = hex[0] + hex[0] + hex[1] + hex[1] + hex[2] + hex[2]
    if (hex.length !== 6) return false
    const r = parseInt(hex.slice(0, 2), 16)
    const g = parseInt(hex.slice(2, 4), 16)
    const b = parseInt(hex.slice(4, 6), 16)
    return 0.299 * r + 0.587 * g + 0.114 * b < 128
  })()

  const C = {
    addedBg:    isDark ? '#0d2a0d' : '#efffef',
    addedNum:   isDark ? '#1a3d1a' : '#d9f7be',
    addedText:  isDark ? '#52c41a' : '#389e0d',
    removedBg:  isDark ? '#2a0d0d' : '#fff5f5',
    removedNum: isDark ? '#3d1a1a' : '#fff1f0',
    removedText:isDark ? '#ff7875' : '#cf1322',
    normalBg:   token.colorBgContainer,
    normalNum:  isDark ? '#1f1f1f' : '#fafafa',
    divider:    token.colorBorderSecondary,
    numText:    token.colorTextQuaternary,
    text:       token.colorText,
  }

  const FS   = 13
  const LH   = 20
  const NW   = 44  // line-number column width

  const bgOf  = (t: SideCell) => !t ? (isDark ? '#111' : '#f5f5f5') : t.type === 'added' ? C.addedBg   : t.type === 'removed' ? C.removedBg   : C.normalBg
  const numBg = (t: SideCell) => !t ? (isDark ? '#111' : '#f0f0f0') : t.type === 'added' ? C.addedNum  : t.type === 'removed' ? C.removedNum  : C.normalNum
  const fg    = (t: SideCell) => !t ? C.text : t.type === 'added' ? C.addedText : t.type === 'removed' ? C.removedText : C.text

  return (
    <div style={{
      border: `1px solid ${C.divider}`,
      borderRadius: token.borderRadius,
      overflow: 'hidden',
      display: 'flex',
      flexDirection: 'column',
      fontFamily: '"SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace',
      ...style,
    }}>
      {/* ── Header ── */}
      <div style={{ display: 'flex', borderBottom: `1px solid ${C.divider}` }}>
        <div style={{
          flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'space-between',
          padding: '5px 10px', background: C.removedNum, color: C.removedText,
          fontSize: 12, fontWeight: 600, borderRight: `2px solid ${C.divider}`,
          overflow: 'hidden',
        }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 6, overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis' }}>
            <span style={{ opacity: 0.6 }}>−</span>{oldLabel}
          </span>
          <Tooltip title={copiedLeft ? 'Copied!' : 'Copy'}>
            <span
              onClick={() => copyText(oldText, 'left')}
              style={{ cursor: 'pointer', opacity: 0.7, flexShrink: 0, marginLeft: 8, fontSize: 13, lineHeight: 1 }}
            >
              {copiedLeft ? <CheckOutlined style={{ color: '#52c41a' }} /> : <CopyOutlined />}
            </span>
          </Tooltip>
        </div>
        <div style={{
          flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'space-between',
          padding: '5px 10px', background: C.addedNum, color: C.addedText,
          fontSize: 12, fontWeight: 600,
          overflow: 'hidden',
        }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 6, overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis' }}>
            <span style={{ opacity: 0.6 }}>+</span>{newLabel}
          </span>
          <Tooltip title={copiedRight ? 'Copied!' : 'Copy'}>
            <span
              onClick={() => copyText(newText, 'right')}
              style={{ cursor: 'pointer', opacity: 0.7, flexShrink: 0, marginLeft: 8, fontSize: 13, lineHeight: 1 }}
            >
              {copiedRight ? <CheckOutlined style={{ color: '#52c41a' }} /> : <CopyOutlined />}
            </span>
          </Tooltip>
        </div>
      </div>

      {noChange ? (
        <div style={{ flex: 1, padding: '28px 16px', textAlign: 'center', color: token.colorTextSecondary, fontSize: 13, background: C.normalBg }}>
          No differences — canary config is identical to stable
        </div>
      ) : (
        <div ref={scrollRef} style={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>
          {/* Fixed layout + 100% width: content columns each take ~50% after accounting for line-number cols */}
          <table style={{ width: '100%', borderCollapse: 'collapse', tableLayout: 'fixed' }}>
            <colgroup>
              <col style={{ width: NW }} />
              <col />                          {/* left content — grows with longest line */}
              <col style={{ width: 2 }} />     {/* centre divider */}
              <col style={{ width: NW }} />
              <col />                          {/* right content */}
            </colgroup>
            <tbody>
              {pairedRows.map((row, i) => (
                <tr key={i}>
                  {/* ── LEFT: line number ── */}
                  <td style={{
                    width: NW, minWidth: NW, textAlign: 'right',
                    paddingRight: 8, paddingLeft: 4, fontSize: FS, lineHeight: `${LH}px`,
                    background: numBg(row.left), color: C.numText,
                    userSelect: 'none', borderRight: `1px solid ${C.divider}`,
                  }}>
                    {row.left?.lineNo ?? ''}
                  </td>
                  {/* ── LEFT: content ── */}
                  <td style={{
                    position: 'relative',
                    background: bgOf(row.left), color: fg(row.left),
                    paddingLeft: 20, paddingRight: 16,
                    whiteSpace: 'pre-wrap', wordBreak: 'break-word', overflowWrap: 'anywhere',
                    fontSize: FS, lineHeight: `${LH}px`,
                    borderRight: `2px solid ${C.divider}`,
                  }}>
                    {row.left && row.left.type !== 'normal' && (
                      <span style={{ position: 'absolute', left: 6, userSelect: 'none', opacity: 0.5 }}>−</span>
                    )}
                    {row.left?.content ?? ''}
                  </td>
                  {/* ── divider ── */}
                  <td style={{ width: 0, padding: 0 }} />
                  {/* ── RIGHT: line number ── */}
                  <td style={{
                    width: NW, minWidth: NW, textAlign: 'right',
                    paddingRight: 8, paddingLeft: 4, fontSize: FS, lineHeight: `${LH}px`,
                    background: numBg(row.right), color: C.numText,
                    userSelect: 'none', borderRight: `1px solid ${C.divider}`,
                  }}>
                    {row.right?.lineNo ?? ''}
                  </td>
                  {/* ── RIGHT: content ── */}
                  <td style={{
                    position: 'relative',
                    background: bgOf(row.right), color: fg(row.right),
                    paddingLeft: 20, paddingRight: 16,
                    whiteSpace: 'pre-wrap', wordBreak: 'break-word', overflowWrap: 'anywhere',
                    fontSize: FS, lineHeight: `${LH}px`,
                  }}>
                    {row.right && row.right.type !== 'normal' && (
                      <span style={{ position: 'absolute', left: 6, userSelect: 'none', opacity: 0.5 }}>+</span>
                    )}
                    {row.right?.content ?? ''}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

