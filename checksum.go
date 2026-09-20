package blocksync

import "hash/crc32"

// weakChecksum 是可滚动的弱核对：a 为字节和，b 为加权和。
// 撞上了不算数，必须再用 strongChecksum 切开。
type weakChecksum struct {
	a, b uint32
}

func weakSum(p []byte) weakChecksum {
	var w weakChecksum
	for _, c := range p {
		w.a += uint32(c)
	}
	for i, c := range p {
		w.b += uint32(len(p)-i) * uint32(c)
	}
	w.a &= 0xffff
	w.b &= 0xffff
	return w
}

// roll 把窗口从 p[i:i+n] 滑到 p[i+1:i+n+1]，out 为滑出字节，in 为滑入字节。
func (w *weakChecksum) roll(out, in byte, n int) {
	w.a = (w.a - uint32(out) + uint32(in)) & 0xffff
	w.b = (w.b - uint32(n)*uint32(out) + w.a) & 0xffff
}

func (w weakChecksum) sum() uint32 {
	return w.b<<16 | w.a
}

// strongChecksum 是硬核对，弱核对撞上时用它区分两块。
func strongSum(p []byte) uint32 {
	return crc32.ChecksumIEEE(p)
}
