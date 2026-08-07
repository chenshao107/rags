# rags

本地语义检索 CLI。对项目文件建索引，用自然语言搜索（`grep` 的语义版）。

依赖：Qdrant、SiliconFlow API key（[siliconflow.cn](https://siliconflow.cn)）。

## 安装

```bash
git clone git@github.com:chenshao107/rags.git
cd rags
go build -o rags ./cmd/rags/
mkdir -p ~/.local/bin && cp rags ~/.local/bin/
```

## 命令

| 命令 | 说明 |
|------|------|
| `rags init` | 初始化项目（不写向量，需再 `update`） |
| `rags update` | 建索引 / 增量更新 |
| `rags grep <query>` | 语义搜索（`-n` 条数，`-k` 多关键词，`-d` 去重，`-e` explore） |
| `rags files` | 列出已索引文件 |
| `rags config` | 查看配置 |
| `rags projects` | 列出已注册项目 |

`rags --help` 查看各命令详情。

---

## 用法一：个人私有库

适合：自己的项目/文档，本机开发和检索。

**准备 Qdrant：**

```bash
docker run -d --name qdrant -p 6333:6333 -v qdrant_data:/qdrant/storage qdrant/qdrant
```

**初始化（首次会交互配置 API key，写入 `~/.rags/settings.yaml`）：**

```bash
cd ~/my-project
rags init
# 按需编辑 .rags/config.yaml（chunk、overlap 等）和 ~/.rags/settings.yaml（模型）
rags update
```

**日常使用：**

```bash
rags grep "你的问题" -n 10
rags update          # 文档变更后
```

---

## 用法二：公共库（systemd）

适合：编译服务器上共享文档，多人只读检索，维护者用 root 更新。

**一次性（root）：**

```bash
# 配置 /root/.rags/settings.yaml（可先在空目录 sudo rags init 交互生成）
sudo rags init --public --name bk7258-docs /path/to/docs
sudo rags update   # 在 /srv/rags/bk7258-docs 下调参后执行
# 或不指定 name：cd /path/to/docs && sudo rags init --public
```

**检索（任意能进入目录的用户，需本机安装 rags CLI）：**

```bash
cd /srv/rags/bk7258-docs
rags grep "BK7258 OTA 分区"
```

**更新索引（仅 root）：**

```bash
cd /srv/rags/bk7258-docs
sudo rags update
```

---

## 用法三：Docker Compose

适合：不想配 root/systemd，一条 compose 拉起 Qdrant + 公共检索服务。

```bash
cd docker
cp .env.example .env
# 编辑 .env：DOCS_PATH、SILICONFLOW_API_KEY、RAGS_NAME、RAGS_PORT、RAGS_PUBLIC_HOST

docker compose up -d --build
# 按需编辑 DOCS_PATH 下 .rags/config.yaml 和 docker/.env，然后：
./update.sh
```

**检索（宿主机安装 rags CLI，进入文档目录，与私有库用法相同）：**

```bash
cd /path/to/chip/docs          # 对应 .env 里 DOCS_PATH，首次启动后会有 .rags/
rags grep "BK7258 OTA 分区"

cd /path/to/chip/docs/api/     # 子目录内检索，只搜该目录及子树
rags grep "OTA 分区"
```

**更新索引：**

```bash
./update.sh
# 或
docker compose exec rags sh -c 'cd /data/docs && rags update'
```

远程用户检索时，将 `.env` 中 `RAGS_PUBLIC_HOST` 设为服务器 IP。

---

## License

MIT
