<script lang="ts">
import { defineComponent } from 'vue';
import { api, Totals, GroupAgg, Point } from '../api';
import { fmtTokens, fmtCost, fmtHitRate, fmtTime, channelLabels } from '../format';
import StatCard from '../components/StatCard.vue';
import LineChart from '../components/LineChart.vue';

type Range = '24h' | '7d' | '30d' | 'all';

export default defineComponent({
  name: 'OverviewView',
  components: { StatCard, LineChart },
  data() {
    return {
      range: '24h' as Range,
      ranges: [
        { v: '24h', label: '24小时' },
        { v: '7d', label: '7天' },
        { v: '30d', label: '30天' },
        { v: 'all', label: '全部' },
      ] as { v: Range; label: string }[],
      totals: {
        requests: 0, errors: 0, input_tokens: 0, cache_read_tokens: 0,
        cache_creation_tokens: 0, output_tokens: 0, cost: 0,
      } as Totals,
      byProvider: [] as GroupAgg[],
      byModel: [] as GroupAgg[],
      byChannel: [] as GroupAgg[],
      points: [] as Point[],
      loading: false,
      dropped: 0,
    };
  },
  computed: {
    tokensOption(): object {
      const p = this.points;
      return {
        tooltip: { trigger: 'axis' },
        legend: { bottom: 0, icon: 'roundRect', itemWidth: 12, itemHeight: 6 },
        grid: { left: 44, right: 12, top: 16, bottom: 44 },
        xAxis: { type: 'category', data: p.map((x) => fmtTime(x.bucket)), axisLabel: { fontSize: 10 } },
        yAxis: { type: 'value', axisLabel: { fontSize: 10, formatter: (v: number) => fmtTokens(v) } },
        series: [
          { name: '输入', type: 'line', stack: 't', areaStyle: { opacity: 0.25 }, smooth: true, symbol: 'none', lineStyle: { width: 1.5 }, data: p.map((x) => x.input_tokens - x.cache_read_tokens - x.cache_creation_tokens < 0 ? 0 : x.input_tokens - x.cache_read_tokens - x.cache_creation_tokens), itemStyle: { color: '#0ea5e9' } },
          { name: '缓存命中', type: 'line', stack: 't', areaStyle: { opacity: 0.25 }, smooth: true, symbol: 'none', lineStyle: { width: 1.5 }, data: p.map((x) => x.cache_read_tokens), itemStyle: { color: '#10b981' } },
          { name: '缓存写入', type: 'line', stack: 't', areaStyle: { opacity: 0.25 }, smooth: true, symbol: 'none', lineStyle: { width: 1.5 }, data: p.map((x) => x.cache_creation_tokens), itemStyle: { color: '#f59e0b' } },
          { name: '输出', type: 'line', stack: 't', areaStyle: { opacity: 0.25 }, smooth: true, symbol: 'none', lineStyle: { width: 1.5 }, data: p.map((x) => x.output_tokens), itemStyle: { color: '#8b5cf6' } },
        ],
      };
    },
    costOption(): object {
      const p = this.points;
      return {
        tooltip: { trigger: 'axis', valueFormatter: (v: number) => fmtCost(v) },
        grid: { left: 52, right: 12, top: 16, bottom: 24 },
        xAxis: { type: 'category', data: p.map((x) => fmtTime(x.bucket)), axisLabel: { fontSize: 10 } },
        yAxis: { type: 'value', axisLabel: { fontSize: 10, formatter: (v: number) => fmtCost(v) } },
        series: [{ name: '费用', type: 'line', smooth: true, symbol: 'none', areaStyle: { opacity: 0.15 }, lineStyle: { width: 2 }, data: p.map((x) => x.cost), itemStyle: { color: '#f43f5e' } }],
      };
    },
    hitOption(): object {
      const p = this.points;
      return {
        tooltip: { trigger: 'axis', valueFormatter: (v: number) => Number(v).toFixed(1) + '%' },
        grid: { left: 44, right: 12, top: 16, bottom: 24 },
        xAxis: { type: 'category', data: p.map((x) => fmtTime(x.bucket)), axisLabel: { fontSize: 10 } },
        yAxis: { type: 'value', max: 100, axisLabel: { fontSize: 10, formatter: '{value}%' } },
        series: [{
          name: '缓存命中率', type: 'line', smooth: true, symbol: 'none', lineStyle: { width: 2 },
          data: p.map((x) => {
            const t = x.input_tokens + x.cache_read_tokens + x.cache_creation_tokens;
            return t ? Number(((x.cache_read_tokens / t) * 100).toFixed(2)) : 0;
          }),
          itemStyle: { color: '#10b981' },
        }],
      };
    },
  },
  mounted() {
    this.load();
  },
  methods: {
    fmtTokens(n: number): string { return fmtTokens(n); },
    fmtCost(n: number): string { return fmtCost(n); },
    fmtHitRate(cr: number, input: number, cc: number): string { return fmtHitRate(cr, input, cc); },
    chLabel(key: string): string { return channelLabels[key] || key; },
    load(): void {
      this.loading = true;
      const bucket = this.range === '24h' ? 'hour' : this.range === '7d' ? 'hour' : 'day';
      Promise.all([api.overview(this.range), api.series(this.range, bucket)])
        .then(([ov, pts]) => {
          this.totals = ov.totals;
          this.byProvider = ov.by_provider || [];
          this.byModel = ov.by_model || [];
          this.byChannel = ov.by_channel || [];
          this.dropped = ov.stats_dropped;
          this.points = pts || [];
        })
        .catch((e) => console.error('加载失败:', e))
        .finally(() => { this.loading = false; });
    },
  },
});
</script>

