[< 返回设计概览](../DESIGN.md)

---

# 3. 核心概念与架构

![Insight & Edge Data Model](../../diagrams/09-insight-edge-datamodel.jpg)

## 3.1 Insight（记忆节点）

Insight 是 Memory 中的基本记忆单元。每条 insight 代表一个独立的知识片段：

```
┌─────────────────────────────────────────────┐
│ Insight                                     │
├─────────────────────────────────────────────┤
│ id         : UUID                           │
│ content    : "选择 Qdrant 而非 Milvus..."    │
│ category   : decision                       │
│ importance : 5  (1-5)                       │
│ tags       : ["vector-db", "architecture"]  │
│ entities   : ["Qdrant", "Milvus"]           │
│ source     : "user"                         │
│ access_count        : 3                     │
│ effective_importance : 0.85                  │
│ created_at : 2026-02-18T10:00:00Z           │
└─────────────────────────────────────────────┘
```

**类别（Category）** 分为六种，帮助区分记忆的性质：

| 类别 | 含义 | 示例 |
|------|------|------|
| `preference` | 用户偏好 | "偏好使用中文交流" |
| `decision` | 架构/技术决策 | "选择 SQLite 而非 PostgreSQL" |
| `fact` | 客观事实 | "API 限流为 100 req/s" |
| `insight` | 推理结论 | "Beam search 比 full BFS 更适合…" |
| `context` | 项目上下文 | "Phase 3 已完成，118 个测试通过" |
| `general` | 通用 | 不属于以上分类的内容 |

**重要度（Importance）** 从 1 到 5，影响检索排序和生命周期：

- **5**：关键决策，永远不会被自动清理
- **4**：重要事实，免疫自动剪枝
- **3**：标准记忆
- **2**：低优先级
- **1**：临时信息，最先被清理

## 3.2 Edge（关系边）

Edge 连接两个 insight，代表它们之间的关系。每条边包含：

```
┌────────────────────────────────────────────┐
│ Edge                                       │
├────────────────────────────────────────────┤
│ source_id  : UUID  ──→  target_id : UUID   │
│ edge_type  : temporal | semantic |         │
│              causal   | entity             │
│ weight     : 0.0 ~ 1.0                    │
│ metadata   : {"sub_type": "backbone", ...} │
└────────────────────────────────────────────┘
```

四种边类型构成 MAGMA 四图模型的基础，详见[第 4 节：图模型与理论](04-graph-model.md)。

## 3.3 数据库模式

