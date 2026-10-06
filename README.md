# Miyabi

配合 115 网盘的一站式 Jav & Emby 管理平台。

Miyabi 支持直接对接 115 进行刮削、订阅和 STRM 导出，并内置 Emby 播放反代，让客户端通过 302 直接请求 115 CDN。

## 主要功能

- 115 扫码授权、媒体根目录挂载、独立磁链下载目录及订阅下载。
- 按目录选择刮削或同步已有元数据，生成 STRM、NFO 和图片，保留已有同步文件。
- Emby 增量刷新通知、媒体路径映射与演员头像同步。
- 内置 Emby 302 播放反代，复用现有 115 直链解析；无需另行运行 MoviePilot 或 MediaWarp。

## 项目预览

![媒体库](./screenshots/ScreenShot_2026-09-19_131853_357.png)
![发现](./screenshots/ScreenShot_2026-09-19_132213_340.png)
![搜索](./screenshots/ScreenShot_2026-09-19_132227_063.png)
![设置](./screenshots/ScreenShot_2026-09-19_132249_331.png)

## Docker 部署

> [!TIP]
> 您可以借助 AI 完成项目的部署，如果要对公网访问，请注意网络安全。

以下示例从当前分支源码构建镜像，包含本分支的目录处理和内置反代功能。使用 Docker CLI 前，在项目根目录执行以下命令；Compose 示例会自动构建：

```bash
docker build -t miyabi:local .
```

如使用已发布镜像，请将示例中的 `miyabi:local` 替换为对应仓库已发布的版本标签。内置反代是本分支功能，部署时应使用包含该功能的构建。

### 1. 使用 Docker CLI 运行

请将 `/path/to/data` 替换为主机上的数据持久化目录、`<宿主机IP>` 替换为实际地址，并将 `change-this-password` 替换为自定义密码：

```bash
docker run -d \
  --name miyabi \
  --restart unless-stopped \
  --security-opt no-new-privileges:true \
  -p 8080:8080 \
  -p 8099:8099 \
  -v /path/to/data:/app/data \
  -e MIYABI_ACCESS_PASSWORD='change-this-password' \
  -e MIYABI_PUBLIC_URL='http://<宿主机IP>:8080' \
  miyabi:local
```

> [!NOTE]
> 管理页面使用 `8080`，内置反代默认使用 `8099`。反代默认关闭，需在设置页启用；仅映射端口不会开启反代。若不使用此功能，可省略 `8099` 的端口映射。
>
> STRM 与元数据默认输出在 `/app/data/emby`（对应宿主机目录 `/path/to/data/emby`）。将该目录挂载到 Emby，例如容器内的 `/media`，并创建对应媒体库；Miyabi 的“Emby 媒体库路径”也填写 `/media`。宿主机数据目录需允许容器用户（UID/GID `10001`）写入。

### 2. 使用 Docker Compose（推荐）

在项目根目录创建 `docker-compose.yml` 文件：

```yaml
services:
  miyabi:
    build:
      context: .
    image: miyabi:local
    container_name: miyabi
    restart: unless-stopped
    security_opt:
      - no-new-privileges:true
    ports:
      - "8080:8080"
      - "8099:8099"
    volumes:
      - /path/to/data:/app/data
    environment:
      - MIYABI_ACCESS_PASSWORD=change-this-password
      - MIYABI_PUBLIC_URL=http://<宿主机IP>:8080
      # 可选：直接通过环境变量预设 Emby 集成（也可启动后在 Web 设置页中配置）
      # - MIYABI_EMBY_ENABLED=true
      # - MIYABI_EMBY_SERVER_URL=http://<Emby_IP>:8096
      # - MIYABI_EMBY_API_KEY=your-emby-api-key
      # - MIYABI_EMBY_MEDIA_PATH=/media
```

执行启动：

```bash
docker compose up -d --build
```

启动后访问 `http://<服务器IP>:8080`。

查看日志：

```bash
docker logs -f miyabi
```

### 升级镜像

更新源码后重新构建，并重新创建容器。使用 Compose 时执行：

```bash
docker compose up -d --build
```

使用 Docker CLI 时执行：

