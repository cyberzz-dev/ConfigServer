import { useState, useEffect } from 'react'
import { Modal, Input, Typography, Alert } from 'antd'

const { Text } = Typography

interface Props {
  open: boolean
  /** The exact string the user must type to confirm deletion */
  targetName: string
  /** Human-readable entity type shown in the warning message */
  entityType?: string
  onConfirm: () => Promise<void>
  onCancel: () => void
}

/**
 * A deletion confirmation modal that requires the user to re-type the target
 * name before the destructive action is allowed.  The OK button stays disabled
 * until the input matches `targetName` exactly.
 */
export default function DeleteConfirmModal({
  open,
  targetName,
  entityType = 'item',
  onConfirm,
  onCancel,
}: Props) {
  const [input, setInput]     = useState('')
  const [loading, setLoading] = useState(false)

  // Reset the input field every time the modal opens or the target changes.
  useEffect(() => {
    if (open) setInput('')
  }, [open, targetName])

  const matched = input === targetName

  const handleOk = async () => {
    if (!matched) return
    setLoading(true)
    try {
      await onConfirm()
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal
      title={<span style={{ color: '#ff4d4f' }}>⚠ Confirm Deletion</span>}
      open={open}
      onOk={handleOk}
      onCancel={onCancel}
      okText="Delete"
      cancelText="Cancel"
      okButtonProps={{ danger: true, disabled: !matched, loading }}
      closable={!loading}
      maskClosable={false}
      destroyOnClose
    >
      <Alert
        type="warning"
        showIcon
        message={
          <>
            You are about to permanently delete {entityType} <Text code>{targetName}</Text>. This action cannot be undone.
          </>
        }
        style={{ marginBottom: 16 }}
      />
      <p style={{ marginBottom: 8 }}>
        Type <Text code>{targetName}</Text> below to confirm:
      </p>
      <Input
        value={input}
        onChange={e => setInput(e.target.value)}
        placeholder={targetName}
        onPressEnter={handleOk}
        autoFocus
        status={input && !matched ? 'error' : undefined}
      />
      {input && !matched && (
        <div style={{ color: '#ff4d4f', fontSize: 12, marginTop: 4 }}>
          Input does not match the name
        </div>
      )}
    </Modal>
  )
}
