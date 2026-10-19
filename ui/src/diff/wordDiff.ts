// Intra-line (word level) differences for paired deletion/addition lines.

export type Segment = { text: string; changed: boolean }

function tokenize(s: string): string[] {
  return s.match(/\w+|\s+|[^\w\s]/g) ?? []
}

/** LCS over word tokens; returns segments for both sides. Bounded so pathological lines stay cheap. */
export function wordDiff(a: string, b: string): { left: Segment[]; right: Segment[] } {
  const ta = tokenize(a)
  const tb = tokenize(b)
  if (ta.length * tb.length > 40_000 || ta.length === 0 || tb.length === 0) {
    return { left: [{ text: a, changed: a !== b }], right: [{ text: b, changed: a !== b }] }
  }
  const n = ta.length
  const m = tb.length
  const dp: Uint16Array[] = []
  for (let i = 0; i <= n; i++) dp.push(new Uint16Array(m + 1))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i]![j] = ta[i] === tb[j] ? dp[i + 1]![j + 1]! + 1 : Math.max(dp[i + 1]![j]!, dp[i]![j + 1]!)
    }
  }
  const left: Segment[] = []
  const right: Segment[] = []
  const push = (arr: Segment[], text: string, changed: boolean) => {
    const last = arr[arr.length - 1]
    if (last && last.changed === changed) last.text += text
    else arr.push({ text, changed })
  }
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (ta[i] === tb[j]) {
      push(left, ta[i]!, false)
      push(right, tb[j]!, false)
      i++
      j++
    } else if (dp[i + 1]![j]! >= dp[i]![j + 1]!) {
      push(left, ta[i]!, true)
      i++
    } else {
      push(right, tb[j]!, true)
      j++
    }
  }
  while (i < n) push(left, ta[i++]!, true)
  while (j < m) push(right, tb[j++]!, true)
  // If almost everything changed, highlighting is noise.
  const changedRatio = (segs: Segment[]) => segs.filter((s) => s.changed).reduce((x, s) => x + s.text.length, 0) / Math.max(1, segs.reduce((x, s) => x + s.text.length, 0))
  if (changedRatio(left) > 0.7 && changedRatio(right) > 0.7) {
    return { left: [{ text: a, changed: false }], right: [{ text: b, changed: false }] }
  }
  return { left, right }
}
