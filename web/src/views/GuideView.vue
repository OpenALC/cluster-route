<script lang="ts">
import { defineComponent } from 'vue';
import { api } from '../api';
import IconCopy from '~icons/lucide/copy';
import IconCheck from '~icons/lucide/check';

export default defineComponent({
  name: 'GuideView',
  components: { IconCopy, IconCheck },
  data() {
    return {
      baseUrl: 'http://127.0.0.1:3721',
      routerKey: '',
      copied: '',
      port: 3721,
    };
  },
  mounted() {
    api.connection()
      .then((c) => {
        this.baseUrl = c.base_url;
        this.routerKey = c.router_key;
        this.port = c.port;
      })
      .catch(() => undefined);
  },
  methods: {
    copy(text: string, tag: string): void {
      navigator.clipboard.writeText(text).then(() => {
        this.copied = tag;
        setTimeout(() => { this.copied = ''; }, 1500);
      });
    },
  },
});
</script>

<template>
  <div>
    <h1 class="mb-5 text-xl font-bold">指引</h1>
    <p class="mb-5 -mt-3 text-xs text-slate-400">cc-switch 只需配置一次, 之后所有切换都在「路由」与 Claude Code 的 /model 中完成。</p>

    <div class="space-y-4">
      <!-- 1 -->
      <div class="card p-5">
        <h2 class="mb-3 flex items-center gap-2 text-sm font-semibold"><span class="flex h-6 w-6 items-center justify-center rounded-lg bg-sky-600 text-xs font-bold text-white">1</span> 在 cc-switch 中添加一个供应商条目</h2>
        <p class="mb-3 text-xs text-slate-400">把 cluster-router 当作唯一供应商: 以后增删真实供应商都不需要再动 cc-switch。</p>
        <div class="space-y-2">
          <div class="flex items-center gap-2 rounded-xl bg-slate-50 p-3 dark:bg-slate-950/60">
            <div class="min-w-0 flex-1">
              <div class="text-[11px] text-slate-400">BASE_URL</div>
              <div class="truncate font-mono text-sm">{{ baseUrl }}</div>
            </div>
            <button class="btn-ghost !px-2" @click="copy(baseUrl, 'url')"><IconCheck v-if="copied === 'url'" class="h-4 w-4 text-emerald-500" /><IconCopy v-else class="h-4 w-4" /></button>
          </div>
          <div class="flex items-center gap-2 rounded-xl bg-slate-50 p-3 dark:bg-slate-950/60">
            <div class="min-w-0 flex-1">
              <div class="text-[11px] text-slate-400">API Key(在「设置」页可轮换)</div>
              <div class="truncate font-mono text-sm">{{ routerKey || '(读取中)' }}</div>
            </div>
            <button class="btn-ghost !px-2" @click="copy(routerKey, 'key')"><IconCheck v-if="copied === 'key'" class="h-4 w-4 text-emerald-500" /><IconCopy v-else class="h-4 w-4" /></button>
          </div>
        </div>
        <p class="mt-3 text-[11px] text-slate-400">cc-switch 会把它写入 ~/.claude/settings.json 的 ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN。</p>
      </div>

      <!-- 2 -->
      <div class="card p-5">
        <h2 class="mb-3 flex items-center gap-2 text-sm font-semibold"><span class="flex h-6 w-6 items-center justify-center rounded-lg bg-sky-600 text-xs font-bold text-white">2</span> 在「供应商」页添加真实上游</h2>
        <ul class="list-disc space-y-1.5 pl-5 text-xs leading-relaxed text-slate-500 dark:text-slate-400">
          <li>Anthropic 格式 BASE_URL: 填供应商的 Anthropic 兼容端点(如 <code class="font-mono">https://api.anthropic.com</code>、中转站 <code class="font-mono">https://xx.com</code>)。</li>
          <li>OpenAI 格式 BASE_URL: 供 OpenAI 协议客户端(如 Codex CLI)使用, 一般填 <code class="font-mono">https://xx.com/v1</code>。</li>
          <li>点「拉取模型」获取可用模型列表, 便于下一步建路由。</li>
        </ul>
      </div>

      <!-- 3 -->
      <div class="card p-5">
        <h2 class="mb-3 flex items-center gap-2 text-sm font-semibold"><span class="flex h-6 w-6 items-center justify-center rounded-lg bg-sky-600 text-xs font-bold text-white">3</span> 在「通道与路由」中建路由表</h2>
        <div class="overflow-x-auto rounded-xl bg-slate-50 p-3 font-mono text-xs dark:bg-slate-950/60">
          <div class="text-slate-400"># 路由表示例(别名 → 供应商.上游模型)</div>
          <div>glm-4.7 <span class="text-slate-400">→</span> 智谱.glm-4.7</div>
          <div>kimi-k3 <span class="text-slate-400">→</span> Kimi.k3[1M]</div>
          <div>deepseek-v3 <span class="text-slate-400">→</span> DeepSeek.deepseek-chat</div>
        </div>
      </div>

      <!-- 4 -->
      <div class="card p-5">
        <h2 class="mb-3 flex items-center gap-2 text-sm font-semibold"><span class="flex h-6 w-6 items-center justify-center rounded-lg bg-sky-600 text-xs font-bold text-white">4</span> 在 Claude Code 中切换模型</h2>
        <ul class="list-disc space-y-1.5 pl-5 text-xs leading-relaxed text-slate-500 dark:text-slate-400">
          <li>会话内输入 <code class="font-mono">/model glm-4.7</code> 即可切换到对应供应商的模型。</li>
          <li>持久配置可在 <code class="font-mono">~/.claude/settings.json</code> 中设置:</li>
        </ul>
        <pre class="mt-2 overflow-x-auto rounded-xl bg-slate-900 p-3 font-mono text-[11px] leading-relaxed text-slate-200">"env": {
  "ANTHROPIC_BASE_URL": "{{ baseUrl }}",
  "ANTHROPIC_AUTH_TOKEN": "{{ routerKey || 'sk-cr-...' }}",
  "ANTHROPIC_MODEL": "glm-4.7",
  "ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-4.7",
  "ANTHROPIC_DEFAULT_HAIKU_MODEL": "cr-light"
}</pre>
        <p class="mt-2 text-[11px] text-slate-400">把 HAIKU(小模型)槽位配成轻量通道触发别名(如 cr-light), CC 的后台轻量任务就会走低成本模型。</p>
      </div>

      <!-- 5 -->
      <div class="card p-5">
        <h2 class="mb-3 flex items-center gap-2 text-sm font-semibold"><span class="flex h-6 w-6 items-center justify-center rounded-lg bg-sky-600 text-xs font-bold text-white">5</span> 限额切换 / 子agent / 提示词注入</h2>
        <ul class="list-disc space-y-1.5 pl-5 text-xs leading-relaxed text-slate-500 dark:text-slate-400">
          <li><b>限额切换</b>: 「通道与路由 → 限额切换」开启后, 主模型限流时自动换到列表中的下一个(或同名模型的其他供应商), 任务不中断。</li>
          <li><b>子agent 指定模型</b>: 在 agent 定义的 frontmatter 配 <code class="font-mono">model: 路由别名</code>(100% 精准); 或开启「子agent」启发式兜底。</li>
          <li><b>提示词注入</b>: 「设置 → 全局提示词注入」配置跨压缩存活的全局守则。</li>
          <li><b>上下文找回</b>: 「会话留档」按项目分组浏览各会话完整上下文, 支持导出 Markdown。</li>
        </ul>
      </div>
    </div>
  </div>
</template>