```bash
docker build -t miyabi:local .
docker stop miyabi
docker rm miyabi
```

然后重新执行上面的 `docker run` 命令，沿用原访问密码、数据目录和端口映射。使用已发布镜像时，先拉取所选版本，再按对应部署方式重新创建容器。

## 115 目录与 Emby 媒体库

在“设置 → 115 网盘”中完成扫码登录并挂载媒体根目录，然后配置磁链下载目录和各子目录的处理方式：

- **刮削**：生成项目管理的 STRM、NFO 和图片，默认输出到 `miyabi/<番号前缀>/<番号>/`。
- **同步元数据**：保留 115 的原目录结构，同步 NFO、图片，并为视频生成 STRM；本地已存在的文件会跳过。

默认两类内容共用 `/app/data/emby` 根目录；`miyabi` 子目录专用于项目刮削，不要将原路径同步目标设到该子目录。需要其他同步目标时，可在目录设置中填写服务器上的绝对路径，并挂载到 Emby。

“设置 → Emby”中的**服务器地址**填写原 Emby 服务地址，例如 `http://192.168.1.100:8096`，填写 API Key 并测试连接。Docker bridge 网络中，`localhost` 指向 Miyabi 容器自身；Emby 应使用它的容器服务名或可访问的宿主机地址。

## 内置 Emby 302 播放反代

### 启用与连接

1. 在“设置 → Emby”中启用 Emby 集成，保留原服务器地址和 API Key。
2. 开启“播放反代”，监听地址默认 `0.0.0.0:8099`，不能与 Miyabi 主端口或原 Emby 服务冲突。
3. “反代访问地址”填写播放器能访问的完整根地址，例如 `http://192.168.1.100:8099`。通过公网 HTTPS 入口访问时，填写对应域名，并让该入口转发到反代端口。
4. 保存并确认“播放反代已运行”，将播放器中的 Emby 服务器地址改为反代访问地址，然后正常使用 Emby 用户登录。

| 地址 | 示例 | 用途 |
| --- | --- | --- |
| Miyabi 管理/STRM 地址 | `http://192.168.1.100:8080` | 管理页面与生成的固定 STRM URL |
| 原 Emby 地址 | `http://192.168.1.100:8096` | Miyabi 的 Emby 配置及反代上游 |
| 内置反代入口 | `http://192.168.1.100:8099` | 播放器连接的 Emby 服务器地址 |

反代设置通过 Web 界面保存到数据库，目前没有对应的 `MIYABI_PROXY_*` 环境变量。修改监听端口后，需同步调整 Docker 端口映射；例如宿主机使用 `9000`、容器仍监听 `8099`，映射为 `9000:8099`，反代访问地址填写 `http://<宿主机IP>:9000`。

### 播放链路与使用条件

```text
客户端 → 内置反代 → Emby 验证身份并返回播放信息
客户端 → 内置反代 → 获取 115 临时直链 → 返回 302
客户端 → 115 CDN → 视频内容
```

反代只对本项目生成的 115 STRM 进行直接播放处理，保留 Emby 的登录、浏览、封面、字幕、WebSocket 和进度上报。它使用客户端的 Emby 凭据验证播放请求，不以配置的管理 API Key 替代客户端权限。

客户端需要能够直接解码视频格式。命中的 115 播放请求解析失败时返回错误，不回退到服务器串流或转码；普通本地媒体和未命中的来源仍按原 Emby 行为透传。视频主体直接来自 CDN，页面、图片、字幕、探测和 API 请求仍可能经过服务器。

继续连接原 Emby 地址会绕过内置反代。保存时端口被占用会保留原设置；启动时反代监听失败，Miyabi 管理页面仍可访问，可在设置页查看原因并修正。浏览器可能缓存原播放器脚本，启用后请刷新再验证。

实现已通过后端全量测试、构建、静态检查及前端测试和构建；真实 Emby、115 与播放器的兼容性需在部署环境中验证。可检查客户端视频请求是否跳转到 CDN，以及 Miyabi/Emby 是否仍持续传输与影片码率相当的视频流量。

## 启动配置

