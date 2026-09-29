// 展示格式化工具。

export function fmtTokens(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
  return String(n ?? 0);
}

export function fmtCost(n: number): string {
  if (n === 0) return '$0';
  if (n < 0.01) return '$' + n.toFixed(5);
  if (n < 1) return '$' + n.toFixed(4);
  return '$' + n.toFixed(2);
}

export function fmtTime(ts: number): string {
  if (!ts) return '-';
  const d = new Date(ts * 1000);
  const now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
  if (sameDay) return hm;
  return `${d.getMonth() + 1}/${d.getDate()} ${hm}`;
}

export function fmtBytes(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + 'GB';
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + 'MB';
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(0) + 'KB';
  return n + 'B';
}

export function fmtHitRate(cr: number, input: number, cc: number): string {
  const total = cr + input + cc;
  if (!total) return '-';
  return ((cr / total) * 100).toFixed(1) + '%';
}

export const channelLabels: Record<string, string> = {
  main: '主对话',
  lightweight: '轻量',
  subagent: '子agent',
  default: '默认',
};

export const channelBadge: Record<string, string> = {
  main: 'bg-sky-100 text-sky-700 dark:bg-sky-950 dark:text-sky-300',
  lightweight: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300',
  subagent: 'bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-300',
  default: 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300',
};

/** 提取消息块的纯文本预览 */
export function blockText(content: unknown): string {
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) {
    return content
      .map((b) => {
        const o = b as Record<string, unknown>;
        if (typeof o.text === 'string') return o.text;
        if (o.type === 'tool_use') return `[工具调用 ${String(o.name ?? '')}]`;
        if (o.type === 'tool_result') return '[工具结果]';
        return '';
      })
      .filter(Boolean)
      .join('\n');
  }
  if (content && typeof content === 'object') return JSON.stringify(content).slice(0, 200);
  return '';
}
