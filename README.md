# mp4post

PromoForge 导出后处理库（Go，无接口层，输入输出均为本地文件）：

- **检查文件**：解析非分片 MP4 的索引，返回每条轨道的轨道号、类型（视频/音频/其他）、时间刻度、时长、样本数、关键帧数，以及索引是否已在第一个媒体数据块之前。
- **读样本**：按轨道号 + 样本序号（从 1 开始）返回样本字节、解码时间戳（DTS，按轨道自身时间刻度）、是否关键帧。无 stss 表的轨道每个样本都算关键帧。
- **找封面**：返回解码时间不晚于给定时间点的最后一个关键帧序号。
- **前置索引（faststart）**：把 moov 挪到第一个 mdat 前面并紧挨它，其余顶层块内容与相对顺序不变；索引已在前面时返回 `ErrAlreadyFastStart`，不写文件。

## 用法

```go
f, err := mp4post.Open("in.mp4")     // 只读索引，不碰媒体数据
tracks := f.Tracks()                 // []TrackInfo
ok := f.MoovBeforeFirstMdat()        // 索引是否已在前面
s, err := f.ReadSample(1, 42)        // 轨道 1 第 42 个样本
n, err := f.FindKeyframe(1, 90000)   // 封面关键帧序号
f.Close()

r, err := mp4post.Inspect("in.mp4")  // 一次性检查（Open + Tracks + 位置）

err := mp4post.FastStart("in.mp4", "out.mp4") // 前置索引
```

时间一律按轨道自身时间刻度和解码时间计算，不考虑编辑列表（elst）。

## 错误

分片 MP4（`moof`/`mvex`）→ `ErrFragmented`；压缩索引（`cmov`）→
`ErrCompressedMoov`；结构损坏/截断 → `ErrInvalidStructure`；其余见
`errors.go`。所有错误都明确返回，不崩溃、不卡住、不产出文件。
`FastStart` 先写同目录临时文件再 rename，任何一步出错原文件不受影响，
目标路径不留半截文件。

## 支持的文件形态

- 块长度 32 位与 64 位（largesize）两种写法；
- 最后一个顶层块长度为 0（直到文件末尾，moov 自身也可以是）；
- 多个 mdat，moov 夹在中间；
- 样本大小三种写法：stsz 统一值、stsz 逐个列出、stz2 按 4/8/16 位紧凑存放；
- tkhd/mdhd 的 32 位（v0）与 64 位（v1）时长版本；
- stco 与 co64 块偏移；前置时若偏移因此跨过 4 GiB，自动把 stco 升级为 co64。

## 非功能设计

- 检查/读样本/找封面只读索引，耗时与内存只随索引大小增长；样本定位
  全部走预计算的累计数组 + 二分查找，2M 样本轨道上随机读 1 万个样本
  约 8 ms（见 `TestRandomReadPerformance`）。
- `FastStart` 内存 = moov 本身 + 1 MiB 拷贝缓冲，不随文件增长；耗时为
  一遍顺序拷贝。除 stco/co64 及其祖先块的长度字段外，索引字节保持不变。

## 测试

```sh
go test ./...        # 全部测试（含 4 GiB 稀疏文件与 2M 样本性能用例）
go test -short ./... # 跳过大文件/性能用例
```

测试自带 MP4 构造器，无需任何外部视频工具；4 GiB 用例使用稀疏文件，
全部产物写在临时目录，跑完自动清理。
