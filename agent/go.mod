module baize

go 1.25.0

require (
	github.com/gorilla/websocket v1.5.3
	modernc.org/sqlite v1.59.0
)

require (
	baize/core v0.0.0
	baize/shared v0.0.0
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace baize/shared => ../shared

// 知识库直接复用手机内核那套领域能力（数据模型 / 密码合并与历史 / 加解密 / 统计），
// 两边的行为口径只有一份，不会漂移
replace baize/core => ../core
