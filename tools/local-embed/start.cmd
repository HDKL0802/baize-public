@echo off
rem 白泽记忆的本地向量化服务（NAS 后端会来调这个端口）
rem 手动启动：双击本文件；开机自启：计划任务 BaizeLocalEmbed
cd /d "%~dp0"
set EMBED_MODEL=Xenova/bge-small-zh-v1.5
set EMBED_POOLING=cls
start "baize-local-embed" /min node server.js 8484
