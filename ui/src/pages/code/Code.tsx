import { useEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { Link, useLocation, useNavigate, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronRight, File, FileCode2, Folder, FolderOpen, GitCommitHorizontal, History, Plus, Search } from 'lucide-react'
import { Button, Card, Dialog, EmptyState, Field, Input, Pill, SkeletonRows, Textarea, useToast } from '../../design'
import { ApiError, repoBase } from '../../lib/api'
import { refName } from '../../lib/format'
import { useHotkey } from '../../lib/keyboard'
import { actions, qk, useBlob, useFeatures, useLog, useRefs, useRepo, useTree } from '../../lib/queries'
import { Markdown, RefPicker, TimeAgo, useDocumentTitle } from '../../shell/common'
import { NotFound } from '../../shell/NotFound'
import { FileView } from './FileView'

type Node = { name: string; path: string; dir: boolean; children: Map<string, Node> }

export function buildTree(paths: string[]): Node {
  const root: Node = { name: '', path: '', dir: true, children: new Map() }
  for (const p of paths) {
    const parts = p.split('/')
    let cur = root
    for (let i = 0; i < parts.length; i++) {
      const name = parts[i]!
      const isLast = i === parts.length - 1
      let next = cur.children.get(name)
      if (!next) {
        next = { name, path: parts.slice(0, i + 1).join('/'), dir: !isLast, children: new Map() }
        cur.children.set(name, next)
      }
      cur = next
    }
  }
  return root
}

export function sortedChildren(n: Node): Node[] {
  return [...n.children.values()].sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1))
}

export function findNode(root: Node, path: string): Node | null {
  if (!path) return root
  let cur = root
  for (const part of path.split('/')) {
    const n = cur.children.get(part)
    if (!n) return null
    cur = n
  }
  return cur
}

/** Fuzzy subsequence scoring for the file finder; lower is better. */
export function fuzzyScore(q: string, s: string): number | null {
  const lq = q.toLowerCase()
  const ls = s.toLowerCase()
  let qi = 0
  let score = 0
  let last = -1
  for (let i = 0; i < ls.length && qi < lq.length; i++) {
    if (ls[i] === lq[qi]) {
      score += last >= 0 ? i - last - 1 : i
      last = i
      qi++
    }
  }
  if (qi < lq.length) return null
  return score + (ls.length - last)
}

function safeDecode(s: string): string {
  try {
    return s.includes('%') ? decodeURIComponent(s) : s
  } catch {
    return s
  }
}