每个命名记忆体拥有独立的 SQLite 文件，位于 `~/.memory/data/<store>/memory.db`，使用 WAL 模式支持并发读取。默认记忆体为 `default`；可创建额外记忆体进行数据隔离（参见[记忆体管理](../USAGE.md#记忆体管理)）。

```sql
-- 记忆节点
insights (
  id, content, category, importance,
  tags, entities, source,
  embedding,                    -- 可选，768 维向量
  access_count, last_accessed_at,
  effective_importance,          -- 衰减后的有效重要度
  created_at, updated_at, deleted_at
)

-- 关系边（复合主键）
edges (
  source_id, target_id, edge_type,  -- PK
  weight, metadata, created_at
)

-- 操作日志（审计追踪）
oplog (
  id, operation, insight_id, detail, created_at
)
```

---

## 3.4 系统架构

Memory 只发布一个可执行文件，但组合了两条刻意隔离的产品路径。Memory 继续
使用根级命令；Agency 只位于 `memory agency ...`。共享可执行文件不会合并
两者的状态或 authority。

```
                         memory
                            |
              +-------------+-------------+
              |                           |
        根级 Memory 命令           memory agency ...
              |                           |
  model / graph / search / store    View / Intent / admission
  embed / import / setup assets     Artifact / peer / attachment
              |                           |
       命名 Memory store            项目 .memory/agency
```

Memory 路径拥有知识存储与检索；Agency 路径为已有 Agent Runtime 提供持久责任
与受准入后果。其规范对象和 package 边界见
[memoryd 协议](../memoryd/protocol.md)。

**项目代码结构：**

```
memory/
├── main.go                    # 进程入口
├── cmd/
│   ├── root.go                # 组合单一产品命令
│   ├── memory/                # 根级 Memory 命令与 Memory flags
│   └── agency/                # `memory agency` 用户命令
├── internal/
│   ├── model/                 # Memory Insight 与 Edge 值
│   ├── graph/                 # 四图建边与遍历
│   ├── search/                # Recall、Intent 检测与去重
│   ├── embed/                 # 可选 Ollama embedding
│   ├── importdraft/           # Memory 草稿校验与导入
│   ├── store/                 # Memory SQLite 持久化
│   ├── setup/                 # Memory runtime 集成与内嵌资产
│   ├── agency/                # 不可变 Agency 协议值与投影
│   ├── authority/             # View 封装、Intent 准入、唯一持久事实写入
│   ├── artifact/              # 内容寻址的不可变证据
│   ├── peerlink/              # 可替换的认证 peer transport
│   ├── daemon/                # 本地 authority 进程组合与生命周期
│   ├── agencyclient/          # Runtime 面向的 terminal 与 replay journal
│   └── attach/                # Agency Hook、guide 与工具投影
├── test/memoryd/              # Agency 边界与场景测试
├── testdata/memoryd/          # 仅含数据的 Agency fixture
└── scripts/e2e_test.sh        # Memory CLI 端到端测试
```

## 3.5 Memory 数据目录布局

下面的用户级目录只属于 Memory。Agency 的独立项目状态位于
`<project>/.memory/agency/`。

```
~/.memory/
├── active                        # 当前默认记忆体名（纯文本）
├── prompt/                       # 所有记忆体共享
│   ├── guide.md                  # 行为引导（recall/remember 规则）
│   └── skill.md                  # 技能定义（命令参考）
└── data/                         # 每个记忆体拥有独立目录
    ├── default/
    │   └── memory.db             # SQLite 数据库（WAL 模式）
    ├── work/
    │   └── memory.db
    └── <name>/
        └── memory.db
```

**隔离边界**：每个记忆体包含独立的 `memory.db` — 洞察、边、操作日志完全隔离。Prompt 文件（`guide.md`、`skill.md`）共享 — 行为规则是通用的，记忆数据是私有的。

## 3.6 Memory 记忆体隔离

Memory 支持命名记忆体（store），为不同 agent、项目或场景提供轻量数据隔离。

**为什么用命名记忆体而非只靠 `--data-dir`？**

`--data-dir` 覆盖整个基础目录 — 需要调用者管理完整路径，语义不清晰。命名记忆体提供语义明确的标识（`MEMORY_STORE=work` 对比 `--data-dir ~/.memory-work`），并且天然适配环境变量 — 这是并发进程间隔离的标准机制。

**解析优先级**（从高到低）：

```
--store 标志  >  MEMORY_STORE 环境变量  >  ~/.memory/active 文件  >  "default"
```

分层设计服务于不同场景：

| 机制 | 场景 |
|------|------|
| `--store` 标志 | 一次性 CLI 覆盖、脚本 |
| `MEMORY_STORE` 环境变量 | 按进程隔离 — 不同 agent 使用不同记忆体 |
| `active` 文件 | 持久化用户偏好 — `memory store set work` |
| `"default"` | 零配置 — 开箱即用 |

**自动迁移**：当 `data/` 目录不存在但旧版 `~/.memory/memory.db` 存在时，memory 自动将其移动到 `data/default/memory.db`。老用户升级无感知。

**设计原则 — 轻量且有界**：记忆体隔离解决的是必要的数据分离需求，不会膨胀为多租户系统。没有访问控制、没有跨 store 查询、除名称外没有 store 元数据。保持功能有界 — Memory 是记忆守护进程，不是知识库平台。
