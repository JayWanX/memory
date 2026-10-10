# Memory Agency

[English](../AGENCY.md) | **中文**

> **状态：Preview，持续迭代中。** Agency 已有可运行实现和测试覆盖，但当前面向
> macOS/Linux 上的评估与受控使用。在明确宣布稳定里程碑前，其命令表面、Runtime
> 集成、协议、Peer 交互以及磁盘/wire 格式仍可能变化。这个成熟度标签只适用于
> Agency；Memory 拥有独立的状态模型与产品契约。

Memory Agency 为一个项目中的 Agent 提供持久、受约束的协作状态。它不替
Agent 规划或执行任务，而是在 Agent 提议改变状态时决定是否接纳，并留下可
重放、可核验的结果。

Agency 与 Memory 由同一个 `memory` 可执行文件提供。Agency 当前以 Pi 为首个
Runtime 集成，支持 macOS 和 Linux。

## 能力边界

- **Memory** 保存跨会话知识，使用根级命令，例如 `memory remember`、
  `memory recall` 和 `memory setup`。
- **Agency** 保存项目内的责任、证据与有效描述，使用
  `memory agency ...`，状态位于项目的 `.memory/agency/`。
- **Agent Runtime** 仍负责模型、提示词、工具调用、任务执行与凭据。当前由
  Pi 提供这个 Runtime。

三者可以独立使用。`memory setup --target pi` 安装 Memory 集成；
`memory agency setup` 安装 Agency 集成。共用一个二进制不会合并两套状态或
生命周期。

Agency 不是 Agent Runtime、调度器、工作流引擎或操作系统沙箱。`memoryd`
只是同一 `memory` 可执行文件在项目内承担的 daemon／协议角色，不是另一个
需要安装或手动管理的程序。

## 一次性设置

先确保 `memory` 已在 `PATH` 中，然后从项目目录运行：

```sh
memory agency setup --runtime pi --project-root .
```

当前目录就是项目根目录时可以省略 `--project-root .`。设置过程可以安全地
重复执行：它会准备 `.memory/agency/`、确保本地 daemon 可用，并安装项目级
Pi Hook 与指南。Pi 的模型、provider 配置和凭据仍由 Pi 管理，不会进入
Agency 状态。

之后照常使用 Pi 即可。普通任务不需要用户手动启动 daemon，也不需要调用
隐藏的 Agent 操作命令；安装的集成会在合适的 Pi 回合边界连接 Agency。

如果计划配置 peer，请先完成下一节的离线配置，再运行本节的最终 setup。

## 可选的 Peer 协作

单个项目不需要配置 peer。若两个项目中的 Agent 需要协作，应在最终
`memory agency setup` 前、两个节点的 daemon 均未运行时配置。先在两端生成
各自的公开 Peer Card：

```sh
# 节点 A
memory agency peer prepare \
  --listen 0.0.0.0:7447 \
  --advertise node-a.example:7447 \
  --project-root /work/a > node-a.card.json

# 节点 B
memory agency peer prepare \
  --listen 0.0.0.0:7447 \
  --advertise node-b.example:7447 \
  --project-root /work/b > node-b.card.json
```

通过你选择的可信渠道交换 card，再在两端用稳定的本地别名登记：

```sh
memory agency peer enroll \
  --alias node-b --project-root /work/a < node-b.card.json

memory agency peer enroll \
  --alias node-a --project-root /work/b < node-a.card.json
```

最后在两个项目中完成 setup：

```sh
memory agency setup --runtime pi --project-root /work/a
memory agency setup --runtime pi --project-root /work/b
```

`--advertise` 必须是 peer 能访问的地址；`0.0.0.0` 可以监听，但不能作为广播
地址。Agency 不提供自动发现、传递信任或全局成员列表。

每个节点始终保有自己的 authority。远端交付只是接收方的候选输入；接收方
会验证身份与 Artifact，并按自己的规则重新接纳，而不会直接导入对方的事实
或完成状态。

## View → Intent → Receipt

规范架构见 [memoryd 协议](memoryd/protocol.md)。协议刻意小于任何一个
内置协作能力。

普通使用中的核心循环是：

```text
View -> Intent -> Receipt -> View'
```

- **View** 是当前项目状态的有界快照，包含当前责任、只读证据，以及本次允许
  提交的选择。
- **Intent** 是 Agent 针对这个 View 提出的一个结构化状态变更。它只能使用
  该 View 提供的选择与临时 handle。
- **Receipt** 记录确定的 `accepted` 或 `rejected` 结果。`replayed` 是同一
  operation 既有结果的元数据，不是第三种 outcome，也不会产生第二次效果。
- **View'** 是已接纳持久状态在后续合格 Host 边界上的新投影。

rejected Receipt 会在有界纠正额度内保留同一个 View；accepted Receipt 才会
结束当前受治理的 Host opportunity，Agent 到后续合格边界才读取 View'。

一次被接纳的 Intent 会原子地写入一个不可变 Event、应用其封闭后果并返回
Receipt。拒绝会记录 admission 结果，但不产生 Event 或声明的后果；精确重试
返回既有结果，不会产生第二次效果。`input_invalid` 等控制结果不是 Receipt。
过期 View、伪造字段、越界输入或缺失证据都会以失败关闭。

Agency 中常见的三个对象是：

- **Handling**：仍需处理的责任；
- **Artifact**：按内容寻址并校验哈希的证据；
- **Reference**：带 CAS head、没有 owner 或完成状态的本地持久材料；active
  head 指向已验证 Artifact，retracted head 则作为不带 Artifact 的 tombstone
  继续可见。

## 完成与安全语义

只有显式提交并被接纳的 `handling.resolve.completed`，且至少附带一个本地可用、
哈希校验通过的 Artifact，才会记录为完成。`declined` 和 `unresolved` 可以关闭
责任，但不会宣称任务成功。

最终回答、进程退出、Runtime 空闲、provider 成功、网络 ACK、peer Receipt
或远端完成都不会自动完成本地责任。远端结果可以成为证据，但仍需本地 Agent
基于新 View 明确决定。

Agency 保护的是持久状态的接纳边界，不是所有 Runtime 副作用。Pi 执行的
shell、文件或外部服务操作仍由 Runtime 与操作系统的权限模型负责。远端文本
和 Artifact 仍是不可信内容；不要把密钥放入 Intent、Artifact 或协作文本。

查看可公开使用的命令：

```sh
memory agency --help
```
