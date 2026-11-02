# 全站顶部通知（System Banner）设计

日期：2026-11-02 · 分支：`ikun-new`（new-api fork）

## 1. 意图

临时维护或特别重要的事情，需要让**每一个打开网站的人**立刻看到。现有的“系统公告”（`Notice`）和“公告列表”只在右上角铃铛弹窗和仪表盘里，不够醒目。

所以新增一条**最高优先级**的全站置顶通知：贴在每个页面的最顶端，所有人都能看到，包括没登录的访客。用户可以手动关闭；关掉以后，这台浏览器就不再显示这条通知，直到管理员发布新通知。

## 2. 目标 / 非目标 / 反目标

**目标**
- 所有页面最顶端显示一条横幅：首页、登录/注册、定价、排行、模型状态、关于、法律文档等公开页面，以及整个控制台。
- 管理员在后台设置：启用开关、中文内容、英文内容、颜色档位（普通 / 重要）。
- 按界面语言显示：简体中文、繁体中文显示中文内容；其他语言（en、fr、ru、ja、vi）都显示英文内容。
- 文字一行放得下就居中显示，不滚动；放不下（文字长或屏幕窄）就像跑马灯一样横向滚动，鼠标移上去暂停。
- 用户点 ✕ 关闭后，关闭记录存在这台浏览器的 localStorage 里。管理员发布新通知后重新显示。

**非目标**
- 定时上线、定时下线。管理员手动关开关即可（用户决定）。
- 同时显示多条通知。
- 链接、加粗等富文本。
- 按用户、分组或角色定向投放。
- 在服务端记录谁关闭了通知。
- 修改或替换现有的 `Notice` 和公告列表，它们保持原样。

**反目标（出现了就算失败）**
- 横幅插进来后布局出问题：遮住页头或侧边栏、侧边栏和横幅重叠、控制台出现第二条滚动条、公开页面的标题被盖住。
- 用户关闭后，管理员没有发布新通知，用户却又看到了同一条。
- 管理员发布了新通知，之前关闭过的用户却看不到。
- 中文用户看到英文，或其他语言用户看到中文。
- 现有的 `Notice`、公告列表、铃铛弹窗行为发生变化。
- 改动数据库表结构。

## 3. 已定的事情（用户决定）

- 用途：临时维护和特别重要的通知。
- 显示范围：所有页面，包括未登录访客。
- 关闭后记录在用户本地浏览器，不再显示；**管理员发布新通知后要重新显示**。
- 只有管理员能设置，中文和英文**必须同时填写**。
- 语言规则：简体中文、繁体中文显示中文内容；其他语言都显示英文内容。
- 颜色：管理员选两档，“普通”是浅黄色提醒条，“重要”是红色警示条（草图里的 A 和 B 配色）。
- 滚动：放得下就居中不动，放不下才滚动。
- 下线：管理员手动关开关，不做定时。
- **什么算“新通知”**：管理员把开关从关闭改为开启，就算发布一次新通知，即使文字和上次完全一样。所有人都会重新看到，包括之前关闭过的用户。

## 4. 我（设计者）定的事情，请审阅

- **通知开启期间改了文字，也算新通知**：中文或英文任意一边的文字改动后，所有人都会重新看到。只改颜色档位不算，已关闭的用户不会再看到。
- **只显示纯文本**，不解析 HTML 或 Markdown，避免 XSS，也避免把横幅撑高。
- **一次只有一条**通知。
- **减少动画**：系统开启“减少动画”（`prefers-reduced-motion`）的用户看不到滚动，长文字改为自动换行完整显示，横幅随之变高。
- **滚动页面时横幅一直钉在顶部**，和页头一起保持可见。
- **权限沿用现有选项接口**：`PUT /api/option/` 只有 Root 能用，和现有 `Notice` 一样，不新开权限。
- **生效有延迟**：前端对 `/api/status` 缓存 5 分钟（`staleTime`），并会先用 localStorage 里的快照渲染。所以通知发布或关闭后，已打开页面的用户最多要约 5 分钟才看到变化，刷新页面后可能先闪一下旧状态。对维护通知来说可以接受，本次不改缓存策略。
- **长度上限**：每种语言最多 500 个字符，后端校验。
- **内容不能为空**：开启时，中英文都必须非空。

## 5. 后端

### 5.1 存储

用现有的全局配置机制（`setting/config` 的 `config.GlobalConfig.Register`），新建一个小配置结构，例如注册名 `system_banner`：

| 字段 | JSON | 说明 |
|---|---|---|
| Enabled | `enabled` | 是否启用，默认 false |
| Level | `level` | `normal` 或 `important`，默认 `normal` |
| TextZh | `text_zh` | 中文内容 |
| TextEn | `text_en` | 英文内容 |
| PublishedAt | `published_at` | 发布时间（Unix 秒），默认 0。由后端自动维护，见 5.2 |

