<script lang="ts">
import { defineComponent } from 'vue';
import { api, Provider, Route as RouteRow, Settings, Target } from '../api';
import { toast } from '../toast';
import Toggle from '../components/Toggle.vue';
import Select from '../components/Select.vue';
import IconPlus from '~icons/lucide/plus';
import IconTrash from '~icons/lucide/trash-2';
import IconUp from '~icons/lucide/arrow-up';
import IconDown from '~icons/lucide/arrow-down';
import IconZap from '~icons/lucide/zap';
import IconBot from '~icons/lucide/bot';
import IconShield from '~icons/lucide/shield-alert';
import IconList from '~icons/lucide/list-ordered';

type Tab = 'routes' | 'lightweight' | 'subagent' | 'failover';

export default defineComponent({
  name: 'ChannelsView',
  components: { Toggle, Select, IconPlus, IconTrash, IconUp, IconDown, IconZap, IconBot, IconShield, IconList },
  data() {
    return {
      tab: 'routes' as Tab,
      tabs: [
        { v: 'routes', label: '常规路由', icon: IconList },
        { v: 'lightweight', label: '轻量通道', icon: IconZap },
        { v: 'subagent', label: '子agent', icon: IconBot },
        { v: 'failover', label: '限额切换', icon: IconShield },
      ] as { v: Tab; label: string; icon: unknown }[],
      providers: [] as Provider[],
      routes: [] as RouteRow[],
      settings: null as Settings | null,
      saving: false,
    };
  },
  computed: {
    providerName(): (id: string) => string {
      const map: Record<string, string> = {};
      this.providers.forEach((p) => { map[p.id] = p.name; });
      return (id: string) => map[id] || '(未知供应商)';
    },
    providerOptions(): { value: string; label: string }[] {
      return this.providers.map((p) => ({ value: p.id, label: p.name }));
    },
    providerOptionsWithNone(): (label: string) => { value: string; label: string }[] {
      return (label: string) => [{ value: '', label }, ...this.providerOptions];
    },
  },
  mounted() {
    Promise.all([api.providers(), api.routes(), api.settings()])
      .then(([p, r, s]) => {
        this.providers = p;
        this.routes = r || [];
        this.settings = s;
      })
      .catch((e) => toast('error', e.message));
  },
  methods: {
    saveRoutes(): void {
      this.saving = true;
      api.putRoutes(this.routes).catch((e) => toast('error', e.message)).finally(() => { this.saving = false; });
    },
    addRoute(): void {
      this.routes.push({ alias: '', provider_id: this.providers[0]?.id || '', upstream_model: '', enabled: true });
    },
    removeRoute(i: number): void {
      this.routes.splice(i, 1);
    },
    saveSettings(patch: (s: Settings) => void): void {
      if (!this.settings) return;
      patch(this.settings);
      this.saving = true;
      api.putSettings(this.settings).catch((e) => toast('error', e.message)).finally(() => { this.saving = false; });
    },
    move(list: Target[], i: number, dir: -1 | 1): void {
      const j = i + dir;
      if (j < 0 || j >= list.length) return;
      [list[i], list[j]] = [list[j], list[i]];
    },
    addLwTarget(): void {
      if (!this.settings) return;
      this.settings.lightweight.targets.push({ provider_id: this.providers[0]?.id || '', model: '' });
    },
    addFoTarget(): void {
      if (!this.settings) return;
      this.settings.failover.targets.push({ provider_id: this.providers[0]?.id || '', model: '' });
    },
  },
});
</script>

