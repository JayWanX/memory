[< 返回设计概览](../DESIGN.md)

# 8. 设计决策与未来方向

## 8.1 设计决策与权衡

### 为什么选择 LLM-Supervised 而非嵌入式 LLM？

| 维度 | LLM-Embedded（Mem0 等） | LLM-Supervised（Memory） |
|------|----------------------|--------------------------|
| LLM 能力 | gpt-4o-mini（受限） | 宿主 LLM（Opus 级别） |
| API 成本 | 每次操作都有调用 | 零 |
| 网络依赖 | 必需 | 不需要 |
| 可替换性 | API 绑定 | 任何 LLM CLI |

### 为什么选择 SQLite WAL 而非嵌入式图数据库？

- **单文件部署**：每个记忆体一个 `.db` 文件 — 易于管理和备份
- **ACID 事务**：remember 管线的原子性保证
- **WAL 并发**：支持 hook 读取和 CLI 写入同时进行
- **零外部依赖**：不需要 Redis/Neo4j/Qdrant
- **记忆体隔离**：命名记忆体（`~/.memory/data/<name>/memory.db`）通过 `MEMORY_STORE` 环境变量提供轻量数据隔离

### 为什么用 Beam Search 而非完整 BFS？

- **预算可控**：MaxVisited 参数避免图爆炸
- **意图自适应**：不同意图使用不同的 beam width 和 depth
- **质量保证**：每一层只保留得分最高的候选，类似剪枝

### 为什么 Soft Delete？

- 保留审计追踪
- 支持 "undo"（恢复误删）
- 简化级联清理
- 查询一致性（`WHERE deleted_at IS NULL`）

### 与 MAGMA 论文的主要偏差

| 方面 | MAGMA 论文 | Memory 实现 |
|------|-----------|------------|
| 实体提取 | LLM 驱动的完整管线 | 正则 + 词典 + LLM 补充 |
| 因果推理 | 嵌入式 prompt chain | 自动候选 + LLM 评审 |
| 节点类型 | EVENT, EPISODE, SESSION, NARRATIVE | 仅 Insight（扁平） |
| 存储 | NetworkX（内存） | SQLite（持久化） |
| 嵌入 | FAISS + OpenAI | Ollama（本地，可选） |
| 部署 | Python 库 | 单一 Go 二进制 |

Memory 保留了 MAGMA 的**架构骨架**（四图分离、intent-adaptive retrieval、multi-signal fusion），同时用工业化的简化手段替换了学术实现细节。这种两层方法——多数场景的确定性自动化 + 复杂少数的 LLM judgment——正是 [RLM 论文](02-philosophy.md#25-理论基础)所验证的模式：regex filtering 加 LLM semantic verification 的组合，持续优于任一单独使用。核心取舍是：**用 regex/heuristics 处理 80% 的自动化场景，将需要深度理解的 20% 交给宿主 LLM。**

---

## 8.2 未来方向

[两层架构](02-philosophy.md#23-记忆网关协议而非数据库)已经实现了 Agent 侧的可插拔——今天任何 LLM CLI 都可以通过协议面与 Memory 交互。剩下的工作在另一侧。

### 存储侧的可插拔

存储引擎目前紧密构建在 SQLite 之上——图遍历、EI 衰减、原子事务都依赖 SQLite 特性（WAL、单文件部署、进程内访问）。这是当前零依赖单二进制分发目标的正确选择，但意味着存储后端还不可替换。

抽象存储接口——使协议层可以运行在 PostgreSQL、专用图数据库或远程服务之上——是下一个架构里程碑。协议天然适配不同后端，表达力各异：

```
              remember        link                recall
              ─────────       ────────────────     ──────────────────
Neo4j         CREATE node     CREATE edge          MATCH + traverse
TigerGraph    add vertex      add edge             GSQL query
Milvus        upsert vec      metadata ref         ANN search
PostgreSQL    INSERT row      INSERT FK/join        SELECT + JOIN
Redis         SET key         _(退化)_              GET key
SQLite        INSERT row      INSERT edge table     multi-signal query
```

图数据库实现协议最自然——三个原语直接映射到原生操作。关系型数据库的 `link` 需要翻译层（外键在 schema 设计时就固定了，而非动态创建）。KV 存储只能实现 `remember` + `recall`（`link` 退化）。这一谱系反映了[结构洞察](02-philosophy.md#25-理论基础)——其他存储类型是图的退化形式。

核心挑战在于定义合适的抽象边界：太高则丧失存储引擎的图感知优化；太低则每个后端都必须重新实现 Beam Search 和 RRF 融合。

### 迈向记忆网关

当上下两端都解耦后，Memory 成为真正的记忆网关——上层接任何 LLM，下层接任何存储后端，协议层作为两者之间的稳定契约：

```
         单体系统                       协议网关
         （产品思路）                    （平台思路）

Mem0  ──┐                         ┌── Neo4j adapter
CC Mem──┤ 每个项目各自             │── TigerGraph adapter
Viking──┤ 重造存储层               │── Milvus adapter
MemGPT──┘                         │── SQLite adapter（当前）
                                   └── PostgreSQL adapter

                                   ↑
                              memory 的位置：
                              不是又一个数据库，
                              而是 LLM ↔ DB 协议网关
```

这重新定义了 Memory 的竞争维度：

- **不跟 Neo4j 比存储引擎**——DB 问题归 DB
- **不跟 Mem0 比产品功能**——Mem0 是绑定自身存储实现的产品
- **与 MCP 类比**——MCP 将 LLM 接入了工具生态，这个协议将 LLM 接入数据库生态，尤其是图数据库，三个原语在其上达到最完整的表达

支撑这一目标的三个性质：

- **Agent 侧优化**（何时召回、记什么、如何评估候选）和**存储侧优化**（索引、查询规划、图算法）独立演进
- 协议面——`remember`、`link`、`recall`、生命周期钩子、带信号透明度的结构化 JSON——作为两侧共同编程的稳定接口
- `remember / link / recall` 的[通用代数](02-philosophy.md#25-理论基础)确保这个接口不是任意设计，而是 agent 记忆系统最小完备原语集的反映
