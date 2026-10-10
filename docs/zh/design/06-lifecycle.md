[< 返回设计概览](../DESIGN.md)

# 6. 生命周期与嵌入

Memory 不是只增不减的系统。有效的记忆管理需要让重要的记忆持久保留，让过时的记忆自然衰减。

![Lifecycle & Retention](../../diagrams/06-lifecycle-retention.jpg)

## 6.1 有效重要度（Effective Importance）

EI 综合基础重要度、访问频率、时间衰减和图连接度：

```
EI = base_weight(importance) × access_factor × decay_factor × edge_factor

base_weight:   imp 5 → 1.0,  4 → 0.8,  3 → 0.5,  2 → 0.3,  1 → 0.15
access_factor: max(1.0, log(1 + access_count))
decay_factor:  0.5 ^ (days_since_access / 30)     // 半衰期 30 天
edge_factor:   1.0 + 0.1 × min(edge_count, 5)     // 最多 +0.5
```

含义：
- **高重要度** → 基础分高
- **频繁访问** → 对数增长的加分
- **长期未访问** → 指数衰减（30 天减半）
- **图连接丰富** → 说明与其他知识相关，加分

## 6.2 免疫规则

以下 insight 不会被自动清理：
- `importance ≥ 4`（高价值记忆）
- `access_count ≥ 3`（频繁被检索）

## 6.3 自动剪枝（Auto-Prune）

当活跃 insight 总数超过 **1000** 时触发：

1. 计算所有 insight 的 EI
2. 排除免疫的 insight
3. 按 EI 升序取最低的（每批最多 10 条）
4. 软删除（设置 `deleted_at`）
5. 级联删除相关边

## 6.4 GC 命令

手动的生命周期管理工具：

```bash
# 查看低保留度候选
memory gc --threshold 0.5

# 保留某条 insight（增加 access_count +3）
memory gc --keep <id>
```

---

## 6.5 嵌入向量支持

嵌入向量是可选的增强功能。没有 embedding 时，Memory 完全基于关键词和图结构工作；有 embedding 时，语义检索能力大幅增强。

### Ollama 集成

通过本地 Ollama 服务（无需外部 API）：

```
Memory ──HTTP──→ Ollama (localhost:11434)
                  └── nomic-embed-text
                      768 维向量
```

- **可用性检测**：2 秒超时，避免阻塞
- **优雅降级**：Ollama 不可用时自动切换到 token 重叠
- **零新依赖**：纯 stdlib `net/http`

### 向量存储

向量序列化为 little-endian float64 的 BLOB 存储在 `insights.embedding` 列中（768 × 8 = 6144 bytes/insight）。

### 使用场景

| 场景 | 无 embedding | 有 embedding |
|------|-------------|-------------|
| remember → 语义边 | token 重叠 > 0.10 | cos ≥ 0.80 自动链接 |
| recall → 锚点 | 关键词 + 时间 | 关键词 + 向量 + 时间 |
| recall → 遍历 | 纯结构分 | 结构 + 语义相似度 |
| recall → 重排 | KW + Entity + Graph | KW + Entity + Similarity + Graph |

### 管理命令

```bash
ollama pull nomic-embed-text    # 安装模型
memory embed --status           # 查看覆盖率
memory embed --all              # 批量生成所有 insight 的 embedding
memory embed <id>               # 生成单条
```
