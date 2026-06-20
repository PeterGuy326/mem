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

# ---- 文本 embedding：idealab 最新文本向量模型 text-embedding-v4（1024-d）----
# 跑 scripts/probe_idealab_models.sh 可列出网关上其它可用的 embedding 模型。
export MEM_DEFAULT_EMBEDDING="openai:text-embedding-v4"

# ---- RAG 问答的 chat LLM：qwen3.7-max（idealab 最新旗舰，thinking 模型，
# 质量最高、推理稍慢）。想要更快可改 openai:qwen3-max。----
export MEM_DEFAULT_LLM="openai:qwen3.7-max"

# ---- 以文搜图：云端视觉大模型 qwen-vl-max 给图片生成描述，再由
# text-embedding-v4 编码 -> 中文自然语言可经文本路检索图片。idealab 无 CLIP 式
# 图文同空间多模态 embedding，故云端图片理解走「VLM 描述 + 文本向量」路线。----
export MEM_DEFAULT_VLM="openai:qwen-vl-max"

# ---- 视觉向量列仍用本地 CLIP（512-d，与 schema embeddings_visual 对齐）；
# --route visual 靠它，--route auto/text 靠上面的云端 caption 向量。----
export MEM_DEFAULT_VISUAL_EMBEDDING="clip:ViT-B-32"
