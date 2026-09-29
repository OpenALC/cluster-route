<script lang="ts">
import { defineComponent } from 'vue';
import { api, Provider, RequestRow } from '../api';
import { toast } from '../toast';
import { fmtTokens, fmtCost, fmtTime, channelLabels, channelBadge } from '../format';

export default defineComponent({
  name: 'LogsView',
  data() {
    return {
      rows: [] as RequestRow[],
      providers: [] as Provider[],
      range: '24h',
      channel: '',
      provider: '',
      status: '',
      model: '',
      loading: false,
      expanded: 0,
    };
  },
  mounted() {
    api.providers().then((p) => { this.providers = p || []; }).catch(() => undefined);
    this.load();
  },
  methods: {
    fmtTokens(n: number): string { return fmtTokens(n); },
    fmtCost(n: number): string { return fmtCost(n); },
    fmtTime(ts: number): string { return fmtTime(ts); },
    chLabel(key: string): string { return channelLabels[key] || key; },
    chBadge(key: string): string { return channelBadge[key] || ''; },
    load(): void {
      this.loading = true;
      const q = new URLSearchParams({
        range: this.range, limit: '200',
        channel: this.channel, provider: this.provider, status: this.status, model: this.model,
      });
      api.requests(q.toString())
        .then((r) => { this.rows = r || []; })
        .catch((e) => toast('error', e.message))
        .finally(() => { this.loading = false; });
    },
    toggleExpand(id: number): void {
      this.expanded = this.expanded === id ? 0 : id;
    },
    failoverList(row: RequestRow): { provider: string; model: string; status: number; err: string }[] {
      try { return JSON.parse(row.failover || '[]'); } catch { return []; }
    },
  },
});
</script>

<template>
  <div>
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-xl font-bold">日志</h1>
      <div class="flex flex-wrap items-center gap-2">
        <select v-model="range" class="input !w-28 !py-1.5 text-xs" @change="load">
          <option value="24h">24小时</option>
          <option value="7d">7天</option>
          <option value="30d">30天</option>
          <option value="all">全部</option>
        </select>
        <select v-model="channel" class="input !w-28 !py-1.5 text-xs" @change="load">
          <option value="">全部通道</option>
          <option value="main">主对话</option>
          <option value="lightweight">轻量</option>
          <option value="subagent">子agent</option>
          <option value="default">默认</option>
        </select>
        <select v-model="provider" class="input !w-32 !py-1.5 text-xs" @change="load">
          <option value="">全部供应商</option>
          <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
        <select v-model="status" class="input !w-24 !py-1.5 text-xs" @change="load">
          <option value="">全部状态</option>
          <option value="ok">成功</option>
          <option value="error">失败</option>
        </select>
        <input v-model="model" class="input !w-36 !py-1.5 text-xs" placeholder="模型过滤" @keyup.enter="load" />
        <button class="btn-ghost !py-1.5 text-xs" @click="load">查询</button>
      </div>
    </div>

    <div class="card overflow-x-auto">
      <table class="w-full min-w-[900px]">
        <thead>
          <tr class="border-b border-slate-100 dark:border-slate-800">
            <th class="th">时间</th>
            <th class="th">模型</th>
            <th class="th">供应商</th>
            <th class="th">通道</th>
            <th class="th">状态</th>
            <th class="th">输入</th>
            <th class="th">缓存命中</th>
            <th class="th">缓存写入</th>
            <th class="th">输出</th>
            <th class="th">耗时</th>
            <th class="th">费用</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length"><td colspan="11" class="td py-10 text-center text-slate-400">{{ loading ? '加载中…' : '暂无请求' }}</td></tr>
          <template v-for="row in rows" :key="row.id">
            <tr
              class="cursor-pointer border-b border-slate-50 hover:bg-slate-50 dark:border-slate-800/60 dark:hover:bg-slate-800/40"
              @click="toggleExpand(row.id)"
            >
              <td class="td whitespace-nowrap text-xs">{{ fmtTime(row.ts) }}</td>
              <td class="td font-mono text-xs">{{ row.model }}<span v-if="row.upstream_model !== row.model" class="text-slate-400"> → {{ row.upstream_model }}</span></td>
              <td class="td text-xs">{{ row.provider_name }}</td>
              <td class="td"><span class="badge" :class="chBadge(row.channel)">{{ chLabel(row.channel) }}</span></td>
              <td class="td">
                <span v-if="row.ok" class="badge bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300">OK</span>
                <span v-else class="badge bg-rose-100 text-rose-700 dark:bg-rose-950 dark:text-rose-300">{{ row.status || 'ERR' }}</span>
              </td>
              <td class="td text-xs">{{ fmtTokens(row.input_tokens) }}</td>
              <td class="td text-xs text-emerald-600 dark:text-emerald-400">{{ fmtTokens(row.cache_read_tokens) }}</td>
              <td class="td text-xs text-amber-600 dark:text-amber-400">{{ fmtTokens(row.cache_creation_tokens) }}</td>
              <td class="td text-xs">{{ fmtTokens(row.output_tokens) }}</td>
              <td class="td text-xs whitespace-nowrap">{{ row.duration_ms }}ms</td>
              <td class="td text-xs">{{ fmtCost(row.cost) }}</td>
            </tr>
            <tr v-if="expanded === row.id">
              <td colspan="11" class="bg-slate-50 px-4 py-3 dark:bg-slate-950/60">
                <div class="text-xs text-slate-500 dark:text-slate-400">
                  <div>格式: {{ row.format }} · 流式: {{ row.stream ? '是' : '否' }} · TTFB: {{ row.ttfb_ms }}ms · 项目: {{ row.project || '-' }}</div>
                  <div v-if="failoverList(row).length" class="mt-1">
                    切换轨迹:
                    <span v-for="(f, i) in failoverList(row)" :key="i" class="mr-2 font-mono">
                      {{ f.provider }}({{ f.model }}){{ f.status ? '[' + f.status + ']' : '' }}{{ f.err ? ' ' + f.err : '' }} →
                    </span>
                    <span class="font-mono">{{ row.provider_name }}({{ row.upstream_model }})</span>
                  </div>
                  <div v-if="row.err" class="mt-1 font-mono text-rose-500">{{ row.err }}</div>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
  </div>
</template>
