# 知识库 SSH 入口

该服务是 `docker-compose.yml` 中的可选 `ssh` profile，提供普通 Linux
Shell、只读知识库和 `rags` CLI。SSH 容器不持有 Qdrant 地址或
SiliconFlow API Key；`rags grep` 通过现有的只读 `rags serve` 查询。

## 启动

1. 在 `.env` 中确认知识库宿主机路径：

   ```dotenv
   DOCS_PATH=/disk4/PublicHttp/chenshaobin/textDoc
   COMPOSE_PROFILES=ssh
   RAGS_SSH_PORT=39222
   RAGS_SSH_PASSWORD=knowledge
   ```

默认用户名和密码均为 `knowledge`，可通过 `RAGS_SSH_PASSWORD` 修改密码。

2. 构建并启动：

   ```bash
   docker compose up -d --build
   ```

不需要 SSH 时，删除或注释 `.env` 中的 `COMPOSE_PROFILES=ssh`，然后启动：

```bash
docker compose up -d
```

## 使用

```bash
ssh -p 39222 knowledge@172.18.1.140
# 输入 .env 中的 RAGS_SSH_PASSWORD

cd /disk4/PublicHttp/chenshaobin/textDoc
rags grep "BK7258 芯片性能" -n 5
rg "关键字"
find . -iname '*关键字*'
```

进入知识库子目录后执行 `rags grep`，检索范围会自动限制在该子目录。

## 重建

```bash
docker compose up -d --build --force-recreate ssh
```
