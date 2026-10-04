<script lang="ts">
import { defineComponent } from 'vue';
import { api, Settings } from '../api';
import { toast, confirmDialog } from '../toast';
import { theme, setThemeMode, ThemeMode } from '../theme';
import Toggle from '../components/Toggle.vue';
import Select from '../components/Select.vue';
import { fmtBytes } from '../format';
import IconCopy from '~icons/lucide/copy';
import IconCheck from '~icons/lucide/check';

export default defineComponent({
  name: 'SettingsView',
  components: { Toggle, Select, IconCopy, IconCheck },
  data() {
    return {
      s: null as Settings | null,
      dropped: 0,
      saving: false,
      saved: false,
      routerKey: '',
      rotating: false,
      copied: false,
      themeMode: theme.mode as ThemeMode,
      themeOptions: [
        { value: 'system', label: '跟随系统' },
        { value: 'light', label: '浅色' },
        { value: 'dark', label: '深色' },
      ],
    };
  },
  mounted() {
    Promise.all([api.settings(), api.overview('all'), api.connection()])
      .then(([s, ov, c]) => {
        this.s = s;
        this.dropped = ov.stats_dropped;
        this.routerKey = c.router_key;
      })
      .catch((e) => toast('error', e.message));
  },
  methods: {
    fmtBytes,
    save(): void {
      if (!this.s) return;
      this.saving = true;
      this.saved = false;
      api.putSettings(this.s)
        .then(() => { this.saved = true; setTimeout(() => { this.saved = false; }, 2000); })
        .catch((e) => toast('error', e.message))
        .finally(() => { this.saving = false; });
    },
    copyKey(): void {
      if (!this.routerKey) return;
      navigator.clipboard.writeText(this.routerKey).then(() => {
        this.copied = true;
        setTimeout(() => { this.copied = false; }, 1500);
      });
    },
    onThemeChange(): void {
      setThemeMode(this.themeMode);
    },
    rotateKey(): void {
      confirmDialog('轮换后旧 Key 立即失效, 需同步更新 cc-switch / 客户端中的 API Key。确定继续?', '轮换 API Key', '轮换')
        .then((ok) => {
          if (!ok) return;
          this.rotating = true;
          api.rotateKey()
            .then((r) => {
              this.routerKey = r.router_key;
              toast('success', '已生成新 API Key, 请同步更新客户端配置');
            })
            .catch((e) => toast('error', e.message))
            .finally(() => { this.rotating = false; });
        });
    },
  },
});
</script>

<template>
  <div>
    <div class="mb-5 flex items-center justify-between">
      <h1 class="text-xl font-bold">设置</h1>
      <button v-if="s" class="btn-primary" :disabled="saving" @click="save">{{ saved ? '已保存 ✓' : (saving ? '保存中…' : '保存全部') }}</button>
    </div>

    <div v-if="s" class="space-y-4">
      <div class="card p-5">
        <h2 class="mb-3 text-sm font-semibold">常规</h2>
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label class="label">监听端口(保存后需重启进程生效)</label>
            <input v-model.number="s.port" class="input !w-40" type="number" />
          </div>
          <div>
            <label class="label">请求明细保留天数(天级聚合永久保留)</label>
            <input v-model.number="s.retention_days" class="input !w-40" type="number" />
          </div>
          <div>
            <label class="label">外观(即时生效, 保存在本机)</label>
            <Select v-model="themeMode" :options="themeOptions" width="w-40" @change="onThemeChange" />
          </div>
        </div>
        <div class="mt-4">
          <Toggle v-model="s.inject_openai_usage" label="OpenAI 流式请求自动注入 stream_options.include_usage(用于统计用量, 不影响内容)" />
        </div>
        <div class="mt-4">
          <Toggle v-model="s.unload_on_minimise" label="最小化到后台时卸载页面(桌面版: 释放渲染内存, 还原窗口时自动恢复当前页面)" />
        </div>
      </div>

      <div class="card p-5">
        <h2 class="mb-1 text-sm font-semibold">API Key</h2>
        <p class="mb-3 text-xs text-slate-400">客户端(cc-switch 等)访问本网关的密钥。轮换后旧 Key 立即失效, 需同步更新客户端配置。</p>
        <div class="flex items-center gap-2">
          <code class="min-w-0 flex-1 truncate rounded-xl bg-slate-50 p-3 font-mono text-sm dark:bg-slate-950/60">{{ routerKey || '(读取中)' }}</code>
          <button class="btn-ghost !px-2" @click="copyKey"><IconCheck v-if="copied" class="h-4 w-4 text-emerald-500" /><IconCopy v-else class="h-4 w-4" /></button>
          <button class="btn-primary" :disabled="rotating || !routerKey" @click="rotateKey">{{ rotating ? '轮换中…' : '轮换密钥' }}</button>
        </div>
      </div>

      <div class="card p-5">
        <h2 class="mb-1 text-sm font-semibold">全局提示词注入</h2>
        <p class="mb-3 text-xs text-slate-400">追加到每个请求的 system 末尾(Anthropic)或首条 system 消息(OpenAI)。用于放置希望跨压缩存活的全局守则。</p>
        <Toggle v-model="s.inject.enabled" label="启用注入" class="mb-3" />
        <textarea v-model="s.inject.text" class="input h-40 font-mono text-xs" placeholder="例如:&#10;- 始终使用简体中文回复&#10;- 修改代码前先说明改动点&#10;- 不要引入新的依赖" :disabled="!s.inject.enabled" />
      </div>

      <div class="card p-5">
        <h2 class="mb-1 text-sm font-semibold">会话归档</h2>
        <p class="mb-3 text-xs text-slate-400">归档完整请求上下文到本地磁盘(明文), 用于找回被压缩/丢失的上下文。</p>
        <div class="flex flex-wrap items-center gap-6">
          <Toggle v-model="s.archive.enabled" label="启用归档" />
          <label class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
            每会话保留快照数
            <input v-model.number="s.archive.keep_per_session" class="input !w-20" type="number" min="1" max="20" :disabled="!s.archive.enabled" />
          </label>
          <label class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
            单份上限 MB
            <input v-model.number="s.archive.max_snapshot_mb" class="input !w-20" type="number" :disabled="!s.archive.enabled" />
          </label>
          <label class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
            全局上限 GB
            <input v-model.number="s.archive.global_cap_gb" class="input !w-20" type="number" :disabled="!s.archive.enabled" />
          </label>
        </div>
        <p class="mt-3 text-[11px] text-slate-400">超过全局上限时从最旧快照开始清理(LRU)。后台探测类小请求(无工具定义)不会归档。</p>
      </div>

      <div class="card p-5">
        <h2 class="mb-3 text-sm font-semibold">运行状态</h2>
        <div class="text-xs text-slate-500 dark:text-slate-400">
          统计队列累计丢弃: <span class="font-mono" :class="dropped > 0 && 'text-amber-500'">{{ dropped }}</span> 条
          (队列满载时为保证转发零阻塞而丢弃统计, 不影响请求)
        </div>
      </div>
    </div>
  </div>
</template>
