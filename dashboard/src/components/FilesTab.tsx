import { useState } from 'react'
import { HardDrive, Ban } from 'lucide-react'
import { cn } from '../lib'
import { BlobsTab } from './BlobsTab'
import { BlacklistPanel } from './BlacklistPanel'

type Sub = 'files' | 'blacklist'

/** Blossom (blob) side of the dashboard: the stored files and the hash blacklist. */
export function FilesTab() {
  const [sub, setSub] = useState<Sub>('files')
  return (
    <div className="space-y-4">
      <div className="flex gap-1">
        <SubTab active={sub === 'files'} onClick={() => setSub('files')} icon={HardDrive} label="Files" />
        <SubTab active={sub === 'blacklist'} onClick={() => setSub('blacklist')} icon={Ban} label="Blacklist" />
      </div>
      {sub === 'files' ? <BlobsTab /> : <BlacklistPanel />}
    </div>
  )
}

function SubTab({ active, onClick, icon: Icon, label }: { active: boolean; onClick: () => void; icon: typeof HardDrive; label: string }) {
  return (
    <button
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-colors',
        active ? 'bg-secondary text-white' : 'text-muted-foreground hover:text-neutral-300',
      )}
    >
      <Icon size={14} /> {label}
    </button>
  )
}
