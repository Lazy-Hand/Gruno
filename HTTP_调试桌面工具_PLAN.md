## Context

在 `/Users/lazyhand/workspace/project/gruno` 实现可实际使用的本地 HTTP 调试桌面工具：MyGo + Web 前端，保留原生 TypeScript，不引入 React/Vue。用户明确要求 Apifox 风格的项目首页与请求工作台、多个隔离的本地项目、多标签、集合及重启恢复、环境变量、Postman Collection v2.1 导入；请求体支持文本/JSON 与 URL 编码表单，认证支持 Basic、Bearer、API Key。导入必须预览兼容问题，保留不支持配置的原文，受影响请求在用户明确替换或移除相关能力前不得发送。认证信息随项目明文保存在本地，并明确提醒用户。

## 已确认依据

- 当前项目 `go.mod:1-11`：MyGo v0.7.4、Go 1.27.3；`main.go:38-66` 使用 `mygo.NewWindow(WindowOptions{URL:"/"})`，是 Web 模板。`src/main.ts`、`index.html`、`src/style.css` 目前只有 Greeter、系统信息与秒钟示例，没有业务模型、测试或前端框架。
- `package.json` 已提供 `bun run generate`、`bun run typecheck`、`bun run build`、`bun run dev`；`mygo.config.ts` 使用 Vite 5173、`dist`、`src/mygo.ts`、输出目录 `build`。`tsconfig.json` 已启用 strict、noUncheckedIndexedAccess。沿用这些模式，不增加 UI 框架或导入 SDK。
- MyGo 源码根为 `/Users/lazyhand/go/pkg/mod/github.com/egoist/mygo@v0.7.4`。已读 `README.md:20-31`：macOS Web 模式用 WKWebView，不打包 Chromium；`ui/doc.go:1-50` 是另一套自绘 UI，本项目不使用。
- 已读 `docs/bindings.md:61-100,141-174,266-322`：每次 IPC 独立 goroutine；首参 `context.Context` 在窗口关闭/导航时取消且不出现在 TS 中；通道在前端 close 时也取消调用 context；支持 `(value,error)`；绑定自动生成，不手改 `src/mygo.ts`。生成绑定会执行 `main` 到 `App.Run`，因此构造服务时不得读写业务数据。
- 已读 `paths.go:49-92` 的 `mygo.App.Path(mygo.PathUserData)`、`dialog.go:126-156` 的文件对话框契约。存储只放应用用户目录，不放仓库。导入采用 Web 文件选择以减少后端导入会话状态，不需要新增原生文件对话框服务。
- 已读 `plugins/fetch/fetch.go:1-21,86-174`、`netproxy/netproxy.go:1-34,78-87`：可复用系统代理 transport、Host/Content-Length 处理与响应体关闭方式。标准 Fetch Response 不能暴露 Set-Cookie，本项目须用自有服务返回完整响应头，而非直接包装前端 fetch 插件。
- 已读 `single_instance.go:39-53,116-121`：可复用单实例锁；生成绑定时锁会自动跳过。已读 `docs/plugins/automation.md:17-44,46-100,210-309`：官方 e2e 插件可经 Unix socket eval 实际 WebView 和截图，必须用 e2e build tag 排除正式构建。
- 本机默认固定 Go 为 1.27.2，但已实际执行 `/Users/lazyhand/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.3.darwin-arm64/bin/go version` 得到 `go1.27.3 darwin/arm64`；使用这个缓存工具链，不降低 go.mod。`rex` 不在 PATH，不依赖它做验收。
- LSP 已确认 Greeter 在 Go 侧引用只有 `main.go:13,16,31,39`，Tick 只有 `main.go:36,57`；TS 使用在 `src/main.ts`，生成声明在 `src/mygo.ts`。切换时完整删除示例类型、事件、计时 goroutine、DOM 与对应依赖导入，不留兼容层。
- 官方格式：[Postman v2.1 schema](https://schema.getpostman.com/json/collection/v2.1.0/collection.json)，已读取顶层及 URL 结构。支持 request/url 的字符串或对象、嵌套 item、disabled 行、集合变量、继承 auth。只承诺 v2.1，不扩展 v2.0、OpenAPI、原生 Apifox 或环境文件导入。[Apifox 导出文档](https://docs.apifox.com/export-data) 确认能选择 Postman 格式。
- 已读取 [Apifox 发送请求说明](https://docs.apifox.com/send-request)、其中请求工作台截图 `https://api.apifox.com/api/v1/projects/5097254/resources/477637/image-preview`，及 [创建项目说明](https://docs.apifox.com/create-api-project)。视觉参考为白/灰面板、紫色主操作、方法色标、上请求下响应。只借鉴布局，不复制其品牌图标、专有源代码或不存在的业务按钮。

## Approach

以下顺序保持可构建；第 2、3 步均依赖第 1 步，可独立实现，第 4 步集成前三步。所有列为“新建”的路径是计划产物，当前尚不存在。当前无等价业务实现；新代码仅承担明确的模型、文件存储、HTTP 执行、导入、DOM 状态职责。

### 1. 固定数据契约，替换问候示例

在根目录新建 `model.go`（package main），定义下述模型，JSON 一律 lowerCamelCase，IDs 为 16 个随机字节的 32 位小写十六进制字符串。Go 用 crypto/rand + hex，TS 的 `src/state.ts` 用 crypto.getRandomValues；不用索引或名称当身份。字符串枚举使用 Go 命名 string 类型及常量，生成 TS 联合类型；不重复手写业务接口。业务服务均挂在当前 package main，不新增多层 repository/domain 框架。

- `Pair {id,key,value,description:string; enabled:bool}`：用于 Query、Headers、表单、Path 参数及变量；数组保留重复键及顺序，不能转换为丢失重复值的 map。
- `BodySpec {mode:"none"|"json"|"text"|"urlencoded"; text:string; form:Pair[]}`。切换模式保留编辑内容。
- `AuthSpec {mode:"none"|"basic"|"bearer"|"apikey"; username,password,token,key,value:string; location:"header"|"query"}`。Basic/Bearer 使用 Authorization；API Key 指定名字与位置。不新增 OAuth、签名或 Cookie 管理器。
- `ImportIssue {id,code,message:string; blocking,acknowledged:bool}`。
- `RequestData {name,method,url:string; query,pathParams,headers:Pair[]; body:BodySpec; auth:AuthSpec; sourceItemPath:string; importIssues:ImportIssue[]}`。`url` 存不含字面 Query/fragment 的 URL 模板；`sourceItemPath` 是原导入 JSON 的位置，仅用于查看原文，不参与网络请求。
- `SavedRequest {id,folderID:string; order:int; data:RequestData}`；`Folder {id,parentID,name:string; order:int}`。空 folderID/parentID 代表集合根。`Collection {id,name:string; variables:Pair[]; folders:Folder[]; requests:SavedRequest[]; sourceJSON:string}`。导入原文只在集合中保存一份，不在每个请求重复保存。
- `Environment {id,name:string; variables:Pair[]}`；`DraftTab {id,requestID,collectionID:string; draft:RequestData}`，空 requestID 表示尚未保存到集合。
- `Project {schemaVersion:int; id,name,updatedAt:string; collections:Collection[]; environments:Environment[]; activeEnvironmentID:string; tabs:DraftTab[]; activeTabID:string}`。schemaVersion 固定 1，updatedAt 为 UTC RFC3339。持久化不含响应、正在执行的任务、取消通道、DOM、格式化 JSON 缓存。
- `ProjectSummary {id,name,updatedAt:string; requestCount:int}`；`Preferences {schemaVersion:int; theme:"system"|"light"|"dark"}`。默认 system；不持久化当前页面，启动先到项目首页，每个项目独立恢复标签和环境。
- `SendInput {request:RequestData; collectionVariables,environmentVariables:Pair[]}`。
- `ResponseHead {statusCode:int; statusText,url,protocol:string; headers:Pair[]; contentLength:int64}`。响应 Pair 的 id 可在映射时生成，enabled 固定 true；完整保留重复响应头，包括 Set-Cookie，不将数据解释为 HTML。
- `ResponseResult {bodyKind:"text"|"binary"|"unsupported-charset"; bodyText:string; sizeBytes:int64; truncated:bool; durationMs:float64; errorCode:""|"invalid-request"|"canceled"|"timeout"|"network"|"body-read"; errorMessage:string}`。errorCode 为空才代表本次请求成功完成，HTTP 4xx/5xx 不属于 errorCode。sizeBytes 是实际读取的应用层 body 字节数（标准库透明 gzip 时为解压后），不冒充网络传输字节数；截断时显示“至少 N B”。

先创建模型；随后按第 2、3 步实现 `Workspace`、`HTTPService` 的具体方法，再在 `main.go` 注册两个指针服务并生成绑定，不先注册不存在方法的空壳。业务前端集成前暂保留 Greeter/Tick 让原页面可构建，最后在第 4 步一并移除。服务构造只创建内存依赖；PathUserData 和业务文件访问延迟到绑定方法调用。App.Run 前调用 `RequestSingleInstanceLock()`，返回 false 即退出；在 WhenReady 中注册 OnSecondInstance，调用 App.Focus 与当前 win.Show。保留 MyGo 0.7.4、Vite、当前项目名及标识，不引入插件式业务层或第二套 RPC。

### 2. 本地项目与编辑恢复

新建 `workspace.go`，实现一个有 mutex 的 `Workspace`，构造参数为私有的目录解析函数，正式实现调用 `mygo.App.Path(mygo.PathUserData)`；测试传 t.TempDir。服务方法精确为：

```go
func (s *Workspace) ListProjects() ([]ProjectSummary, error)
func (s *Workspace) CreateProject(name string) (Project, error)
func (s *Workspace) LoadProject(id string) (Project, error)
func (s *Workspace) SaveProject(project Project) error
func (s *Workspace) DeleteProject(id string) error
func (s *Workspace) LoadPreferences() (Preferences, error)
func (s *Workspace) SavePreferences(preferences Preferences) error
```

- 数据位置：PathUserData 下新建 `workspace/`，内含 `preferences.json` 与 `projects/<id>.json`。新目录权限 0700、文件 0600；临时文件放同目录，写入、Sync、Close、Rename 后替换目标。写失败保留原文件、回传错误，不回退到“成功”。不引入 SQLite 或第三方存储包。
- 新安装 ListProjects 返回空数组，LoadPreferences 文件缺失返回 system。项目由 CreateProject 明确创建，不隐式创建示例项目。项目名 trim 后必须非空；可同名，因为 ID 才是身份。
- 所有路径中的 ID 校验 `^[0-9a-f]{32}$`，禁止路径穿越。Save 仅更新已存在的项目，防止已删除项目被排队保存复活；Create 单独生成 ID。验证 schemaVersion、ID 唯一性、folder 所属/无环、请求与标签引用所属项目、active ID 有效；空草稿 URL 合法，发送时另验。拒绝未知 schemaVersion，不覆盖数据。
- ListProjects 从各项目文件读取摘要并按 updatedAt 倒序、ID 次序稳定排序；任何损坏/读取失败须带文件名返回可见错误，不悄悄跳过。读取失败不启动自动保存，不把损坏项目覆盖为空项目；界面提供重试及错误文件路径，用户自行修复文件后可重新读取。
- 前端只加载当前项目，不把所有项目完整内容常驻内存。首页 CRUD：创建、重命名、进入、带确认的删除；删除前等待该项目保存队列完成，取消其请求，成功后返回首页。
- `src/state.ts` 管理纯数据、当前项目和标签；`src/workspace.ts` 串行化保存。编辑草稿、环境、结构变化后 350ms 防抖持久化当前项目快照；一次只允许一个 Save，执行期间的新变更合并为下一次快照，响应成功只确认对应版本，不将旧快照回写前端。项目切换/回首页先 await flush；失败显示“未保存”与重试，不丢本地编辑，也不继续切换。服务在同一个 mutex 内完成检查/读/写，生成 updatedAt 使用 UTC RFC3339Nano；前端不依赖它作为并发锁。应用使用 MyGo 单实例模式，不新增跨进程协作编辑或合并协议；SaveProject 检测目标已删除必须报错，不能复活项目。
- 集合、目录、请求均支持新建/重命名/删除；请求保存支持选择集合及目录。新建顺序用同父节点最大 order + 1，混合树按 order 展示。删除目录需确认连同后代；关闭相关标签前先走未保存处理，任一取消则不删除。删除集合/项目亦遵循同一处理。删除范围内不提供“保存回即将删除的位置”，要保留草稿就先另存到范围外；确认删除后不再以旧快照保存。只需操作菜单，不加入拖拽排序。
- 点击集合请求复用对应标签，否则创建一个独立草稿。切换标签不重置输入。保存按钮/Cmd或Ctrl+S 将草稿写入 SavedRequest；新草稿先选保存位置和名称。关闭修改过的标签给“保存/放弃/取消”，比较草稿与已保存请求；新空草稿可直接关。放弃只丢该草稿，不影响集合。未保存草稿仍随项目快照恢复，不能把自动恢复当成已保存到集合。
- 环境/集合变量均用 Pair 编辑器；支持创建/重命名/删除环境、项目内单个活动环境和“不使用环境”。删除活动环境后 activeEnvironmentID 置空，不自动选其他环境。所有变量值存字符串，重复的启用 key 最后一行生效，界面提示重复；禁用行不参与覆盖。
- 明文提示在首次创建项目的确认区、环境/认证编辑区固定可见：“凭据以明文保存在本机，不上传；同账户程序仍可能读取。”敏感值输入框默认遮挡，可显式切换显示，但不能声称已加密。不要将请求、响应、导入原文或凭据写日志。
- 关闭窗口/菜单退出也必须 flush，不能丢掉防抖期间编辑。`src/main.ts` 设置唯一页面钩子 `window.__grunoFlushBeforeClose: () => Promise<void>`，在 Window 的全局类型声明中加入此属性：锁定编辑/新动作、取消 HTTP、flush，失败恢复编辑并抛错。`main.go` 使用已核实的 `win.OnClose(func(e *mygo.CloseEvent))`：除一次性 allowClose 外立即 `e.PreventDefault()`，用原子 pending 标志防重复，另起 goroutine 用 15s context 调 `win.Page().EvalContext` 执行钩子，不能阻塞 UI 线程。钩子尚未注册时仅在 document.readyState 不是 complete 且没有业务 DOM 时可关；已加载但钩子丢失按错误处理。成功置 allowClose 后调用 `mygo.App.Quit()`；该路径再次触发 OnClose 时放行。失败用 `mygo.Dialog.Message` 显示“取消退出 / 不保存退出”，默认/取消均第 0 项；仅用户明确选第 1 项才放行，否则清 pending 留窗，并用页面 Eval 解锁编辑。系统强杀不保证最后 350ms 数据，不能宣称崩溃零丢失。生成绑定不安装事件回调，所有窗口相关操作只在 WhenReady 内注册。

### 3. Go 执行 HTTP、变量与取消

新建 `http_service.go` 与 `request.go`，复用 net/http、net/url、context、MyGo netproxy.Transport，不使用页面 fetch。绑定方法：

```go
func (s *HTTPService) Send(ctx context.Context, input SendInput, head *mygo.Channel[ResponseHead]) (ResponseResult, error)
```

`head` 不是空占位：收到响应头即 Send 一次，UI 随即显示状态与完整响应头，再读取有限响应体，最终由 promise 返回 ResponseResult。前端用每次调用专属 `new Channel<ResponseHead>(...)`；用户点取消调用 close，MyGo 会取消 ctx，Go 的 NewRequestWithContext 终止请求。窗口关闭/导航同样取消，不另加取消映射和 Cancel RPC。Send 返回前 defer 关闭 resp.Body。核心方法精确为 `func (s *HTTPService) send(ctx context.Context, input SendInput, onHead func(ResponseHead) error) (ResponseResult, error)`，Send 直接传 head.Send；prepareRequest 错误映射 invalid-request、ctx取消映射 canceled、deadline/net.Error.Timeout 映射 timeout、其他 client.Do 错误为 network、读 body 错误为 body-read，作为 ResponseResult 返回且 Go error 为 nil。只在桥发送失败或非预期内部错误时返回 Go error；不依赖 CallError 保留 Go 错误类型（MyGo 只保证 message/method）。错误文本禁止带认证值，URL 仅显示不带 userinfo/query 的源地址；UI 可在用户编辑器中看完整 URL，不将底层可能含凭据的错误原文直接暴露为日志。

请求构造规则固定如下，集中在可独立测试的非导出函数 `prepareRequest(ctx context.Context, input SendInput) (*http.Request, error)`，服务调用它：

1. 存在未 acknowledged 的 blocking 导入问题时拒绝；URL 空、非 http/https、无 host 或非法方法/头交给明确错误返回，不更改为其他 URL、不默认补 https。方法允许合法自定义 token，方法输入转大写；新请求默认 GET。
2. `{{name}}` 在 URL、Path 值、启用 Query/Headers/表单键值、文本体和认证字段中解析。环境覆盖集合；按最后启用行构建映射；禁用行忽略。支持变量值引用其他变量，用 visiting 集检测循环并缓存解析值；缺失、循环、`{{$...}}` 动态变量均在发送前报出变量名，不原样发送或替换为空；不泄漏值。禁用字段及未使用的变量无需解析。
3. URL 编辑器在 blur/Enter 时把粘贴 URL 中的字面 query 拆为 Query 行、移除 fragment；拆分支持含 `{{...}}` 的模板，不用 new URL 强制要求已解析 host。URL 栏显示不含 Query 的模板，并提示“Query 在 Params 中编辑”；不要维护两份可相互覆盖的 query 字符串。现有 query 行在用户粘贴含 query 的完整 URL 时由新 query 替换并显示该变更；仅编辑不含 query 的基础 URL 时保留已有行。
4. 变量替换后的 base URL 若自带 query（例如变量值带 query），保留它，再按行顺序追加启用 Query，保留重复键；各键值只编码一次。x-www-form-urlencoded 也按启用行顺序 QueryEscape 后连接，空键/值合法，不把表单 map 化。表格用于已解码键值；URL 粘贴/导入解析时只做一次 QueryUnescape。
5. Path 表支持 Postman `:name` 整路径段；匹配后以 PathEscape(value) 写入，正确保留带斜杠的编码段（设置 URL.Path 与 RawPath），缺值拒绝发送。不会把 host 端口误判为 Path 变量。没有引用的 Path 行忽略；不新增另一套大括号 Path 语法。
6. headers 逐行 Add 保留重复值；Host 放 r.Host；Content-Length 由 body 计算，用户提供该行时在 UI 标为自动管理且不透传。默认无 body；json/text 直接使用原字符串，JSON 模式不强制格式化或修改内容；表单按上条编码。用户无显式 Content-Type 时自动填 application/json、text/plain; charset=utf-8 或 application/x-www-form-urlencoded；显式值优先。
7. Basic 用标准库 SetBasicAuth；Bearer 拼 `Bearer `；API Key 加 header 或 query。认证助手目标与已启用手工 Authorization/API Key 同名字段冲突时拒绝，并提示删除其中一个来源，不静默覆盖。none 不生成认证。请求 Cookie 可通过手工头发送，不增加共享 Cookie Jar，防止不同项目自动共享登录态。

`HTTPService` 只持有共享 `http.Client{Transport: netproxy.Transport(), Timeout:30*time.Second}`。TLS 按系统信任验证，不做“忽略证书”开关；重定向遵循 net/http 默认最多 10 次与敏感头保护。UI 显示最终 URL 和总耗时，不宣称是完整 wire trace，不增加自定义重试。

响应内存行为：

- 用 io.LimitReader 将解压后 body 读取限制为 `8<<20` 加 1 个探测字节；超限截去探测字节并 truncated=true，及时关闭 body，不在后台继续下载；错误、超时、取消分别展示，取消不显示为成功。HTTP 4xx/5xx 仍作为完整 HTTP 响应展示。
- Content-Type 文本、JSON、XML（含 +json/+xml）且 UTF-8/ASCII 时返回 bodyText；缺 Content-Type 可用 http.DetectContentType。二进制或声明其他字符集只给元信息，不强转字符串或内嵌 HTML。文本编码先验证；不合法 UTF-8 返回 unsupported-charset，不替换乱码；超限时仅允许裁掉结尾不完整 rune。空 body 固定 text。未解码的 Content-Encoding 数据按 binary 展示，不冒充解码文本；沿用 net/http 默认 gzip 行为，不新增解压库。不提供尚未实现的下载/图片预览按钮。
- durationMs 从 client.Do 前开始，到读取结束为止。响应头来自 Go Head channel，包括协议、重复 Set-Cookie、contentLength（未知为 -1）；不可依赖浏览器 Headers 合并它们。
- 前端开始新请求即释放该标签旧响应；只保留每个标签最近一次原文，所有标签合计原文 UTF-8 大小上限 32MiB，超限先逐出最久未访问的非活动响应并显示“响应缓存已释放，可重新发送”。关闭标签/项目切换释放响应及取消通道。Go 不缓存 body；前端不持久化响应，不构建隐藏标签的响应 DOM。
- Raw 用 pre.textContent；Pretty JSON 仅用户选择时计算，限原文 1MiB；更大提示使用 Raw。只保存活动标签一次格式化结果，切换/新请求释放；JSON 解析失败显示错误并仍可看 Raw。所有远端/导入文字用 textContent/value，不经 innerHTML；不运行响应 HTML、脚本或 Postman 脚本。

### 4. 原生 TypeScript 页面和交互

在 `index.html` 新建 `<div id="app"></div>` 挂载区，替换现有 main 问候表单，lang 改 zh-CN。`src/main.ts` 负责启动、关闭保存钩子和启动加载错误；新增 `src/app.ts`（页面布局/事件分发）、`src/state.ts`（状态与纯操作）、`src/workspace.ts`（存取队列）、`src/request.ts`（HTTP 调用/取消/响应缓存）、`src/components.ts`（复用 Pair 表格、模态框、菜单）、`src/postman.ts`（第 5 步导入）。使用 addEventListener 和小型具名渲染函数，不建立通用虚拟 DOM、事件总线、依赖注入或组件框架。只重绘受影响区域；输入期间不替换正在编辑的 input/textarea，不能造成焦点、选区或中文输入法丢失。

- 项目首页：窄主导航、页头“本地项目”、搜索框、新建项目按钮，项目卡片包含名称、请求数、最近更新、进入/重命名/删除菜单；空状态可新建。只展示实际支持的项目功能，不出现虚假的团队、登录、云同步或 Mock。
- 请求工作台：左侧 52px 导航栏与 252px 可拖宽集合栏（180–420px）；主区顶部项目名/返回首页、环境选择/管理、主题选择；下一行水平可滚动标签和新建按钮；请求行方法、URL、发送/取消、保存。请求区 Params/Headers/Body/Auth 标签；Params 包括 Query 与 Path；Body 四模式选择；Auth 四模式助手；底部响应区 Body/Headers，状态、耗时、读取大小、截断/错误标记。请求/响应之间可拖动高度分隔条（两区至少 160px）。集合树搜索按名称/URL 过滤且保留祖先，折叠状态只在当前会话保存。
- CSS 用自定义属性统一 light/dark tokens，默认跟随 prefers-color-scheme，用户选 light/dark/system 保存 Preferences。浅色近白底、细灰边框、紫色主操作、GET 绿/POST 橙/DELETE 红等方法色，不用外链字体/图标/CDN，不复制 Apifox logo。系统字体和简洁本地 SVG 图标足够。
- 默认窗口改 1280×820、最小 960×640，保持 StateKey "main" 与 URL "/"；BackgroundColor 使用与 CSS 默认 system 主题相同的 light-dark 颜色，页面布局满屏覆盖窗口背景；持久化显式主题在渲染业务 DOM 前应用。960 宽时收窄侧栏、URL flex:1 min-width:0，标签与表格各自滚动，不出现页面整体横向溢出。
- 在 request.ts 用标签 ID + 单调 run version 判断返回归属；标签 A 的结果不能覆盖 B，新请求/关闭后迟到结果不落地。同一标签只允许一个请求，执行中发送按钮变取消；切换标签不取消，关闭标签或切换项目取消。错误文本必须可见，不只 console.error。
- 生命周期错误分类使用 ResponseResult.errorCode；用户主动 close 通道后若 IPC 先拒绝，以本地取消意图显示 canceled，其余未知 IPC 错误显示调用失败。不用英文错误文案包含关系猜超时。请求失败时保留已经收到的响应头，明确响应体不完整；不展示旧 body 冒充本次结果。开始关闭/项目切换时停止接受新编辑，成功后关闭或切换，失败才解锁。
- 语义 button/input/select/textarea、aria-label、表格标题、可见 focus ring；模态框 Esc 取消、焦点进入/退出恢复。Cmd/Ctrl+Enter 发送当前请求，Cmd/Ctrl+S 保存，Cmd/Ctrl+T 新请求。全局快捷键不拦截输入法 composing 事件。删除有明确确认；没有实现的能力不放置可点击占位按钮。
- 新请求可临时发送而不保存；保存时必须指定集合。变量预览只显示已解析 URL/参数需要的内容，不自动在日志/通知里展开凭据。环境修改即时影响下一次发送，不改变正在执行请求的快照。
- 完成本步后删除 main.go 的 Greeter/SystemInfo/Tick/计时 goroutine 与 log 之外不再使用的导入，删除旧 DOM、CSS 示例；用 `bun run generate` 重生成 src/mygo.ts，前端只引用新服务。保留已有 resources/icon.png 与前端构建配置。

### 5. Postman v2.1 导入：先预览、再原子保存

新增 `src/postman.ts`：导出 `parsePostman(text: string): ImportPreview`，其中 `ImportPreview {collection: Collection; requestCount:number; folderCount:number; warnings:ImportIssue[]}` 是导入模块的 TS 局部接口，Collection/RequestData 等从生成绑定 import type。输入 unknown 后逐字段校验，不用 any 掩盖格式错误；当前没有解析器，手写该单一格式映射，不加 SDK/schema validator。官方 schema 只是格式依据，不在运行时联网下载。

- 从当前项目集合栏“导入 Postman”打开 `<input type=file accept=".json,application/json">`；取消无变化。文件大小上限 20MiB，嵌套目录上限 64，错误提示限制并不产生半集合。File.text 后解析；根必须 info.name 为字符串、item 为数组，info.schema 为 HTTPS 且 host 属于 schema.getpostman.com 或 schema.postman.com，pathname 严格为 `/json/collection/v2.1.0/collection.json` 或 `/collection/json/v2.1.0/draft-07/collection.json`。JSON 损坏/其他版本/错误结构拒绝，错误路径定位到 item；不悄悄转成空请求。预览无有效请求时禁止确认。
- 模态预览列出集合名、目录/请求数、每请求兼容警告，可展开原文。确认默认将整个集合追加到当前项目，不覆盖同名集合或按 URL 去重；生成全新 ID。同名集合保留原名、ID 区分；再次导入也是新集合。用户可取消；模态期间不修改其他项目内容。
- 确认前 flush 当前保存队列；以新集合构造候选 Project 调 Workspace.saveProject，成功才安装到前端状态及集合树。失败保留预览供重试，不重复追加。sourceJSON 保存上传原文一次，RequestData.sourceItemPath 保存定位，形式固定为 JSON Pointer（如 `/item/0/item/2`），通过指针定位请求及祖先获取原配置；用户始终可从集合/问题详情查看原文。不保存文件路径、也不读取 Postman 指向的外部文件。
- 递归映射 folder/item，保留名字、父子与混合顺序。request 字符串转 GET；对象 method/url 按字段读取。url 字符串拆 query；对象有 raw 时用 raw 的基础部分、如存在 query 数组以结构化 query 为唯一来源，否则解析 raw query；没有 raw 则用 protocol/host/port/path 组装，host 数组用点、path 字符串数组用斜杠；path 对象段只用明确的 value，无 value 则创建空 URL 草稿并标记 unsupported-url，必须显式修复才能发。fragment 不发送。query null key/value 转空字符串；header 支持数组或逐行 `name:value` 字符串，解析不了则保留原文并 blocking，disabled 转 enabled=false。所有可映射 description（字符串或 `{content}`）提取到 Pair.description，集合/请求备注未提供编辑功能则留在 sourceJSON，不冒充已丢失原文。
- url.variable 映射 Path 表；变量 key 缺省用 id，缺 value 转空串，非字符串用 JSON.stringify（字符串不再加引号），disabled 保留。集合级变量进入 Collection.variables，可在集合变量面板编辑；文件不含独立环境，不杜撰环境值。request/目录级额外变量若存在且不能无损映射则产生 blocking 警告，不擅自改变作用域。
- body absent/disabled -> none；`body.options.raw.language === "json"` 时 raw -> json，其余 raw -> text，不改原文；urlencoded -> Pair 表；formdata/file/graphql/其他 mode 保留 sourceJSON，对该请求创建 blocking issue（code unsupported-body），必须由用户显式选择“替换为支持的请求体”并确认新内容才能 acknowledge，不擅自转成空 body 发出。
- 遍历时计算 auth 继承：请求自身非 null auth 优先，否则最近目录，再集合；Postman noauth 明确停止继承并映射本地 none。支持 basic/bearer/apikey，将参数展开到每个请求 AuthSpec；apikey 的 in 只能 header/query。无法理解的 auth/缺失必需参数标记 unsupported-auth blocking；变量形式参数仍保持模板。flatten 仅影响导入结果，不执行任何认证脚本；原配置保留在 sourceJSON。
- 集合/目录/请求 pre-request event 的非空脚本（disabled 不为 true）对所有受影响请求标记 unsupported-prerequest-script blocking；详情展示“移除脚本依赖，按当前参数发送”，只有用户显式确认才解除；test/post-response 脚本与保存响应示例不执行/不转成实际响应，列为 advisory 并在导入确认中可见。显式 protocolProfileBehavior、非 HTTP(S) 协议、不可映射的字段影响请求行为时亦 blocking；元信息/描述不影响发送，不为每个无关扩展字段制造报错。
- 动态变量不实现，发现则 issue `unsupported-dynamic-variable` blocking；普通未定义变量在预览警告并由环境编辑后正常解析。替换含动态变量的字段后用相同检测更新该问题；不提供“忽略后原样发送”。未支持认证/请求体的解除入口分别在 Auth/Body 面板，不能用一个全局“忽略全部问题”放行。
- acknowledged 是用户明确处置的记录，随 RequestData 保存；删除/替换问题相关配置才解除对应 blocking，不因切换标签或保存自动解除。保留的 raw 原文不执行；导入部分可用并不等于整份 Postman 语义兼容。
- 其余 blocking 问题提供确定修复入口：无法解析 header（`unsupported-header`）需用户在 Headers 显式选择“以当前请求头替换导入配置”；不支持的路径段/作用域变量（`unsupported-url` / `unsupported-variable-scope`）需用户改成完整可解析 URL/变量后确认；protocolProfileBehavior（`unsupported-protocol-settings`）显示原配置，允许显式“移除导入协议设置，使用本工具网络设置”。每个入口只处理对应 issue，明确展示被舍弃原配置；原文继续保留。非 HTTP(S) URL 即使确认移除原配置，仍受服务端协议校验，必须修改 URL。所有发送阻断均由 prepareRequest 再验，不能仅禁用按钮。

## Critical files & anchors

实现前重读目标锚点，行号仅作提示；所有新路径按 Approach 明确创建。

1. `main.go:38-66` / `main`：注册服务、单窗口 Web 入口、关闭保存、删除示例；在服务注册后 App.Run 前加入 RequestSingleInstanceLock，第二实例直接退出；OnSecondInstance 中调用 `mygo.App.Focus()` 与 `win.Show()` 聚焦现有窗口（已读 app.go:412-416、window.go:796-809）。
2. `src/mygo.ts:1-37`：生成文件，不能手改；新业务模型及服务都从此 import。
3. `tsconfig.json:1-14`：严格 TS + DOM；保持约束，前端测试放 `test/`，不为测试全局污染生产 DOM 类型。
4. `mygo.config.ts:7-14`：复用 dev/build/bindings/out；不新增第二个构建器。
5. `/Users/lazyhand/go/pkg/mod/github.com/egoist/mygo@v0.7.4/docs/plugins/automation.md:17-44,46-100,210-245`：正式 WebView e2e、eval/capture 协议及发布隔离。

## Verification

计划阶段只进行了只读文件/文档读取与版本查询，未构建、启动应用或声称性能降幅。执行者须完成下面可重复验证；不是只通过编译就完成。

### 环境与命令

工作目录 `/Users/lazyhand/workspace/project/gruno`，给每个命令使用已核实的 Go 1.27.3 bin 在 PATH 最前，`GOTOOLCHAIN=local`，不降低 go.mod。示例：

```sh
env PATH="/Users/lazyhand/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.3.darwin-arm64/bin:$PATH" GOTOOLCHAIN=local bun run generate
env PATH="/Users/lazyhand/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.3.darwin-arm64/bin:$PATH" GOTOOLCHAIN=local CGO_ENABLED=0 go test ./...
bun test ./test
env PATH="/Users/lazyhand/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.3.darwin-arm64/bin:$PATH" GOTOOLCHAIN=local bun run build --skip-dmg
```

现有 node_modules/bun.lock 可用；新增标准库业务不需 npm install。只有官方 e2e 插件引发 Go 模块图必要变化时使用现有版本依赖解析，不升级 MyGo。bun run build 已含 tsc --noEmit + Vite；不重复构建前端。

### 持久性、变量与网络边界测试

新建根目录 `workspace_test.go`、`request_test.go`、`http_service_test.go`，所有文件访问用 t.TempDir，网络用 httptest；测试服务核心的 private 方法，不能要求 GUI loop，也不 mock 复述传参。保持永久测试针对消费者可见行为：

- 新建两项目，同名请求/环境互不混用；保存含未保存草稿后重载恢复标签顺序、字段、活动环境；删除后 Save 拒绝复活。无效 ID、坏 JSON、未知 schema 不覆盖现存文件；损坏文件读后仍保持原字节。文件不可写导致错误时旧数据仍能读。
- `{{base}}` 在集合为 A、环境为 B 且启用 -> 实际请求去 B；禁用环境项 -> A。变量链正确替换，循环/缺失/动态变量在服务端拒绝且 httptest handler 未收到请求。重复参数保持值，禁用行不发送，表单含中文、+、& 和空值编码正确，`:id` 值 `a/b` 在 EscapedPath 里仍是 `a%2Fb`。
- Basic、Bearer、两种 API Key 实际到达 handler；手工 Authorization/API Key 冲突不发送。Host 独立设置，Content-Length 来自真实 body。重复响应头及两个 Set-Cookie 完整可见；401/500 是响应而非网络失败。
- 超过 8MiB body 可观察 truncated 且无完整大 body 保留；正常 UTF-8、跨截断边界中文、空/204、二进制、非 UTF-8 charset 各得到指定 bodyKind；慢 handler 在 ctx 取消后收到 r.Context().Done()。新建服务核心私有方法 `send(ctx,input,onHead)` 供 Send 与测试复用，不用假通道回声测试。

新建 `test/postman.test.ts`、`test/state.test.ts`，用 Bun 自带 bun:test，构造符合官方 schema 的最小 fixture，不复制第三方业务数据：

- 嵌套文件夹 mixed order、字符串/对象 URL、重复 Query/Headers、禁用行、raw JSON、表单、集合变量与目录认证继承；noauth 确实停止继承；不因 raw/query 双来源重复追加。
- multipart/不支持 auth/祖先 pre-script 正确保留原文且产生逐请求 blocking；test script/示例响应为 advisory；解析失败或取消预览不修改项目，重试确认只追加一份。
- A/B 标签独立草稿、关闭取消不丢编辑、项目切换 flush 失败保持当前项目；旧 run version 不能覆盖新结果；响应 LRU 逐出有标记且活动标签优先保留。只测纯状态函数与行为，不测 DOM 源码、文案、生成代码或机械转发。

### 实际桌面闭环与视觉证明

使用 MyGo 官方 automation，不借用其他应用的浏览器标签。为 e2e 新建 `automation_e2e.go`，`//go:build e2e`，仅 blank import 官方插件；init 中只有当 `GRUNO_TEST_DATA_DIR` 非空时调用 App.SetPath(PathUserData,该目录)，不创建文件。发布构建不带 e2e tag，不暴露自动化 socket/测试目录开关。

执行者用工具创建用户私有的临时目录（如 /tmp 下 mktemp -d，权限 0700），以及一次性本地 HTTP fixture 服务（不保留为产品端点）：

- `/echo` 返回实际 method、EscapedPath、RawQuery、重复头、请求体的 JSON，添加两个 Set-Cookie；不加 Access-Control-Allow-Origin，以证明由 Go 发送不受 CORS 限制。
- `/slow` 等待请求 context 取消，输出取消观察；`/large` 返回 8MiB+1 字节；`/binary` 返回 application/octet-stream；`/status/401` 返回 401 文本。

可在一次性 Go 脚本中监听 127.0.0.1:0 并打印实际 URL；自动化步骤使用该 URL，不假定端口空闲。fixture 用 named service，记录输出，不重复运行只为取日志。

先启动真实开发应用（命令中的 `<private-dir>` 替换为上一步实际值）：

```sh
env PATH="/Users/lazyhand/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.3.darwin-arm64/bin:$PATH" GOTOOLCHAIN=local GOFLAGS=-tags=e2e MYGO_AUTOMATION=<private-dir>/app.sock GRUNO_TEST_DATA_DIR=<private-dir>/data bun run dev
```

通过 Eval 的 socket 客户端发送文档已核实的 windows/eval/capture 请求；eval 在真正 WKWebView 中执行，允许设置 input.value、派发 input/change、click 并等待 DOM 状态，不能绕过 UI 直接调用服务作为唯一证明。完成：

1. 首页创建“本地联调”和“隔离项目”，进入前者；创建集合/目录；环境 baseURL 指向 fixture，新增 POST `{{baseURL}}/echo`，文本 JSON、重复 Query、禁用 Header，发送后看到实际 echo 匹配、200、完整两个 Set-Cookie。Auth 切换并实际验证，不只是控件可点击。
2. 新建第二标签发 /slow，切回第一标签确认原草稿与响应保留；取消第二标签，在 fixture 输出观察 context canceled；401、binary、large 的显示符合上面规则。编辑环境后下一次请求改变，当前已发快照不变。
3. 用 file input 的标准 change 路径导入带嵌套目录、继承 Bearer、formdata 和 pre-script 的 fixture File（automation eval 可构造 File + DataTransfer 交给 input，走正常事件处理）；先取消确认树不变，再确认。可兼容请求能发，不兼容请求 Send 禁用且问题可见；显式替换/移除后方能发。手工检查一次系统文件选择取消/选择流程，无法操作该对话框时报告此项限制，不能宣称已验证。
4. 修改但不保存某请求，立即点窗口关闭（不等待 350ms），观察 flush 后才退出；使用相同隔离目录重启。首页仍有两个项目；重新进入时集合、环境、标签与未保存草稿恢复，但响应不恢复；另一个项目无这些集合/环境。将隔离测试数据的 workspace/projects 父目录临时改为只读（非 root 执行），制造一次真实原子保存失败，观察留窗与可重试，选择取消退出不丢草稿，再恢复目录写权限重试成功。重命名/删除确认取消和确认都按约定生效。
5. 分别在浅/深色、1280×820 与最小 960×640 捕获真实窗口 PNG并读取图片。检查整体布局、滚动、焦点、中文输入、无整体溢出、发送/取消/保存状态、空状态和错误反馈；图中不能残留 Greeter 或虚假占位按钮。保存必要截图作为交付证据，报告实际验证平台仅 macOS arm64。

最后启动不带 e2e 的正式产物 `build/gruno.app/Contents/MacOS/gruno`（如 CLI 实际命名不同，读取 build 目录使用真实名称），用隔离 HOME 保证不触碰用户数据。至少完成一次真实请求并观察响应；正式包不受 MYGO_AUTOMATION 控制。用 `du -sk build/gruno.app /Applications/Apifox.app` 报安装后体积；内存至少记录正式构建的空闲首页与一次 1MiB JSON 请求后两个场景，每个场景等界面稳定再用 `ps -axo pid,ppid,rss,command` 记录主进程及可归属 WebKit 进程，表中注明 RSS/KiB 与场景，不把 RSS 求和称独占物理内存。不启动用户安装的 Apifox 发业务请求来制造比较，不能仅凭主进程给整体内存降幅；无法可靠归属共享 WebKit 进程则明确整体占用未测得。不以本工具有限功能与完整 Apifox 作等价性能结论，不承诺未经测量的百分比。

## Assumptions & contingencies

- 已确定：MyGo + Web、原生 TypeScript、多本地项目、Postman v2.1、Basic/Bearer/API Key、文本/JSON/URL 编码表单、导入预览/问题保留/阻止误发、凭据本地明文。不因实施困难改 Native UI、引入框架、删首页或跳过导入。
- 默认双主题及系统跟随，参考 Apifox 工作台结构但不追求像素级/品牌复制。验收平台为当前 macOS arm64；不声称 Windows/Linux 已测试。用户以后指定更多平台或精确截图可另行调整，但本计划不加入团队、Mock、脚本执行、上传文件、OpenAPI/cURL/原生 Apifox 导入。
- 初始阈值明确为响应 8MiB、总原文缓存 32MiB、Pretty 1MiB、导入文件 20MiB/目录深度 64、请求超时 30s。这些是避免无界资源使用的产品限制，在 UI 中可见；不另做任意高级配置面板。若用户需要更大数据，应调整本计划再实施，不静默取消限制或假报完整结果。
- 若默认 Go 不能编译，使用已核实缓存 1.27.3；若执行环境已移除缓存，使用项目要求的 Go >=1.27.3，不编辑 go.mod 降级；下载需网络/权限时报告确切前提，先完成可达工作。
- 若 MyGo 生成器不接受某个模型字段，调整 Go 模型为已支持的命名 string、struct、slice，保持这里指定的 JSON 字段与行为，不退回 any 或手改绑定。若系统 TLS/代理导致外网失败，用本地 fixture 完成可达闭环，保留外网失败原错误，不关闭证书验证掩盖。
- 若发现执行时工作区已有用户新增业务，先重读并按其已有等价模式接入，不覆盖用户代码或重新建立第二套同功能模块。上文新文件是当前空模板下的确定分工，不能作为覆盖新用户文件的授权。
