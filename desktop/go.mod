module baize/desktop

go 1.25

require (
	baize/shared v0.0.0
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	golang.org/x/sys v0.0.0-20210218145245-beda7e5e158e
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
)

replace baize/shared => ../shared
