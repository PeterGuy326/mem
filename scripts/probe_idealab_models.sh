#!/usr/bin/env bash
# 列出 idealab OpenAI 兼容网关在你的 key 下可调的模型，
# 并高亮 embedding 类（mem 文本向量要用的）。
#
# 用法：
#   OPENAI_API_KEY=你的key bash scripts/probe_idealab_models.sh
# 或先 source scripts/cloud_env.sh 再跑。
#
# 只读操作：GET /v1/models，不发送任何用户数据。key 只在你本机使用。
set -euo pipefail

BASE="${OPENAI_BASE_URL:-https://idealab.alibaba-inc.com/api/openai}"
KEY="${OPENAI_API_KEY:-}"

if [[ -z "$KEY" || "$KEY" == "<在这里填你的 idealab key>" ]]; then
  echo "ERR  请先设置 OPENAI_API_KEY（export 或 source scripts/cloud_env.sh）" >&2
  exit 1
fi

echo "GET ${BASE}/v1/models"
resp="$(curl -sS "${BASE}/v1/models" -H "Authorization: Bearer ${KEY}")"

ids="$(echo "$resp" | jq -r '.data[].id' 2>/dev/null || true)"
if [[ -z "$ids" ]]; then
  echo "—— 原始响应（无法解析 .data[].id）——"
  echo "$resp" | head -c 2000
  exit 0
fi

total="$(echo "$ids" | wc -l | tr -d ' ')"
echo "可调模型总数：${total}"
echo
echo "=== 疑似 embedding / 向量模型（mem 文本向量候选）==="
echo "$ids" | grep -iE 'embed|向量|gte|bge|m3e|text-embedding|vector|conan|multimodal-embedding' || echo "（未匹配到 embedding 命名——把下面全量列表发我，我来判断哪个是向量模型）"
echo
echo "=== 全量模型 ID ==="
echo "$ids"
