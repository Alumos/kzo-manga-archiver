# kzo-manga-archiver

本地运行的漫画转存工具：使用账号密码登录 `kzo.moe`，读取漫画与卷/话列表，调用站点 EPUB 下载接口并发转存到 NAS。

## 运行

```bash
DOWNLOAD_DIR=/Volumes/nas/comics \
LIBRARY_PATH=/ \
WORKERS=2 \
go run .
```

打开 <http://127.0.0.1:8080>，在前台登录账号；连接和存储配置在 <http://127.0.0.1:8080/admin> 维护。密码只用于登录，不写入文件；登录态不保存在浏览器 `localStorage`，而是保存在服务端 Cookie Jar，并持久化到 `.kzo-session.json`。可用 `SESSION_FILE=/path/to/session.json` 修改位置。

编译部署：

```bash
go build -o koxmoe-transfer .
./koxmoe-transfer
```

## 说明

- 只使用 Go 标准库，前端是内置静态页面，视觉令牌和组件样式参考 shadcn/ui，避免 NAS 上额外运行 Node 服务。
- `kzo.moe` 使用 `/login_act.php`、`disp_divinfo`、`book_data.php` 和 `getdownurl.php`，代码已针对这套流程适配；其他上游仍使用通用 HTML/图片解析兜底。
- 登录 KZO 后会读取 `/my.php` 的 `user_level` 和 `is_vip`；状态会随服务端登录态保存，前台显示 `VIP` 或 `LV1`/`LV3` 等级。VIP 优先请求 `vip=1` 线路，普通账号请求 `vip=0`，失败时才切换备用线路。
- EPUB 下载会先走站点第一线路，遇到 CDN 500、网关错误或连接中断时最多尝试 3 次，再切换第二线路；上游文件下载超时为 15 分钟，慢线路的大卷可能需要等待几分钟。
- 默认会从首页沿分页抓取漫画卡片，最多 100 页；EPUB 按漫画详情的首个分类归档到“分类/漫画名”，文件名例如 `[Kmoe][木頭風紀委員和迷你裙JK的故事]卷01.epub`。
- `WORKERS` 控制单个文件内部的章节/图片工作线程，建议从 2 开始，过高可能触发站点限流；KZO 直链 EPUB 本身是单个 HTTP 文件流，不受该值影响。
- 不论同时建立多少任务，全局最多 2 个 EPUB 文件同时下载。
- 文件下载使用全局 FIFO 队列和 2 个固定 worker；失败卷可在下载管理中重试，重试会排到当前队列末尾，不会插队。
- 下载管理可取消排队中或进行中的整组漫画任务；取消会中止请求、跳过剩余队列并清理 `.epub-*.tmp` 临时文件，已完成 EPUB 保留。
- 章节选择页会实时扫描对应漫画目录，标记并自动跳过已有 EPUB；写入失败时会清理本次卷的临时文件。
- KZO 漫画章节会按同一 ID 从 `bookof.moe` 读取分卷封面，封面由服务端代理并懒加载；缺失封面显示占位文字。
- 前台分为漫画列表、漫画详情、章节下载/任务管理三个页面，详情和下载页之间使用翻页过渡；NAS 文件重命名与删除在 `/admin` 的下载目录区域完成，删除目录仅允许空目录。
- 后台可填写 HTTP 代理地址（例如 `http://192.168.31.3:7890`）并切换开关；“测试 Google 连通性”使用当前开关验证直连或代理链路。Docker 也支持 `PROXY_URL` 和 `PROXY_ENABLED` 环境变量。
- 账号密码不会写入日志或文件；会话 Cookie 会以 `0600` 权限保存，登出时删除。请仅在本地可信网络中运行。

## Docker / GitHub Actions

将仓库推送到 GitHub 后，`.github/workflows/docker.yml` 会在 `main`/`master` 更新时自动构建并发布：

```text
ghcr.io/<GitHub 用户名>/<仓库名>:latest
```

复制 `docker-compose.yml`，把镜像名和 volumes 左侧的 `/path/to/your/nas/comics` 替换为实际 NAS 下载目录即可。容器内统一使用 `/downloads` 作为下载目录，`/data` 保存登录会话；两个目录都应使用持久化挂载。Compose 默认以 root 运行，确保下载文件可以读写、重命名和删除。