选项键是 `system_banner.enabled`、`system_banner.level`、`system_banner.text_zh`、`system_banner.text_en`、`system_banner.published_at`，存在现有的 options 表里，**不改表结构**。具体放在哪个 `setting/` 子包由实现者决定，参考 `setting/console_setting/config.go` 的写法。键名不能以 `Token`、`Secret`、`Key`、`api_key` 结尾，否则会被 `GetOptions` 隐藏。

### 5.2 校验（`controller/option.go` 的 `UpdateOption`）

- `level` 只能是 `normal` 或 `important`。
- `text_zh` 和 `text_en` 去掉首尾空白后，每种最多 500 个字符（按 rune 计）。
- 把 `enabled` 设为 true 时，当前的 `text_zh` 和 `text_en` 都必须非空。
- 启用状态下，不能把任意一边的文字改成空。
- 错误信息用 `common.ApiErrorT` 加英文源文案，七个前端语言文件都要补上对应 key。

- `published_at` 不接受管理员直接写入，`PUT /api/option/` 收到这个键时拒绝。

**自动更新发布时间**（副作用，写在保存成功之后）：
- `enabled` 从 false 改为 true 时，把 `published_at` 设为当前时间。
- 启用状态下，`text_zh` 或 `text_en` 的值真正发生变化时，也更新 `published_at`。
- 修改 `level`、保存相同的值、在关闭状态下改文字，都不更新。关闭状态下改的文字，会在下次开启时一并生效。
- `published_at` 要和其他选项一样持久化到 options 表，保证重启和多实例部署时值一致。如果同一秒内发布两次，后一次要保证值比前一次大（例如取 `max(now, 旧值+1)`），否则用户的关闭记录会把新通知也盖住。

**保存顺序**：开启时，前端先保存文字和档位，最后保存开关，这样才能通过上面的校验。关闭时，先保存开关，再保存其他字段，这样关闭并清空文字也能通过校验。

### 5.3 下发（`controller/misc.go` 的 `GetStatus`）

只在启用时往 `/api/status` 里加一项：

```json
"system_banner": { "level": "important", "text_zh": "…", "text_en": "…", "published_at": 1793621505 }
```

关闭时不返回这个字段。`/api/status` 本来就不需要登录，访客也能拿到。

## 6. 前端

### 6.1 横幅组件

- 在 `web/src/routes/__root.tsx` 的 `RootComponent` 里挂载，放在 `<Outlet/>` 前面，这样覆盖所有页面。
- 用 `useStatus()` 读取 `system_banner`。
- 语言：`(i18n.resolvedLanguage || i18n.language).startsWith('zh')` 为真时显示 `text_zh`，否则显示 `text_en`。界面语言代码是 `zhCN` / `zhTW`，不是 `zh` / `zh-TW`。
- 外观：一行高的细条，从左到右依次是图标、文字、关闭按钮。
  - 普通：浅黄底、深棕字、底部一条黄色边。
  - 重要：红底白字。
  - 用项目的主题 token 或 Tailwind 颜色实现，暗色模式下也要清楚可读。
- 滚动：用 `ResizeObserver` 比较文字宽度和容器宽度。放得下就居中静止；放不下就横向循环滚动，hover 时暂停。在 `web/src/styles/index.css` 新增 `marquee` 关键帧，并加进现有的 `prefers-reduced-motion` 覆盖规则。为了无缝循环而复制的那份文字要加 `aria-hidden`。
- 无障碍：容器用 `role="region"` 加 `aria-label`（i18n）。关闭按钮要能用键盘操作，并有 `aria-label`（i18n）。不用 `role="alert"`，否则每次打开页面都会被读屏打断。
- 先按 `web/AGENTS.md` 的组件复用流程，看 `components/ui/alert.tsx` 等现有组件能不能组合使用；不能的话，在变更说明里写清楚原因。

### 6.2 关闭记录

- localStorage 只存一个值，例如键名 `system-banner-dismissed`，值是用户关闭时那条通知的 `published_at`。
- 当前通知的 `published_at` 等于存下的值时，不渲染横幅。新通知的 `published_at` 不同，所以会重新显示；用户再关闭时，用新值覆盖旧值。
- 只改档位不会更新 `published_at`，符合第 4 节的规则。

### 6.3 布局让位

横幅的实际高度写到 `document.documentElement` 的 CSS 变量 `--system-banner-height` 上，不显示时设为 `0px`。高度用 `ResizeObserver` 测量，这样换行或缩放时也准确。横幅本身是 `fixed top-0`，层级高于页头。