<template>
  <div>
    <div class="mb-5 flex items-center justify-between">
      <h1 class="text-xl font-bold">总览</h1>
      <div class="flex items-center gap-2">
        <div class="flex rounded-xl border border-slate-200 bg-white p-1 dark:border-slate-700 dark:bg-slate-900">
          <button
            v-for="r in ranges" :key="r.v"
            class="rounded-lg px-3 py-1 text-xs font-medium transition"
            :class="range === r.v ? 'bg-sky-600 text-white' : 'text-slate-500 hover:text-slate-800 dark:hover:text-slate-200'"
            @click="range = r.v; load()"
          >{{ r.label }}</button>
        </div>
      </div>
    </div>

    <div class="grid grid-cols-2 gap-3 md:grid-cols-4 lg:grid-cols-7">
      <StatCard label="请求数" :value="String(totals.requests)" :sub="`失败 ${totals.errors}`" tone="sky" />
      <StatCard label="总 Tokens" :value="fmtTokens(totals.input_tokens + totals.output_tokens)" sub="输入+输出" tone="slate" />
      <StatCard label="输入" :value="fmtTokens(totals.input_tokens)" sub="含缓存部分" tone="sky" />
      <StatCard label="缓存命中" :value="fmtTokens(totals.cache_read_tokens)" :sub="fmtHitRate(totals.cache_read_tokens, totals.input_tokens, totals.cache_creation_tokens) + ' 命中率'" tone="emerald" />
      <StatCard label="缓存写入" :value="fmtTokens(totals.cache_creation_tokens)" tone="amber" />
      <StatCard label="输出" :value="fmtTokens(totals.output_tokens)" tone="violet" />
      <StatCard label="总费用" :value="fmtCost(totals.cost)" tone="rose" />
    </div>

    <div class="mt-5 grid gap-4 lg:grid-cols-2">
      <div class="card p-4 lg:col-span-2">
        <h2 class="mb-2 text-sm font-semibold">Tokens 用量趋势</h2>
        <LineChart :option="tokensOption" height="260px" />
      </div>
      <div class="card p-4">
        <h2 class="mb-2 text-sm font-semibold">费用趋势</h2>
        <LineChart :option="costOption" height="220px" />
      </div>
      <div class="card p-4">
        <h2 class="mb-2 text-sm font-semibold">缓存命中率趋势</h2>
        <LineChart :option="hitOption" height="220px" />
      </div>
    </div>

    <div class="mt-5 grid gap-4 lg:grid-cols-3">
      <div class="card overflow-hidden">
        <h2 class="px-4 pt-4 text-sm font-semibold">分供应商</h2>
        <table class="mt-2 w-full">
          <thead><tr class="border-b border-slate-100 dark:border-slate-800"><th class="th">供应商</th><th class="th">请求</th><th class="th">Tokens</th><th class="th">费用</th></tr></thead>
          <tbody>
            <tr v-if="!byProvider.length"><td colspan="4" class="td text-center text-slate-400">暂无数据</td></tr>
            <tr v-for="g in byProvider" :key="g.key" class="border-b border-slate-50 dark:border-slate-800/50">
              <td class="td font-medium">{{ g.key }}</td>
              <td class="td">{{ g.requests }}</td>
              <td class="td">{{ fmtTokens(g.input_tokens + g.output_tokens) }}</td>
              <td class="td">{{ fmtCost(g.cost) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="card overflow-hidden">
        <h2 class="px-4 pt-4 text-sm font-semibold">分模型</h2>
        <table class="mt-2 w-full">
          <thead><tr class="border-b border-slate-100 dark:border-slate-800"><th class="th">模型</th><th class="th">请求</th><th class="th">Tokens</th><th class="th">费用</th></tr></thead>
          <tbody>
            <tr v-if="!byModel.length"><td colspan="4" class="td text-center text-slate-400">暂无数据</td></tr>
            <tr v-for="g in byModel" :key="g.key" class="border-b border-slate-50 dark:border-slate-800/50">
              <td class="td font-mono text-xs font-medium">{{ g.key }}</td>
              <td class="td">{{ g.requests }}</td>
              <td class="td">{{ fmtTokens(g.input_tokens + g.output_tokens) }}</td>
              <td class="td">{{ fmtCost(g.cost) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="card overflow-hidden">
        <h2 class="px-4 pt-4 text-sm font-semibold">分通道</h2>
        <table class="mt-2 w-full">
          <thead><tr class="border-b border-slate-100 dark:border-slate-800"><th class="th">通道</th><th class="th">请求</th><th class="th">Tokens</th><th class="th">费用</th></tr></thead>
          <tbody>
            <tr v-if="!byChannel.length"><td colspan="4" class="td text-center text-slate-400">暂无数据</td></tr>
            <tr v-for="g in byChannel" :key="g.key" class="border-b border-slate-50 dark:border-slate-800/50">
              <td class="td font-medium">{{ chLabel(g.key) }}</td>
              <td class="td">{{ g.requests }}</td>
              <td class="td">{{ fmtTokens(g.input_tokens + g.output_tokens) }}</td>
              <td class="td">{{ fmtCost(g.cost) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <p v-if="dropped > 0" class="mt-4 text-xs text-amber-500">注意: 统计队列曾满载, 已丢弃 {{ dropped }} 条记录(不影响转发)。</p>
  </div>
</template>
