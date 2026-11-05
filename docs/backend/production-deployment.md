# 本仓库生产发布

维护主线为 [TNTest/infinite-canvas](https://github.com/TNTest/infinite-canvas) 的 `main`，生产镜像来自 `ghcr.io/tntest/infinite-canvas`。保留原作者信息和开源协议；上游仓库不再是生产拉取来源。

## 发布代码与镜像

1. 将验证通过的改动合并到 `main`，更新 `VERSION` 和 `CHANGELOG.md`，同步根目录 `docker-compose.yml` 的本仓库镜像版本。
2. 推送主线及对应 `v*` 标签，触发本仓库 `Docker image` 工作流，若 fork 未自动启动镜像工作流，可运行 `gh workflow run docker-image.yml --repo TNTest/infinite-canvas --ref <版本标签>`，再等待已有检查与镜像构建完成。镜像工作流显式使用小写命名空间，适用于当前 fork 所有者。
3. 核对镜像的 `org.opencontainers.image.source` 为本仓库，`org.opencontainers.image.revision` 等于发布提交。正式部署使用 `ghcr.io/tntest/infinite-canvas@sha256:...` 固定摘要，避免标签变动导致版本漂移。

## 宝塔 Compose 升级

沿用已有宝塔 Docker Compose 项目、站点反向代理、HTTPS、端口绑定及数据挂载，不另起一套服务。

- 先保存原 Compose 配置、环境文件、原镜像摘要和可恢复的数据备份。环境文件及管理员凭据只保留在服务器，不提交到 Git。
- SQLite 使用 WAL 时，采用 SQLite 在线备份或停止容器后备份完整数据目录，不能只复制运行中的 `.db` 文件。
- 在原宝塔 Compose 项目中，仅把 `services.app.image` 换为本仓库已验证的固定镜像摘要。保留现有 `.env`、数据路径、宿主机绑定、健康检查、日志轮转及重启策略。
- 用户 Key 复制功能需要服务器内生成并保存独立的 `API_KEY_ENCRYPTION_KEY`（64 位随机十六进制）。首次配置只追加该变量，保留原有环境变量的值；后续部署必须沿用同一值，不输出到日志或导出到本地。将包含该值的目标环境文件另存一份服务器私有备份。
- 宝塔 `env/s` 参数会去掉环境文件末尾换行。原生保存 Compose 后应原样恢复已确认的目标环境文件字节，再校验原有变量未变、加密配置存在且权限为 600。
- 通过宝塔原生 Compose 管理接口更新项目，等待健康检查成功。站点与证书继续由宝塔维护。
- 可在 Compose 项目目录内保留本仓库 `main` 的源码检出，`origin` 指向 `https://github.com/TNTest/infinite-canvas.git`，检出提交应与镜像 revision 相同。

## 验收与回滚

通过正式 HTTPS 域名检查首页、静态资源、健康接口和登录；创建临时 Key 验证刷新/重新登录后取回原完整 Key、业务读取、敏感接口拒绝、限流/累计额度及撤销，之后清理临时 Key。验证不得向模型供应商发送付费测试请求。

异常时先保留新数据现场，通过原 Compose 管理接口恢复原镜像和原配置；即使回滚旧程序，也应保留新增的加密主密钥环境变量及其服务器私有备份，避免留下密文却丢掉主密钥。若出现不兼容的数据变更，再按备份计划恢复数据，避免直接覆盖上线后产生的新记录。

API Key 权限与限额规则见 [接口响应约定](api-response.md#用户-api-key)。