需要把这个变量加进去的地方（调研时找到的，实现者要再全局搜一遍有没有遗漏）：

| 位置 | 现状 | 调整 |
|---|---|---|
| `components/layout/components/header.tsx:28` | `sticky top-0` | `top` 加上横幅高度 |
| `components/layout/components/authenticated-layout.tsx:51-53` | `h-[calc(100svh-var(--app-header-height))]` | 再减去横幅高度 |
| `components/ui/sidebar.tsx:253` | `fixed top-[var(--app-header-height)]`，高度也用这个变量算 | top 加上、高度减去横幅高度 |
| `components/layout/components/authenticated-layout.tsx`（外层） | 页头上方没有留白 | 顶部留出横幅高度 |
| `public-header.tsx:202` | `fixed top-0` | `top` 设为横幅高度 |
| `public-layout.tsx` 的 `<main>` | `pt-20` | 加上横幅高度 |
| `features/home/components/sections/hero.tsx:83` | `pt-24 md:pt-32 lg:pt-36` | 加上横幅高度 |
| `features/pricing/index.tsx:154, 179, 224` | `pt-16 sm:pt-20`，sticky `top-20` 和 `max-h-[calc(100dvh-6rem)]` | 加上横幅高度 |
| `features/rankings/index.tsx:76`、`features/model-status/index.tsx:130` | `pt-16 sm:pt-20` / `pt-20` | 加上横幅高度 |
| `features/about/index.tsx:157`、`features/home/index.tsx:64, 81` | iframe 用 `100vh` / `h-screen` 算高度 | 减去横幅高度 |
| `public-header.tsx:362` | 手机抽屉 `pt-20` | 加上横幅高度 |
| `features/auth/auth-layout.tsx`、`features/setup/setup-wizard.tsx` | `min-h-svh` | 加顶部留白，避免内容被横幅盖住 |

其中有的页面内容可能自带了偏移，实现者需要逐一核对，不要重复叠加。

### 6.4 管理员设置

- 在 `features/system-settings/site/section-registry.tsx` 里新增一个区块，放在现有 `NoticeSection`（“System Notice”）旁边，写法参考 `maintenance/notice-section.tsx`。
- 表单字段：启用开关、颜色档位（普通 / 重要）、中文内容、英文内容。开启时两项内容都必填，并显示每种语言 500 字符的计数。
- 保存顺序：开启时最后保存开关，关闭时最先保存开关（见 5.2）。
- 表单里不显示、也不提交 `published_at`。
- 在 `features/system-settings/types.ts` 和 `site/index.tsx` 补上新键的类型和默认值。
- 把四个可编辑的新键加进 `hooks/use-update-option.ts` 的 `STATUS_RELATED_KEYS`，让管理员保存后自己的页面立刻刷新。
- 新增的界面文案都用 `t('English key')`，并补全七个语言文件（可用 `bun run i18n:sync`）。

## 7. 验证

- 后端：在现有的 option 测试文件里加一组表格测试，覆盖档位非法、超长、开启时文字为空、启用状态下清空文字、直接写 `published_at` 被拒绝等情况；再覆盖发布时间的更新规则：关→开会更新（文字相同也更新），启用时改文字会更新，改档位、保存相同的值、关闭状态下改文字都不更新，同一秒内连续发布两次值仍递增。用 `require` 和 `assert`。
- 本次不改表结构，选项值走现有的 options 表读写，因此不需要做三种数据库的迁移验证。如果实现中改动了 model 或迁移代码，就必须按 AGENTS.md 补做验证。
- 前端：按 `web/AGENTS.md` 的约定跑 lint、类型检查、构建，并为下面两条逻辑写测试：
  - 语言选择：`zhCN`、`zhTW` 显示中文，其他语言显示英文。
  - 关闭判断：关闭后，`published_at` 不变就不显示，`published_at` 一变就重新显示。
- 手动检查：
  - 在首页、定价、登录、控制台（展开和折叠侧边栏）、手机宽度下，分别看短文字和长文字的效果。
  - 打开系统的“减少动画”，看长文字是否换行完整显示。
  - 切换暗色模式。
  - 点 ✕ 关闭后刷新页面，确认不再出现。
  - 管理员关掉再打开（文字不变）后刷新，确认重新出现；通知开启期间改文字后刷新，也确认重新出现。

## 8. 留给实现者

- 配置结构具体放在哪个 `setting/` 子包，用什么 Go 类型名。
- 图标的选择、具体的颜色值和横幅高度（草图是 36px），以及滚动速度。滚动速度建议按文字长度换算成恒定的像素速度，而不是固定时长。
- 后台区块里要不要加一个实时预览。
