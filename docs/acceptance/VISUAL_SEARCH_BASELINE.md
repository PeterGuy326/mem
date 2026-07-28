# 自然语言搜图基线

这份记录区分“检索链路存在”和“某种语言的召回质量已达标”。自然语言搜图是
Agent 网盘的核心能力，但任何语言都必须在真实模型和固定图片集上通过，不能用
mock、caption 或接口返回 200 代替质量证据。

## 当前固定集

`scripts/demo_data/images/`：

- `golden_retriever_grass.jpg`
- `cat.jpg`
- `river_landscape.jpg`

图片必须由 `embed_image` 直接读取原始字节生成 512 维向量；查询必须由同一
checkpoint 的文本塔编码。

## 2026-07-28 基线结果

生产默认 `clip:ViT-B-32:openai` 的真实 checkpoint 已运行：

| 查询 | 期望首位 | 实际结果 | 判断 |
|---|---|---|---|
| `a golden retriever standing on green grass` | 金毛 | 金毛首位（0.279846） | 通过 |
| 英文河流描述 | 河流 | 河流首位 | 通过 |
| `草地上的金毛` | 金毛 | 猫首位 | **未通过** |
| 中文河流描述 | 河流 | 河流首位 | 通过，但不足以代表中文整体通过 |

因此当前可以说“真实 CLIP 以文搜图链路已跑通，英文小型基线通过”，不能说
“默认模型的中文自然语言搜图已达标”。

## 自动化门槛

- `worker/tests/test_image_visual_regression.py`：默认运行的 hermetic 回归，覆盖
  原始图片字节、512 维守卫、provider 错误可观察和无伪造降级向量。
- `worker/tests/test_multilingual_visual_acceptance.py`：显式 opt-in 的真实模型
  评测，同时要求英文金毛、中文金毛和中文河流查询首位正确。

候选模型默认设为
`clip:xlm-roberta-base-ViT-B-32:laion5b_s13b_b90k`。本次因大权重下载未在
有界验证窗口内完成，所以它仍是“待验证候选”，不是已经通过或可以直接替换的
生产默认。

切换视觉 checkpoint 前还必须实现 versioned visual index generation，并对旧图片
全量重建；不同 checkpoint 产生的向量不得混入同一检索空间。
