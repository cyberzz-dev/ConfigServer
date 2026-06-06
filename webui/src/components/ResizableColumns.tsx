import { useState } from 'react'
import { Resizable } from 'react-resizable'
import type { ResizeCallbackData } from 'react-resizable'

// Workaround: react-resizable type defs mark some defaultProps as required
const ResizableAny = Resizable as any

// Resizable header cell for Ant Design Table
export const ResizableTitle = (props: any) => {
  const { onResize, width, ...restProps } = props
  if (!width) return <th {...restProps} />
  return (
    <ResizableAny
      width={width}
      height={0}
      axis="x"
      minConstraints={[50, 0]}
      maxConstraints={[1000, 0]}
      handle={
        <span
          className="react-resizable-handle"
          onClick={e => e.stopPropagation()}
        />
      }
      onResize={onResize}
      draggableOpts={{ enableUserSelectHack: false }}
    >
      <th {...restProps} />
    </ResizableAny>
  )
}

// Pass to <Table components={tableComponents}>
export const tableComponents = {
  header: { cell: ResizableTitle },
}

/**
 * Hook: manages per-column runtime widths and attaches onHeaderCell.
 */
export function useResizableColumns<T extends { width?: number | string; [key: string]: any }>(
  columns: T[]
): T[] {
  const [widths, setWidths] = useState<Record<number, number>>({})

  const handleResize = (index: number) => (
    _: React.SyntheticEvent,
    { size }: ResizeCallbackData
  ) => {
    setWidths(prev => ({ ...prev, [index]: size.width }))
  }

  return columns.map((col, index) => {
    const w = widths[index] ?? (typeof col.width === 'number' ? col.width : undefined)
    const originalHeaderCell = col.onHeaderCell
    return {
      ...col,
      width: w ?? col.width,
      onHeaderCell: (column: any) => {
        const base = typeof originalHeaderCell === 'function' ? originalHeaderCell(column) : {}
        return {
          ...base,
          width: w ?? col.width,
          onResize: handleResize(index),
          onClick: (e: React.MouseEvent) => {
            // Prevent header clicks from bubbling to row onClick
            e.stopPropagation()
            // Call original onClick if exists
            if (base.onClick) base.onClick(e)
          },
        }
      },
    }
  })
}
