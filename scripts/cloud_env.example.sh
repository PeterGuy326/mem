#!/usr/bin/env bash
# mem — 云端 provider 配置（idealab OpenAI 兼容网关）
#
# 用法：
#   1. cp scripts/cloud_env.example.sh scripts/cloud_env.sh
#   2. 编辑 scripts/cloud_env.sh，填入你的 idealab API key
#   3. source scripts/cloud_env.sh   # 把云端 provider 注入当前 shell
#   4. bash scripts/dev_up.sh        # worker 会自动走云端 embedding + LLM
#
# scripts/cloud_env.sh 已在 .gitignore 里（key 不会被提交）。
# dev_up.sh 的 provider 变量是 ${VAR:-默认ollama}，不 source 这个文件时
# 行为完全不变（仍走本地 ollama）——零破坏，按需切云端。

# ---- idealab OpenAI 兼容网关（已实测：/api/openai/v1/models 返回 200）----
export OPENAI_BASE_URL="https://idealab.alibaba-inc.com/api/openai"

# ---- 你的 idealab API key（替换下面这行；不要提交到 git）----
export OPENAI_API_KEY="<在这里填你的 idealab key>"

# ---- 文本 embedding：用 idealab 提供的向量模型 ----
# 跑 scripts/probe_idealab_models.sh 列出可用的 embedding 模型 ID 再填这里。
# 常见命名形如 openai:text-embedding-v3 / openai:gte-... 等。
export MEM_DEFAULT_EMBEDDING="openai:<你的embedding模型ID>"

# ---- RAG 问答的 chat LLM：idealab chat 模型（mem ask 用）----
export MEM_DEFAULT_LLM="openai:qwen3.5-plus"

# ---- 视觉：图搜图仍走本地 CLIP（512-d，与 schema embeddings_visual 对齐）----
# CLIP 是本地视觉模型、不是 4.7GB 的 LLM，首次约 600MB 权重；如需纯云端可后续替换。
export MEM_DEFAULT_VISUAL_EMBEDDING="clip:ViT-B-32"