export function CodePage() {
  const params = useParams({ strict: false }) as { org: string; repo: string; ref?: string; _splat?: string }
  const location = useLocation()
  const navigate = useNavigate()
  const { org, repo } = params
  const repoQ = useRepo(org, repo)
  const refs = useRefs(org, repo)
  const isBlob = location.pathname.includes(`/${org}/${repo}/blob/`)
  const refParam = params.ref ? safeDecode(params.ref) : ''
  const defaultRef = repoQ.data ? `refs/heads/${repoQ.data.default_branch}` : ''
  const refRaw = refParam || defaultRef
  // Accept bare branch names in URLs; resolve to a full ref when it exists.
  const ref = useMemo(() => {
    if (!refRaw) return ''
    if (refRaw.startsWith('refs/')) return refRaw
    if (refs.data?.[`refs/heads/${refRaw}`]) return `refs/heads/${refRaw}`
    if (refs.data?.[`refs/tags/${refRaw}`]) return `refs/tags/${refRaw}`
    return refRaw
  }, [refRaw, refs.data])
  const path = params._splat ? params._splat.replace(/\/+$/, '') : ''
  const tree = useTree(org, repo, ref)
  const [finderOpen, setFinderOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  useDocumentTitle(`${path ? path + ' · ' : ''}${org}/${repo}`)

  const root = useMemo(() => buildTree(tree.data ?? []), [tree.data])
  const node = useMemo(() => findNode(root, path), [root, path])
  const dirView = !isBlob
  const showBlob = isBlob || (node !== null && !node.dir && path !== '')
  useHotkey('t', () => setFinderOpen(true))

  // TanStack encodes path params itself (a slash becomes %2F); pass the bare name.
  const refSlug = (r: string) => refName(r) || r
  const goTree = (p: string, r = ref) => void navigate({ to: p ? '/$org/$repo/tree/$ref/$' : '/$org/$repo/tree/$ref', params: { org, repo, ref: refSlug(r), _splat: p } as never })
  const goBlob = (p: string, r = ref) => void navigate({ to: '/$org/$repo/blob/$ref/$', params: { org, repo, ref: refSlug(r), _splat: p } as never })

  if (repoQ.isError && repoQ.error instanceof ApiError && repoQ.error.status === 404) return <NotFound what="repository" />
  if (!ref || (tree.isPending && !tree.data)) return <SkeletonRows rows={8} />

  const emptyRepo = tree.isError || (tree.data && tree.data.length === 0 && !path)
  if (emptyRepo && !path) return <EmptyRepo org={org} repo={repo} branch={repoQ.data?.default_branch ?? 'main'} />

  return (
    <div className="code-layout">
      <aside className="tree-pane card flat" aria-label="File tree">
        <div className="row" style={{ padding: 'var(--sp-2) var(--sp-2) 0' }}>
          <Button size="sm" variant="ghost" onClick={() => setFinderOpen(true)} aria-keyshortcuts="t" title="Find a file (t)">
            <Search /> Find file
          </Button>
        </div>
        <TreeNav root={root} current={path} onOpenDir={goTree} onOpenFile={goBlob} />
      </aside>
      <div className="stack">
        <div className="row" style={{ gap: 'var(--sp-3)' }}>
          <RefPicker refs={refs.data} current={ref} onPick={(r) => (showBlob ? goBlob(path, r) : goTree(path, r))} />
          <nav className="breadcrumb-path grow" aria-label="Path">
            <Link to="/$org/$repo" params={{ org, repo }} className="quiet">
              {repo}
            </Link>
            {path.split('/').filter(Boolean).map((seg, i, arr) => {
              const p = arr.slice(0, i + 1).join('/')
              const leaf = i === arr.length - 1
              return (
                <span key={p} style={{ display: 'contents' }}>
                  <span className="sep">/</span>
                  {leaf ? (
                    <span className="leaf">{seg}</span>
                  ) : (
                    <Link to="/$org/$repo/tree/$ref/$" params={{ org, repo, ref: refSlug(ref), _splat: p } as never} className="quiet">
                      {seg}
                    </Link>
                  )}
                </span>
              )
            })}
          </nav>
          <Link to="/$org/$repo/commits/$ref" params={{ org, repo, ref: refSlug(ref) }} className="btn sm">
            <History /> History
          </Link>
          {dirView && !showBlob ? (
            <Button size="sm" onClick={() => setEditOpen(true)}>
              <Plus /> New file
            </Button>
          ) : null}
        </div>
        {showBlob ? (
          <BlobPane org={org} repo={repo} refFull={ref} path={path} onEdit={() => setEditOpen(true)} />
        ) : node ? (
          <DirPane org={org} repo={repo} refFull={ref} node={node} onOpenDir={goTree} onOpenFile={goBlob} />
        ) : (
          <NotFound what="path" />
        )}
      </div>
      <FileFinder open={finderOpen} onClose={() => setFinderOpen(false)} paths={tree.data ?? []} onPick={(p) => goBlob(p)} />
      <EditFileDialog open={editOpen} onClose={() => setEditOpen(false)} org={org} repo={repo} refFull={ref} path={showBlob ? path : ''} dir={showBlob ? path.split('/').slice(0, -1).join('/') : path} />
    </div>
  )
}

function TreeNav({ root, current, onOpenDir, onOpenFile }: { root: Node; current: string; onOpenDir: (p: string) => void; onOpenFile: (p: string) => void }) {
  const [open, setOpen] = useState<Set<string>>(() => {
    const s = new Set<string>()
    const parts = current.split('/')
    for (let i = 1; i < parts.length; i++) s.add(parts.slice(0, i).join('/'))
    return s
  })
  useEffect(() => {
    setOpen((s) => {
      const n = new Set(s)
      const parts = current.split('/')
      for (let i = 1; i <= parts.length; i++) n.add(parts.slice(0, i).join('/'))
      return n
    })
  }, [current])
  const toggle = (p: string) =>
    setOpen((s) => {
      const n = new Set(s)
      if (n.has(p)) n.delete(p)
      else n.add(p)
      return n
    })
  const render = (n: Node, depth: number): ReactElement[] =>
    sortedChildren(n).map((c) => (
      <li key={c.path}>
        {c.dir ? (
          <>
            <button type="button" className="node" aria-expanded={open.has(c.path)} aria-current={c.path === current ? 'page' : undefined} onClick={() => { toggle(c.path); onOpenDir(c.path) }} style={{ paddingLeft: 8 + depth * 12 }} title={c.path}>
              <ChevronRight className="chev" aria-hidden />
              {open.has(c.path) ? <FolderOpen aria-hidden /> : <Folder aria-hidden />}
              <span className="truncate">{c.name}</span>
            </button>
            {open.has(c.path) ? <ul>{render(c, depth + 1)}</ul> : null}
          </>
        ) : (
          <button type="button" className="node" aria-current={c.path === current ? 'page' : undefined} onClick={() => onOpenFile(c.path)} style={{ paddingLeft: 8 + depth * 12 + 18 }} title={c.path}>
            <File aria-hidden />
            <span className="truncate">{c.name}</span>
          </button>
        )}
      </li>
    ))
  return <ul className="tree">{render(root, 0)}</ul>
}

function DirPane({ org, repo, refFull, node, onOpenDir, onOpenFile }: { org: string; repo: string; refFull: string; node: Node; onOpenDir: (p: string) => void; onOpenFile: (p: string) => void }) {
  const children = sortedChildren(node)
  const readme = children.find((c) => !c.dir && /^readme(\.md|\.markdown|\.txt)?$/i.test(c.name))
  const readmeBlob = useBlob(org, repo, refFull, readme?.path ?? '', !!readme)
  const log = useLog(org, repo, refFull)
  const last = log.data?.[0]
  return (
    <>
      <div className="card file-list">
        {last ? (
          <div className="last-commit">
            <GitCommitHorizontal size={16} aria-hidden style={{ color: 'var(--fg-muted)' }} />
            <span className="muted">{last.author}</span>
            <Link to="/$org/$repo/commit/$oid" params={{ org, repo, oid: last.oid }} className="msg quiet" title={last.subject}>
              {last.subject}
            </Link>
            <span className="muted small">
              <Link to="/$org/$repo/commit/$oid" params={{ org, repo, oid: last.oid }} className="mono quiet">
                {last.oid.slice(0, 7)}
              </Link>{' '}
              · <TimeAgo iso={last.date} />
            </span>
          </div>
        ) : null}
        <div className="list" role="list">
          {node.path ? (
            <button type="button" className="list-row" onClick={() => onOpenDir(node.path.split('/').slice(0, -1).join('/'))} style={{ background: 'none', border: 0, borderTop: 0, width: '100%', textAlign: 'left', cursor: 'pointer', font: 'inherit' }}>
              <Folder aria-hidden />
              <span>..</span>
            </button>
          ) : null}
          {children.map((c) => (
            <button key={c.path} type="button" className="list-row" role="listitem" onClick={() => (c.dir ? onOpenDir(c.path) : onOpenFile(c.path))} style={{ background: 'none', border: 0, borderTop: '1px solid var(--border)', width: '100%', textAlign: 'left', cursor: 'pointer', font: 'inherit' }}>
              {c.dir ? <Folder aria-hidden /> : <FileCode2 aria-hidden />}
              <span className="truncate" style={{ fontFamily: c.dir ? undefined : 'var(--font-mono)' }}>
                {c.name}
              </span>
              {c.dir ? <span className="subtle small">{c.children.size} items</span> : null}
            </button>
          ))}
        </div>
      </div>
      {readme && readmeBlob.data && !readmeBlob.data.binary ? (
        <div className="card">
          <div className="card-head">
            <h3>
              <FileCode2 aria-hidden /> {readme.name}
            </h3>
            <Button size="sm" variant="ghost" onClick={() => onOpenFile(readme.path)}>
              Open
            </Button>
          </div>
          <div className="card-body" style={{ padding: 'var(--sp-4) var(--sp-5)' }}>
            {/\.md|\.markdown$/i.test(readme.name) || !/\./.test(readme.name) ? <Markdown source={readmeBlob.data.content} /> : <pre className="codeblock">{readmeBlob.data.content}</pre>}
          </div>
        </div>
      ) : null}
    </>
  )
}

function BlobPane({ org, repo, refFull, path, onEdit }: { org: string; repo: string; refFull: string; path: string; onEdit: () => void }) {
  const blob = useBlob(org, repo, refFull, path)
  const rawUrl = `${repoBase(org, repo)}/file?ref=${encodeURIComponent(refFull)}&path=${encodeURIComponent(path)}`
  if (blob.isPending) return <SkeletonRows rows={10} />
  if (blob.isError) {
    if (blob.error instanceof ApiError && blob.error.status === 404) return <NotFound what="file" />
    return <EmptyState title="Could not load the file" compact actions={<Button onClick={() => blob.refetch()}>Retry</Button>}>{blob.error instanceof Error ? blob.error.message : 'Request failed'}</EmptyState>
  }
  return (
    <>
      {blob.data.last_commit ? (
        <div className="card flat last-commit" style={{ borderRadius: 'var(--radius-3)' }}>
          <GitCommitHorizontal size={16} aria-hidden style={{ color: 'var(--fg-muted)' }} />
          <span className="muted">{blob.data.last_commit.author}</span>
          <Link to="/$org/$repo/commit/$oid" params={{ org, repo, oid: blob.data.last_commit.oid }} className="msg quiet">
            {blob.data.last_commit.subject}
          </Link>
          <span className="muted small">
            <TimeAgo iso={blob.data.last_commit.date} />
          </span>
        </div>
      ) : null}
      <FileView blob={blob.data} rawUrl={rawUrl} onEdit={onEdit} />
    </>
  )
}

function FileFinder({ open, onClose, paths, onPick }: { open: boolean; onClose: () => void; paths: string[]; onPick: (p: string) => void }) {
  const [q, setQ] = useState('')
  const [idx, setIdx] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (open) {
      setQ('')
      setIdx(0)
      setTimeout(() => inputRef.current?.focus(), 0)
    }
  }, [open])
  const hits = useMemo(() => {
    if (!q.trim()) return paths.slice(0, 50)
    return paths
      .map((p) => ({ p, s: fuzzyScore(q.trim(), p) }))
      .filter((x): x is { p: string; s: number } => x.s !== null)
      .sort((a, b) => a.s - b.s || a.p.length - b.p.length)
      .slice(0, 50)
      .map((x) => x.p)
  }, [q, paths])
  useEffect(() => setIdx(0), [q])
  return (
    <Dialog open={open} onClose={onClose} title="Go to file">
      <div className="stack">
        <div className="input-with-icon">
          <Search aria-hidden />
          <Input ref={inputRef} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Type a path…" aria-label="File path" autoComplete="off" onKeyDown={(e) => {
            if (e.key === 'ArrowDown') { e.preventDefault(); setIdx((i) => Math.min(i + 1, hits.length - 1)) }
            else if (e.key === 'ArrowUp') { e.preventDefault(); setIdx((i) => Math.max(i - 1, 0)) }
            else if (e.key === 'Enter' && hits[idx]) { onClose(); onPick(hits[idx]!) }
          }} />
        </div>
        <div className="list" role="listbox" aria-label="Matching files" style={{ maxHeight: 360, overflow: 'auto' }}>
          {hits.map((p, i) => (
            <div key={p} role="option" aria-selected={i === idx} className="list-row" data-active={i === idx} style={{ minHeight: 34, padding: '6px 12px', cursor: 'pointer' }} onMouseEnter={() => setIdx(i)} onClick={() => { onClose(); onPick(p) }}>
              <File size={14} aria-hidden style={{ color: 'var(--fg-muted)' }} />
              <span className="mono small truncate">{p}</span>
            </div>
          ))}
          {hits.length === 0 ? <p className="muted" style={{ padding: 'var(--sp-3)' }}>No files match.</p> : null}
        </div>
      </div>
    </Dialog>
  )
}

