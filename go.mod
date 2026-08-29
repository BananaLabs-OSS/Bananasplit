module github.com/bananalabs-oss/bananasplit

go 1.25.6

require (
	github.com/BananaLabs-OSS/Pulp-Lua v0.0.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require (
	github.com/BananaLabs-OSS/Fiber v0.0.0 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	github.com/yuin/gopher-lua v1.1.2 // indirect
)

replace (
	github.com/BananaLabs-OSS/Fiber => ../Fiber
	github.com/BananaLabs-OSS/Pulp-Lua => ../Pulp-Lua
)