启动配置使用环境变量，未设置时采用默认值。Emby、目录处理及播放反代的动态设置在 Web 界面保存到数据库。监听地址与数据目录显式设为空时会报错。

通过 `MIYABI_ACCESS_PASSWORD` 配置访问密码。未配置密码或将密码设为空时关闭门禁。**强烈建议您开启门禁**。

| 环境变量                 | 用途                                                  | 默认值                                        |
| ------------------------ | ----------------------------------------------------- | --------------------------------------------- |
| `MIYABI_ACCESS_PASSWORD` | Web 入口访问密码                                      | 空，关闭门禁（强烈建议配置）                  |
| `MIYABI_PUBLIC_URL`      | 外部访问 Miyabi 的根地址，用于生成 STRM 播放直链      | 默认根据监听端口推导                          |
| `MIYABI_LISTEN`          | HTTP 监听地址                                         | `:8080`                                       |
| `MIYABI_DATA_DIR`        | SQLite 与图片缓存目录                                 | 容器内为 `/app/data`，二进制为 `./data`       |
| `MIYABI_LOG_LEVEL`       | 日志级别：`debug`、`info`、`warn`、`error`             | `info`                                      |
| `MIYABI_EMBY_DIR`        | STRM 与媒体导出目录                                   | `$MIYABI_DATA_DIR/emby`                       |
| `MIYABI_EMBY_ENABLED`    | 是否启用 Emby 集成通知刷新                            | 配置了服务器或密钥时自动为 `true`             |
| `MIYABI_EMBY_SERVER_URL` | Emby 服务器访问地址（如 `http://192.168.1.100:8096`） | 空，可在 Web 界面动态配置                     |
| `MIYABI_EMBY_API_KEY`    | Emby API 密钥（在 Emby「高级」→「API 密钥」中生成）   | 空，可在 Web 界面动态配置                     |
| `MIYABI_EMBY_MEDIA_PATH` | Emby 容器内挂载的媒体库路径（如 `/media`）            | 空（默认使用本地路径），可在 Web 界面动态配置 |
| `MIYABI_EMBY_SYNC_ACTORS` | 是否同步演员头像，`false` 或 `0` 关闭               | `true`                                      |
| `MIYABI_STRM_TOKEN`      | STRM 播放直链访问鉴权 Token                           | 空，未启用 Token 鉴权                         |
| `MIYABI_JWT_SECRET`      | Web 登录令牌的签名密钥                               | 空，按访问密码推导                           |
| `MIYABI_TRUSTED_PROXIES` | 可信 HTTP 代理 IP/CIDR，多个值使用逗号分隔           | 空，不信任代理转发的客户端 IP                |

> [!TIP]
> Emby 相关配置（服务器地址、API Key、媒体库路径等）除通过环境变量在容器初始化时配置外，也可以在服务启动后随时通过 Web 界面「设置」→「Emby」中进行可视化配置与连通性测试。

## NSFW 警告

本软件可能存在裸露、暴力、色情或冒犯等不适宜公众场合的内容，请勿在公共场合使用本软件，避免不必要的纷争。

## 特别鸣谢

感谢 [莫愁](https://github.com/zk020106) 的 token，感谢 [老罗](https://github.com/luoqiz) 的115账号。

## 致谢

本项目参考了以下项目的部分实现，在此表示衷心的感谢！

- [javdb-cli](https://github.com/FlanChanXwO/javdb-cli)
- [MoviePilot-Plugins](https://github.com/DDSRem-Dev/MoviePilot-Plugins)：115 STRM 与 Emby 反代的功能设计参考。
- [MediaWarp](https://github.com/DDSRem-Dev/MediaWarp)：Emby 播放信息与 302 处理的设计参考。

同时感谢社区 [LinuxDO](https://linux.do) 的帮助。

## 免责声明

本项目仅供学习、研究和技术交流使用。项目作者与任何第三方服务、原始应用或内容提供方无关。
使用者应自行遵守当地法律法规以及相关服务条款。因使用本项目产生的任何法律、版权、账号、数据或财务风险均由使用者自行承担。

## License

遵循 [GNU GPL v3](./LICENSE) 协议。