/** Edit or create a file through the API: a server-side commit, no clone. */
function EditFileDialog({ open, onClose, org, repo, refFull, path, dir }: { open: boolean; onClose: () => void; org: string; repo: string; refFull: string; path: string; dir: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const navigate = useNavigate()
  const existing = useBlob(org, repo, refFull, path, open && !!path)
  const [filePath, setFilePath] = useState(path)
  const [content, setContent] = useState('')
  const [message, setMessage] = useState('')
  const [branch, setBranch] = useState(refName(refFull))
  const [busy, setBusy] = useState(false)
  const { data: features } = useFeatures()
  useEffect(() => {
    if (!open) return
    setFilePath(path || (dir ? dir + '/' : ''))
    setBranch(refFull.startsWith('refs/heads/') ? refName(refFull) : 'main')
    setMessage(path ? `Update ${path}` : '')
  }, [open, path, dir, refFull])
  useEffect(() => {
    if (open && existing.data && !existing.data.binary) setContent(existing.data.content)
    if (open && !path) setContent('')
  }, [open, existing.data, path])

  const submit = async () => {
    setBusy(true)
    try {
      const res = await actions.commit(org, repo, { branch, message: message || (path ? `Update ${filePath}` : `Create ${filePath}`), files: [{ path: filePath, content }] })
      await qc.invalidateQueries({ queryKey: qk.r(org, repo) })
      toast.push({ tone: 'ok', title: 'Committed through the API', body: `${res.ref} → ${res.commit.slice(0, 7)}. No clone was needed.` })
      onClose()
      void navigate({ to: '/$org/$repo/blob/$ref/$', params: { org, repo, ref: branch, _splat: filePath } as never })
    } catch {
      /* toast by API layer */
    } finally {
      setBusy(false)
    }
  }
  const protectedBranch = refFull === `refs/heads/${features ? '' : ''}` // placeholder; protection is enforced server-side
  void protectedBranch
  return (
    <Dialog open={open} onClose={onClose} title={path ? `Edit ${path}` : 'New file'} wide foot={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={busy} disabled={!filePath.trim()} onClick={() => void submit()}>Commit to {branch || 'branch'}</Button></>}>
      <div className="stack">
        <div className="grid-2">
          <Field label="Path" htmlFor="edit-path">
            <Input id="edit-path" value={filePath} onChange={(e) => setFilePath(e.target.value)} placeholder="src/main.go" disabled={!!path} />
          </Field>
          <Field label="Branch" htmlFor="edit-branch" hint="A new name creates the branch from the default branch; open a change from it afterwards.">
            <Input id="edit-branch" value={branch} onChange={(e) => setBranch(e.target.value)} />
          </Field>
        </div>
        <Field label="Contents" htmlFor="edit-content">
          <Textarea id="edit-content" mono value={content} onChange={(e) => setContent(e.target.value)} rows={18} spellCheck={false} style={{ minHeight: 320 }} />
        </Field>
        <Field label="Commit message" htmlFor="edit-msg">
          <Input id="edit-msg" value={message} onChange={(e) => setMessage(e.target.value)} placeholder={path ? `Update ${path}` : `Create ${filePath}`} />
        </Field>
        <p className="small muted">
          This is a server-side commit (<code>POST /commits</code>): Ore writes the tree and moves the ref with compare-and-swap. The default branch is protected against direct pushes over git but not against API commits by authorized users; prefer a branch and a change for anything reviewed.
        </p>
      </div>
    </Dialog>
  )
}

function EmptyRepo({ org, repo, branch }: { org: string; repo: string; branch: string }) {
  const { data: features } = useFeatures()
  const base = features?.base_url || window.location.origin
  return (
    <Card>
      <EmptyState icon={<GitCommitHorizontal />} title="This repository is empty" command={`cd your-project\ngit remote add ore ${base}/${org}/${repo}.git\ngit push ore ${branch}\n\n# or, without a clone:\nore commit ${org}/${repo} -m "import" $(git ls-files)`}>
        Push an existing project over HTTP (any user name, your token as the password) or SSH, or commit files through the API. The first push creates the default branch <Pill small>{branch}</Pill>, which is protected: later work goes through branches and changes.
      </EmptyState>
    </Card>
  )
}