<template>
  <div>
    <div class="mb-5">
      <h1 class="text-xl font-bold">路由</h1>
    </div>

    <div class="mb-4 flex gap-1 rounded-xl border border-slate-200 bg-white p-1 dark:border-slate-700 dark:bg-slate-900">
      <button
        v-for="t in tabs" :key="t.v"
        class="flex items-center gap-1.5 rounded-lg px-3.5 py-1.5 text-sm font-medium transition"
        :class="tab === t.v ? 'bg-sky-600 text-white' : 'text-slate-500 hover:text-slate-800 dark:hover:text-slate-200'"
        @click="tab = t.v"
      >
        <component :is="t.icon" class="h-4 w-4" /> {{ t.label }}
      </button>
    </div>

    <!-- 常规路由 -->
    <div v-if="tab === 'routes'" class="card p-4">
      <div class="mb-3 flex items-center justify-between">
        <div>
          <h2 class="text-sm font-semibold">模型路由表</h2>
          <p class="text-xs text-slate-400">客户端模型别名 → 供应商 + 上游真实模型名。Claude Code 中 /model 输入别名即完成切换。</p>
        </div>
        <div class="flex gap-2">
          <button class="btn-ghost" @click="addRoute"><IconPlus class="h-4 w-4" /> 添加</button>
          <button class="btn-primary" :disabled="saving" @click="saveRoutes">保存</button>
        </div>
      </div>
      <div class="space-y-2">
        <div v-for="(r, i) in routes" :key="i" class="flex flex-wrap items-center gap-2 rounded-xl bg-slate-50 p-2.5 dark:bg-slate-950/50">
          <input v-model="r.alias" class="input !w-44 font-mono text-xs" placeholder="别名, 如 glm-4.7" />
          <span class="text-slate-400">→</span>
          <Select v-model="r.provider_id" :options="providerOptions" width="w-44" />
          <input v-model="r.upstream_model" class="input !w-56 font-mono text-xs" placeholder="上游模型名, 如 glm-4.7-air" />
          <Toggle v-model="r.enabled" />
          <button class="btn-danger !px-2" @click="removeRoute(i)"><IconTrash class="h-4 w-4" /></button>
        </div>
        <p v-if="!routes.length" class="py-6 text-center text-sm text-slate-400">暂无路由。添加后未命中路由的请求将走「默认供应商」。</p>
      </div>
      <div v-if="settings" class="mt-4 flex items-center justify-between rounded-xl border border-dashed border-slate-300 p-3 dark:border-slate-700">
        <div>
          <div class="text-sm font-medium">默认供应商(兜底)</div>
          <div class="text-xs text-slate-400">路由表未命中时透传原模型名到该供应商</div>
        </div>
        <Select
          width="w-52"
          :model-value="settings.default_provider_id"
          :options="providerOptionsWithNone('(不设置, 未命中即报错)')"
          @change="(v: string) => saveSettings((s) => { s.default_provider_id = v; })"
        />
      </div>
    </div>

    <!-- 轻量通道 -->
    <div v-if="tab === 'lightweight' && settings" class="card p-4">
      <div class="mb-4 flex items-center justify-between">
        <div>
          <h2 class="flex items-center gap-1.5 text-sm font-semibold"><IconZap class="h-4 w-4 text-amber-500" /> 轻量通道(负载均衡)</h2>
          <p class="text-xs text-slate-400">命中触发条件的请求按列表顺序转发: 首目标承接全部流量, 失败/限额时顺延下一目标。</p>
        </div>
        <div class="flex items-center gap-3">
          <Toggle :model-value="settings.lightweight.enabled" label="启用" @update:model-value="(v: boolean) => saveSettings((s) => { s.lightweight.enabled = v; })" />
        </div>
      </div>
      <div class="mb-4">
        <label class="label">触发别名(逗号分隔, 客户端把这些模型名发来即走轻量通道, 如把 CC 的 ANTHROPIC_DEFAULT_HAIKU_MODEL 配成 cr-light)</label>
        <input
          class="input font-mono text-xs"
          :value="(settings.lightweight.aliases || []).join(', ')"
          placeholder="cr-light, mini"
          @change="saveSettings((s) => { s.lightweight.aliases = ($event.target as HTMLInputElement).value.split(/[,，]/).map((x) => x.trim()).filter(Boolean); })"
        />
      </div>
      <div class="mb-4 rounded-xl bg-slate-50 p-3 dark:bg-slate-950/50">
        <Toggle
          :model-value="settings.lightweight.heuristic.enabled"
          label="启发式触发: 无工具定义的小请求也走轻量通道"
          @update:model-value="(v: boolean) => saveSettings((s) => { s.lightweight.heuristic.enabled = v; })"
        />
        <div class="mt-3 flex items-center gap-4">
          <label class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
            请求体上限(KB)
            <input
              class="input !w-24 text-xs"
              type="number"
              :value="Math.round(settings.lightweight.heuristic.max_body_bytes / 1024)"
              @change="saveSettings((s) => { s.lightweight.heuristic.max_body_bytes = Number(($event.target as HTMLInputElement).value) * 1024; })"
            />
          </label>
          <Toggle
            :model-value="settings.lightweight.heuristic.require_no_tools"
            label="要求无工具定义"
            @update:model-value="(v: boolean) => saveSettings((s) => { s.lightweight.heuristic.require_no_tools = v; })"
          />
        </div>
      </div>
      <div>
        <div class="mb-2 flex items-center justify-between">
          <span class="text-xs font-medium text-slate-500 dark:text-slate-400">目标列表(顺序即优先级)</span>
          <button class="btn-ghost !py-1" @click="addLwTarget"><IconPlus class="h-3.5 w-3.5" /> 添加目标</button>
        </div>
        <div class="space-y-2">
          <div v-for="(t, i) in settings.lightweight.targets" :key="i" class="flex items-center gap-2">
            <span class="w-6 text-center text-xs font-bold text-sky-600">{{ i + 1 }}</span>
            <Select v-model="t.provider_id" :options="providerOptions" width="w-48" />
            <input v-model="t.model" class="input !w-56 font-mono text-xs" placeholder="上游模型名" />
            <button class="btn-ghost !px-1.5" :disabled="i === 0" @click="move(settings!.lightweight.targets, i, -1)"><IconUp class="h-3.5 w-3.5" /></button>
            <button class="btn-ghost !px-1.5" :disabled="i === settings!.lightweight.targets.length - 1" @click="move(settings!.lightweight.targets, i, 1)"><IconDown class="h-3.5 w-3.5" /></button>
            <button class="btn-danger !px-2" @click="saveSettings((s) => { s.lightweight.targets.splice(i, 1); })"><IconTrash class="h-4 w-4" /></button>
          </div>
        </div>
        <p class="mt-2 text-[11px] text-slate-400">上下移动调整优先级; 保存即时生效。</p>
      </div>
    </div>

    <!-- 子agent -->
    <div v-if="tab === 'subagent' && settings" class="card p-4">
      <div class="mb-4 flex items-center justify-between">
        <div>
          <h2 class="flex items-center gap-1.5 text-sm font-semibold"><IconBot class="h-4 w-4 text-violet-500" /> 子agent 兜底通道</h2>
          <p class="text-xs text-slate-400">system 提示词不含主标记词的请求视为子 agent(显式别名路由优先级更高)。</p>
        </div>
        <Toggle :model-value="settings.subagent.enabled" label="启用" @update:model-value="(v: boolean) => saveSettings((s) => { s.subagent.enabled = v; })" />
      </div>
      <div class="grid gap-3 md:grid-cols-2">
        <div>
          <label class="label">主 agent 标记词(system 含此词 = 主对话)</label>
          <input
            class="input font-mono text-xs"
            :value="settings.subagent.marker"
            @change="saveSettings((s) => { s.subagent.marker = ($event.target as HTMLInputElement).value; })"
          />
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label">转发目标供应商</label>
            <Select
              :model-value="settings.subagent.target.provider_id"
              :options="providerOptionsWithNone('(不转发)')"
              @change="(v: string) => saveSettings((s) => { s.subagent.target.provider_id = v; })"
            />
          </div>
          <div>
            <label class="label">指定模型</label>
            <input
              class="input font-mono text-xs"
              :value="settings.subagent.target.model"
              placeholder="上游模型名"
              @change="saveSettings((s) => { s.subagent.target.model = ($event.target as HTMLInputElement).value; })"
            />
          </div>
        </div>
      </div>
      <p class="mt-3 rounded-xl bg-violet-50 p-3 text-xs leading-relaxed text-violet-700 dark:bg-violet-950/40 dark:text-violet-300">
        提示: 也可以在各 agent 定义(含内置 agent 同名覆盖)的 frontmatter 中配置 <code class="font-mono">model: 某路由别名</code>, 走常规路由表, 100% 精准。此页的启发式用于兜底捕获未显式配置的子 agent 请求。误判可通过「请求日志」的通道列核对。
      </p>
    </div>

    <!-- 限额切换 -->
    <div v-if="tab === 'failover' && settings" class="card p-4">
      <div class="mb-4 flex items-center justify-between">
        <div>
          <h2 class="flex items-center gap-1.5 text-sm font-semibold"><IconShield class="h-4 w-4 text-rose-500" /> 限额自动切换</h2>
          <p class="text-xs text-slate-400">主目标返回 429/402/403(可选 5xx)时, 在响应送达客户端之前无感切换下一目标。</p>
        </div>
        <Toggle :model-value="settings.failover.enabled" label="启用" @update:model-value="(v: boolean) => saveSettings((s) => { s.failover.enabled = v; })" />
      </div>
      <div class="mb-4 flex flex-wrap items-center gap-5">
        <label class="flex items-center gap-2 text-sm">
          <input type="radio" value="manual" :checked="settings.failover.mode === 'manual'" @change="saveSettings((s) => { s.failover.mode = 'manual'; })" />
          指定切换列表
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input type="radio" value="same_name" :checked="settings.failover.mode === 'same_name'" @change="saveSettings((s) => { s.failover.mode = 'same_name'; })" />
          同名模型自动切换(按供应商优先级)
        </label>
        <label class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
          <Toggle :model-value="settings.failover.on_5xx" label="5xx 也切换" @update:model-value="(v: boolean) => saveSettings((s) => { s.failover.on_5xx = v; })" />
        </label>
        <label class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
          最大尝试次数
          <input
            class="input !w-16 text-xs"
            type="number" min="1" max="5"
            :value="settings.failover.max_attempts"
            @change="saveSettings((s) => { s.failover.max_attempts = Number(($event.target as HTMLInputElement).value); })"
          />
        </label>
      </div>
      <div v-if="settings.failover.mode === 'manual'">
        <div class="mb-2 flex items-center justify-between">
          <span class="text-xs font-medium text-slate-500 dark:text-slate-400">切换目标(顺序即优先级; 与主目标重复的项会被忽略)</span>
          <button class="btn-ghost !py-1" @click="addFoTarget"><IconPlus class="h-3.5 w-3.5" /> 添加</button>
        </div>
        <div class="space-y-2">
          <div v-for="(t, i) in settings.failover.targets" :key="i" class="flex items-center gap-2">
            <span class="w-6 text-center text-xs font-bold text-rose-500">{{ i + 1 }}</span>
            <Select v-model="t.provider_id" :options="providerOptions" width="w-48" />
            <input v-model="t.model" class="input !w-56 font-mono text-xs" placeholder="上游模型名" />
            <button class="btn-ghost !px-1.5" :disabled="i === 0" @click="move(settings!.failover.targets, i, -1)"><IconUp class="h-3.5 w-3.5" /></button>
            <button class="btn-ghost !px-1.5" :disabled="i === settings!.failover.targets.length - 1" @click="move(settings!.failover.targets, i, 1)"><IconDown class="h-3.5 w-3.5" /></button>
            <button class="btn-danger !px-2" @click="saveSettings((s) => { s.failover.targets.splice(i, 1); })"><IconTrash class="h-4 w-4" /></button>
          </div>
        </div>
      </div>
      <p v-else class="rounded-xl bg-rose-50 p-3 text-xs leading-relaxed text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">
        同名模式: 主目标限流后, 自动尝试其他已启用供应商的【同名模型】(按供应商优先级排序, 仅限支持当前协议格式的供应商; 已拉取模型列表且不含该模型的供应商会被跳过)。上方手动列表在此模式下不生效。
      </p>
    </div>
  </div>
</template>
