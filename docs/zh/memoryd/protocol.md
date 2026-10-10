# memoryd 协议

> **稳定性：Preview。** 本文是 Agency 当前实现遵循的 authority contract，
> 不是长期兼容承诺。在 Agency 明确宣布稳定里程碑前，协议字段、持久格式、Peer
> 交互和 Runtime 投影仍可能演进。

本文定义 Memory Agency 背后的最小产品契约。它不是纯 Event Sourcing
要求，也不是内置协作模式清单。

## 目标

`memoryd` 向短暂的 Agent turn 投影一个有界的本地责任世界，并准入
Agent 提议的后果。它不替 Agent 规划任务、调度工具或同步远端节点状态。

协议只有一个本地循环：

```text
本地 authority -> View -> Intent -> admission -> Event + effect + Receipt
       ^                                                        |
       `----------------------- 下一个 View --------------------'
```

模型拥有开放的语义选择；本地 authority 拥有身份、可用 handle、边界、
路由、fence、持久化以及后果是否被接受。

这个循环是逻辑循环，不表示一个模型 turn 中可以连续执行多个动作。accepted
Receipt 会结束当前受治理的 Host opportunity；下一次合格边界再读取新 View。
有界输入诊断属于控制结果，不是 Receipt。

## 核心对象

| 对象 | 含义 | 所有者 |
|---|---|---|
| **View** | 本地世界和当前可用后果的有界投影 | 本地 authority 派生 |
| **Intent** | Agent 从一个精确 View 中选择的有界语义提议 | Agent |
| **Event** | 本地 admission 接受 Intent 或认证后的远端 candidate 后产生的不可变语义行动 | 本地 authority |
| **Receipt** | 一个精确 operation 的持久 accepted/rejected 结果；重放返回既有结果而不产生第二次后果 | 本地 authority |
| **Handling** | 某个 Principal 仍需考虑的本地持久责任 | 仅本地 authority |
| **Reference** | 带 CAS head、没有 owner、claim 或完成状态的本地持久谱系；active head 指向 Artifact，retracted head 作为 tombstone 保留 | 仅本地 authority |
| **Artifact** | 通过 digest 寻址和验证的不可变内容；Event 只携带引用 | Artifact store 与本地 authority catalog |

`Handling`、`Reference` 和 `Artifact` 可以被投影进 View，但 View 不是
它们的 canonical storage。更换 `view.md` 或 JSON 的渲染方式不能改变
admission 结果。

## Event 边界

只有当一个已接受行动需要跨 turn、进程、Runtime、Principal 或节点继续
存在，或者其因果关系与结果必须在原 Agent 消失后仍可恢复时，才应形成
Event。

查询、View 渲染、prompt 拼装、索引、缓存、transport ACK、claim 维护和
模型私有推理都不是 Event。

Event 明确分离三类数据：

```text
machine     身份、接受时间、封闭 consequence、解析后的 target
semantic    开放但有界的 kind 与自然语言 payload
evidence    Artifact digest、causation 与 correlation
```

语义 kind 开放，持久 consequence 封闭。自然语言可以解释或建议行动，但
不能生成身份、authority、路由、完成或持久化结果。

## 本地责任，而不是 Agent 状态

`memoryd` 记录 Handling 是否 open/terminal，以及某个 claim 当前是否有效；
它不保存 `agent.status = reviewing` 或模型所在的 workflow step。claim 只是
短暂占用，过期只释放占用，不会声明责任已经完成。

Agent 从 View 中看到当前 Handling 和相关 note，再自由选择下一个允许的
Intent。新的协作模式应由语义 Event kind 和 guide 表达，而不是在 Core 中
增加 Agent 状态机。

## 跨节点 handoff

跨节点 handoff 是两个本地责任循环相接，不是把一个 Handling 原子搬到远端：

```text
Node A                                         Node B

View A
  -> Intent(request)
  -> 本地 admission
     + Event(request)
     + Handling A：等待并评估 B
                      |
                      | 有界投递
                      v
                 认证后的 candidate
                   -> 本地 admission
                   -> Handling B：考虑请求
                   -> View B
                   -> Intent(result / decline / unresolved)
                   -> Event + Artifact 引用
                      |
                      v
Node A 收到 candidate
  -> 本地重新准入
  -> View A'
  -> Intent(adopt / rework / decline)
  -> 本地 Receipt，并结算 Handling A
```

两个节点不共享 canonical Task 或 Handling。因此：

1. transport delivery 不等于远端 admission；
2. 远端 admission 不等于业务完成；
3. 远端 result 不等于本地采纳；
4. 远端 Event 只有经过接收端本地 admission 才能成为本地事实；
5. 网络可以至少一次投递，但 operation identity 与 digest 保证语义后果幂等。

## Package 权责

```text
internal/agency           不可变协议值与 canonical projection
internal/agency/authority sealed View、Intent binding、admission、Handling/Reference state
internal/agency/artifact  不可变内容字节与 digest 验证
internal/agency/peerlink  可替换的认证传输
internal/agency/client    Runtime 面向的本地 terminal 与 replay journal
internal/agency/attach    Host Hook、guide 和工具投影
internal/daemon           进程组合与生命周期
```

`internal/agency/authority` 是唯一持久事实 writer。Runtime adapter 和 transport
只能提供 candidate 或 observation。`internal/agency` 校验不可变值；解析
View handle、选择持久 consequence 等 policy 必须属于 `internal/agency/authority`。

## 能力边界

Memory、teamwork、review、negotiation 和 self-evolution 都是建立在此协议
之上的能力。它们可以增加有界 View 投影、语义 Event kind、Agent guide 和
不会创造第二 authority 的确定性 provider。

它们不能让 Core 决定什么知识有价值、哪个 Agent 应赢得争论、Runtime 应该
怎样规划，或模型必须采用哪种协作模式。任何新增 canonical consequence 的
能力都必须作为 authority 修改接受评审，而不能作为普通数据加载。
