#!/bin/sh
# S3Bridge 容器入口。
#
# 后台「一键更新」会把校验通过的新版本写入数据卷 /app/data/bin。
# 这里优先加载数据卷中的版本，因此容器重建（compose up -d / 重启）后
# 不会回退到镜像内的旧版本，实现无需手工敲命令的持久化自更新。
set -e
mkdir -p /app/data/bin 2>/dev/null || true

if [ -x /app/data/bin/s3bridge ]; then
  exec /app/data/bin/s3bridge "$@"
fi
exec /app/bin/s3bridge "$@"
