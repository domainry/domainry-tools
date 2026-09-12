# Domainry Tools

可复用工具实现与注册模块，不依赖 Agent 实现。

- `module.Registry.Register` 注册版本化定义、当前授权、执行、恢复和结果权限检查。
- `module.Registry.Select` 冻结工具子集；未知工具、重复工具、非法 schema 和缺少恢复操作的写工具注册失败。
- 直接调用工具也会校验输入、定义、当前授权、执行时限及输出大小 / schema。
- `module.Combine` 将明确声明的扩展键组合到一个既有工具宿主。
- `calculation`、`timeutil` 是迁出的确定性计算与时间实现。
- `internal/adapter/recordtools` 实现结构化文档工具，通过 `module.RecordAdapter` / `module.RecordSpec` 装配；只依赖 Knowledge 的记录契约。PM、Work 提供各自领域 schema 和校验。
- `module.CalendarAdapter` / `CalendarDefinitions` 注册账号发现、日历目录、安排、详情和共同空闲五个只读工具。适配层仅消费 Connector SDK 的中立日历契约及 Integration SDK 当前账号读取端口；宿主每次解析当前身份和具体账号动作权限。执行前后及历史复用均复核工具权限与账号来源，账号名称另受当前列表范围约束。敏感读取重放没有正文时明确失败；全天日期、DST 偏移、分页与不完整忙闲状态保留，文本缩短有显式标记。
- `module.MailAdapter` / `MailDefinitions` 注册 `mail_accounts`、`mail_list`、`mail_search`、`mail_read` 四个只读工具，只消费 Mail 契约与 Integration SDK。基础权限账号可以发现／查看邮件头，正文和搜索按具体操作授权；查询明确 Gmail／Graph KQL 方言。原邮件 ID、RFC ID、Reply-To、分页／搜索上限和正文完整性保留；列表每类地址最多 10、详情 20，缩短邮件头时 `metadata_complete=false`，不缩短地址和来源 ID。草稿通过产品选装的 Knowledge 成果能力保存，邮件工具不承担发送或存储。
- `module.WebAdapter` / `WebDefinitions` 注册 `web_search`、`web_fetch`，只消费 Connector SDK `web` 与 Integration SDK。宿主用 `ConnectionKey` 固定选择当前工作区的服务连接；模型不能传账号、服务地址、processor 或凭证，没有 `web_accounts`。可用性只检查该连接，个人账号或其他可用连接不能替代它；未配置时保持不可用。返回查询、原请求／来源 URL、未知完整性、截断与 owner `read_at`。结果是未受信任的资料，不执行页面指令；再次请求可能再次计费。
- 日历、邮件和网页的中立账号机制集中于 `internal/adapter/accounttools`：宿主实时身份、发现、执行前后授权、敏感重放和历史来源复核。业务适配器只提供固定操作、参数及展示规则，彼此不导入；共享机制也不依赖日历／邮件／网页协议。未声明发现操作时空工具 key 不会进入发现分支。原日历／邮件定义、操作身份、请求命名空间和结果格式保持兼容。
- 历史、记忆、执行查看等 Agent 原生能力仍由 Agent 提供；既有 Todo / Artifact 调用入口保留原子账本适配，数据实现由各业务模块拥有。

基础契约位于 `domainry-tools-sdk`，不要求调用方采用 Agent。新工具应把业务幂等回执保存在数据所属模块，`Reconcile` 查询回执，不以不明结果为由盲目重写。

`module.CalendarWriteAdapter` / `CalendarWriteDefinitions` 是独立选装的账号发现、事件检查、创建和修改四个工具；`module.MailWriteAdapter` / `MailWriteDefinitions` 是账号发现、发送和回复三个工具。原只读定义保持不变。共享写入机制只依赖中立 Tools / Integration SDK，日历和邮件参数及回执各自消费 Connector SDK 契约；不导入 Agent、Integration 或 Provider 实现。

写入必须装配宿主当前授权、具体账号 Subject 和 `toolsdk.ConfirmationVerifier`。验证器从执行 owner 的持久记录核对准确批准，结构化 Confirmation 字段本身不能授权。Agent 通过启动期 `AssembleTools` 提供这个可选端口；组合时应在其他包装前取得并传给写入适配器，不把数据库传给 Tools。当前额外确认策略不会被验证器覆盖。模型参数只包含账号键、发现所得 `account_updated_at` 与类型化 `request`，不能选择执行 ID、授权或凭证；Schema 与语义校验在请求确认前执行。

稳定 owner 请求 ID 由宿主 Runtime 与原执行幂等键生成，账号、动作及正文不参与生成新 ID，保留 owner 检测参数冲突的能力。调用前后检查当前授权及精确来源；结果不明或失败只通过 Integration 原回执端口核查，不重新发送。日程保留完整参与者、实际 ETag、PATCH 省略／清空和时区语义；邮件保持显式 To / CC / BCC 与完整纯文本，结果只有 accepted / delivery unknown。历史复用重新检查来源与权限，不要求已结束 worker 的租约，不重新请求批准。

在当前源码工作区执行：`GOWORK=../domainry-agent/go.work go test ./...`（实际使用时请传绝对路径，或从 Agent 根目录运行 `go test ../domainry-tools/...`）。

公开入口为 `module/module.go`，注册、选装、调用与组合实现位于 `internal/application/tool/`。架构检查禁止公开目录出现数据库或应用实现，JSON Schema 校验统一使用 `domainry-tools-sdk/schema`。

用户工具设置由 `module.OpenSettings` 装配：借用宿主数据库、Driver Profile 和唯一迁移账本，数据 owner 为 Tools。`module.SettingsHTTPAdapter` 声明两个当前用户设置动作，需宿主提供已认证的 Identity principal。设置只过滤当前授权清单，不授予工具权限；必须传入显式连接可用性端口，Tools 不依赖 Integration／Agent 实现。已接入 Agent／Work／PM；真实 Identity、SQLite/CAS、重启、浏览器工具开关和产品目录均有 F01 验收。连接故障时设置页面保留偏好并显示状态无法确认，执行策略仍拒绝不可确认的连接。Tools 浏览器契约在 `../domainry-tools-sdk/browser`，产品只提供会话绑定的 HTTP transport。
