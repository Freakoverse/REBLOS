import { useEffect, useState } from 'react'
import { Boxes, HardDrive, ShieldCheck, Settings2, LogOut, Loader2, KeyRound, Flame, Radio } from 'lucide-react'
import { connect, hasExtension, npubEncode } from './nostr'
import { getWhitelist } from './api'
import { cn, truncateMiddle } from './lib'
import { toast, Toaster } from './toast'
import { FilesTab } from './components/FilesTab'
import { RelayTab } from './components/RelayTab'
import { WhitelistTab } from './components/WhitelistTab'
import { SettingsTab } from './components/SettingsTab'

const SESSION_KEY = 'brs-dashboard:pubkey'
type Tab = 'blobs' | 'relay' | 'whitelist' | 'settings'

const TABS: { id: Tab; label: string; icon: typeof Boxes }[] = [
  { id: 'blobs', label: 'Files', icon: HardDrive },
  { id: 'relay', label: 'Relay', icon: Radio },
  { id: 'whitelist', label: 'Whitelist', icon: ShieldCheck },
  { id: 'settings', label: 'Settings', icon: Settings2 },
]

export function App() {
  const [pubkey, setPubkey] = useState<string | null>(() => localStorage.getItem(SESSION_KEY))
  const [connecting, setConnecting] = useState(false)
  const [tab, setTab] = useState<Tab>('blobs')

  const login = async () => {
    setConnecting(true)
    try {
      const pk = await connect()
      // Authorization is the node's call: a whitelist read succeeds only for the
      // admin key, so it doubles as the "are you the admin?" check.
      await getWhitelist()
      localStorage.setItem(SESSION_KEY, pk)
      setPubkey(pk)
    } catch (e) {
      const msg = e instanceof Error ? e.message : 'Connection failed'
      toast(/not the admin/i.test(msg) ? 'That key is not this node’s admin.' : msg, 'error')
    } finally {
      setConnecting(false)
    }
  }

  const logout = () => {
    localStorage.removeItem(SESSION_KEY)
    setPubkey(null)
  }

  if (!pubkey) return <Login connecting={connecting} onLogin={login} />

  return (
    <div className="min-h-screen">
      <header className="border-b border-border bg-card/60">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3">
          <div className="flex items-center gap-2">
            <Flame className="h-5 w-5 text-primary" />
            <span className="text-sm font-bold tracking-tight">BRS Dashboard</span>
          </div>
          <div className="flex items-center gap-3">
            <span className="hidden font-mono text-xs text-muted-foreground sm:inline">
              {truncateMiddle(npubEncode(pubkey), 12, 8)}
            </span>
            <button
              onClick={logout}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border px-2.5 py-1.5 text-xs text-neutral-300 transition-colors hover:border-[#404040] hover:text-white"
            >
              <LogOut className="h-3.5 w-3.5" /> Sign out
            </button>
          </div>
        </div>
      </header>

      <div className="mx-auto max-w-6xl px-4 py-6">
        <div className="mb-5 flex flex-wrap gap-1 border-b border-border pb-px">
          {TABS.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              onClick={() => setTab(id)}
              className={cn(
                '-mb-px flex items-center gap-1.5 border-b-2 px-3 py-2 text-xs font-medium transition-colors',
                tab === id
                  ? 'border-primary text-white'
                  : 'border-transparent text-muted-foreground hover:text-neutral-300',
              )}
            >
              <Icon size={14} />
              {label}
            </button>
          ))}
        </div>

        {tab === 'blobs' && <FilesTab />}
        {tab === 'relay' && <RelayTab />}
        {tab === 'whitelist' && <WhitelistTab />}
        {tab === 'settings' && <SettingsTab />}
      </div>

      <Toaster />
    </div>
  )
}

function Login({ connecting, onLogin }: { connecting: boolean; onLogin: () => void }) {
  // Extensions inject window.nostr a moment AFTER the page loads, so a one-time
  // check on first render often runs too early and wrongly reports "none". Poll
  // for a few seconds; the button stays available the whole time either way, so
  // even a slow-injecting or click-to-grant extension still works.
  const [ext, setExt] = useState(hasExtension())
  useEffect(() => {
    if (ext) return
    let tries = 0
    const iv = setInterval(() => {
      if (hasExtension()) {
        setExt(true)
        clearInterval(iv)
      } else if (++tries > 25) {
        clearInterval(iv) // ~5s
      }
    }, 200)
    return () => clearInterval(iv)
  }, [ext])

  return (
    <div className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm space-y-5 rounded-xl border border-border bg-card p-6 text-center">
        <div className="flex justify-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-primary/15">
            <Flame className="h-6 w-6 text-primary" />
          </div>
        </div>
        <div className="space-y-1.5">
          <h1 className="text-lg font-semibold">BRS Dashboard</h1>
          <p className="text-sm text-muted-foreground">
            Sign in with your Nostr extension. Only the node’s admin key is admitted — your key
            never leaves the extension.
          </p>
        </div>

        <button
          onClick={onLogin}
          disabled={connecting}
          className="inline-flex w-full items-center justify-center gap-2 rounded-lg bg-primary px-4 py-2.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary-hover disabled:opacity-60"
        >
          {connecting ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />}
          {connecting ? 'Connecting…' : 'Connect extension'}
        </button>

        {!ext && (
          <p className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
            No extension detected yet. If you have one (Alby, nos2x, …), make sure it’s enabled for
            this site, then click Connect.
          </p>
        )}
      </div>
    </div>
  )
}
