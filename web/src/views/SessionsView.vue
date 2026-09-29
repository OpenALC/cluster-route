<script lang="ts">
import { defineComponent } from 'vue';
import { api, SessionRow, SnapshotContent, SnapshotRow } from '../api';
import { toast, confirmDialog } from '../toast';
import { fmtTime, fmtBytes, blockText } from '../format';
import Modal from '../components/Modal.vue';
import IconTrash from '~icons/lucide/trash-2';
import IconFolder from '~icons/lucide/folder';
import IconChevron from '~icons/lucide/chevron-down';
import IconDownload from '~icons/lucide/download';

interface Group {
  project: string;
  sessions: SessionRow[];
}

export default defineComponent({
  name: 'SessionsView',
  components: { Modal, IconTrash, IconFolder, IconChevron, IconDownload },
  data() {
    return {
      sessions: [] as SessionRow[],
      loading: false,
      collapsed: {} as Record<string, boolean>,
      openKey: '',
      snaps: [] as SnapshotRow[],
      viewSnap: null as SnapshotContent | null,
      viewTitle: '',
    };
  },
  computed: {
    groups(): Group[] {
      const map = new Map<string, SessionRow[]>();
      for (const s of this.sessions) {
        const k = s.project || '未分组';
        if (!map.has(k)) map.set(k, []);
        map.get(k)!.push(s);
      }
      return Array.from(map.entries()).map(([project, ss]) => ({ project, sessions: ss }));
    },
  },
  mounted() {
    this.load();
  },
  methods: {
    fmtTime(ts: number): string { return fmtTime(ts); },
    fmtBytes(n: number): string { return fmtBytes(n); },
    blockText(content: unknown): string { return blockText(content); },
    load(): void {
      this.loading = true;
      api.sessions().then((s) => { this.sessions = s || []; }).catch((e) => toast('error', e.message)).finally(() => { this.loading = false; });
    },
    toggleGroup(project: string): void {
      this.collapsed[project] = !this.collapsed[project];
    },
    openSession(s: SessionRow): void {
      this.openKey = s.key;
      api.snapshots(s.key).then((list) => {
        this.snaps = list || [];
        if (this.snaps.length) this.viewSnapshot(this.snaps[0]);
      }).catch((e) => toast('error', e.message));
    },
    viewSnapshot(row: SnapshotRow): void {
      api.snapshotContent(row.id).then((c) => {
        this.viewSnap = c;
        this.viewTitle = `${fmtTime(row.ts)} · ${row.model || ''} · ${row.msg_count} 条消息`;
      }).catch((e) => toast('error', e.message));
    },
    async removeSession(s: SessionRow): Promise<void> {
      const okGo = await confirmDialog(`删除会话「${s.title.slice(0, 30)}」及其全部快照?`, '删除会话', '删除');
      if (!okGo) return;
      try {
        await api.deleteSession(s.key);
        if (this.openKey === s.key) { this.openKey = ''; this.snaps = []; this.viewSnap = null; }
        toast('success', '会话已删除');
        this.load();
      } catch (e) { toast('error', (e as Error).message); }
    },
    exportSnapshot(): void {
      if (!this.viewSnap) return;
      const text = (this.viewSnap.messages || [])
        .map((m) => {
          const role = String(m.role ?? '?');
          return `## ${role}\n\n${blockText(m.content)}`;
        })
        .join('\n\n---\n\n');
      const blob = new Blob([text], { type: 'text/markdown;charset=utf-8' });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = `context-${this.openKey.slice(0, 8)}.md`;
      a.click();
      URL.revokeObjectURL(a.href);
    },
    roleBadge(role: string): string {
      if (role === 'user') return 'bg-sky-100 text-sky-700 dark:bg-sky-950 dark:text-sky-300';
      if (role === 'assistant') return 'bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-300';
      return 'bg-slate-200 text-slate-600 dark:bg-slate-800 dark:text-slate-300';
    },
  },
});
</script>

<template>
  <div>
    <div class="mb-5">
      <h1 class="text-xl font-bold">会话</h1>
    </div>

    <div class="space-y-4">
      <div v-for="g in groups" :key="g.project" class="card overflow-hidden">
        <button class="flex w-full items-center gap-2 px-4 py-3 text-left hover:bg-slate-50 dark:hover:bg-slate-800/50" @click="toggleGroup(g.project)">
          <IconChevron class="h-4 w-4 text-slate-400 transition-transform" :class="collapsed[g.project] && '-rotate-90'" />
          <IconFolder class="h-4 w-4 text-amber-500" />
          <span class="text-sm font-semibold">{{ g.project }}</span>
          <span class="badge bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400">{{ g.sessions.length }} 个会话</span>
        </button>
        <div v-show="!collapsed[g.project]" class="divide-y divide-slate-100 border-t border-slate-100 dark:divide-slate-800 dark:border-slate-800">
          <div
            v-for="s in g.sessions" :key="s.key"
            class="flex cursor-pointer items-center gap-3 px-4 py-2.5 transition hover:bg-slate-50 dark:hover:bg-slate-800/40"
            :class="openKey === s.key && 'bg-sky-50/60 dark:bg-sky-950/30'"
            @click="openSession(s)"
          >
            <div class="min-w-0 flex-1">
              <div class="truncate text-sm font-medium">{{ s.title || '(无标题)' }}</div>
              <div class="mt-0.5 text-xs text-slate-400">
                {{ fmtTime(s.updated_at) }} · {{ s.msg_count }} 条消息 · {{ fmtBytes(s.size_bytes) }} · {{ s.last_model }}
              </div>
            </div>
            <button class="btn-danger !px-2" @click.stop="removeSession(s)"><IconTrash class="h-4 w-4" /></button>
          </div>
        </div>
      </div>
      <div v-if="!sessions.length && !loading" class="card p-10 text-center text-sm text-slate-400">
        暂无归档。经中转站转发的 Claude Code 对话(含工具定义的请求)会自动留档。
      </div>
    </div>

    <Modal :open="!!viewSnap" :title="'会话上下文 · ' + viewTitle" wide @close="viewSnap = null">
      <div v-if="viewSnap">
        <div class="mb-3 flex items-center justify-between">
          <div class="flex gap-1.5 overflow-x-auto">
            <button
              v-for="row in snaps" :key="row.id"
              class="badge whitespace-nowrap ring-1 transition"
              :class="viewTitle.includes(fmtTime(row.ts)) ? 'bg-sky-600 text-white ring-sky-600' : 'bg-white text-slate-500 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700'"
              @click="viewSnapshot(row)"
            >{{ fmtTime(row.ts) }}</button>
          </div>
          <button class="btn-ghost !py-1 text-xs" @click="exportSnapshot"><IconDownload class="h-3.5 w-3.5" /> 导出 MD</button>
        </div>
        <div class="max-h-[60vh] space-y-2 overflow-y-auto pr-1">
          <div v-for="(m, i) in viewSnap.messages" :key="i" class="rounded-xl bg-slate-50 p-3 dark:bg-slate-950/60">
            <div class="mb-1.5 flex items-center gap-2">
              <span class="badge" :class="roleBadge(String(m.role ?? 'system'))">{{ String(m.role ?? 'system') }}</span>
            </div>
            <pre class="whitespace-pre-wrap break-words font-sans text-xs leading-relaxed text-slate-600 dark:text-slate-300">{{ blockText(m.content) || '(空)' }}</pre>
          </div>
        </div>
      </div>
    </Modal>
  </div>
</template>
