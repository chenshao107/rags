# 知识库 SSH 入口

该服务提供普通 Linux Shell、只读知识库和 `rags` CLI。SSH 容器不持有
Qdrant 地址或 SiliconFlow API Key；`rags grep` 通过现有的只读
`rags serve` 查询。

## 启动

1. 在 `.env` 中确认知识库宿主机路径：

   ```dotenv
   DOCS_PATH=/disk4/PublicHttp/chenshaobin/textDoc
   RAGS_SSH_PORT=2222
   RAGS_SSH_PASSWORD=请替换为登录密码
   ```

2. 构建并启动：

   ```bash
   docker compose -f docker-compose.yml -f compose.ssh.yml up -d --build
   ```

## 使用

```bash
ssh -p 2222 knowledge@172.18.1.140
# 输入 .env 中的 RAGS_SSH_PASSWORD

cd /disk4/PublicHttp/chenshaobin/textDoc
rags grep "BK7258 芯片性能" -n 5
rg "关键字"
find . -iname '*关键字*'
```

进入知识库子目录后执行 `rags grep`，检索范围会自动限制在该子目录。

## 重建

```bash
docker compose -f docker-compose.yml -f compose.ssh.yml \
  up -d --build --force-recreate ssh
```
